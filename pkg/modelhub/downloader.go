// Package modelhub downloads model weights from public model hubs
// (ModelScope, HuggingFace) into a local directory. The control plane
// writes into a JuiceFS mount so inference pods read the same weights.
package modelhub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// Source identifies a model hub.
type Source string

const (
	// SourceModelScope is the ModelScope hub.
	SourceModelScope Source = "modelscope"
	// SourceHuggingFace is the HuggingFace hub.
	SourceHuggingFace Source = "huggingface"
)

// ParseSource normalizes a source string into a Source. An empty or
// unknown source is an error.
func ParseSource(s string) (Source, error) {
	switch Source(strings.ToLower(strings.TrimSpace(s))) {
	case SourceModelScope:
		return SourceModelScope, nil
	case SourceHuggingFace:
		return SourceHuggingFace, nil
	default:
		return "", fmt.Errorf("modelhub: unsupported source %q", s)
	}
}

// Downloader downloads model weights from a hub into a local directory.
type Downloader interface {
	// Download fetches the model identified by modelID from the hub and
	// writes its files under destDir, preserving relative paths.
	Download(ctx context.Context, source Source, modelID, destDir string) error
}

// SizeReporter is implemented by a Downloader that can report how many
// bytes a model will occupy before the transfer starts, so progress can be
// a real percentage instead of an indeterminate animation.
type SizeReporter interface {
	// TotalBytes returns the summed size of the model's files on the hub.
	// A hub that reports no sizes yields 0.
	TotalBytes(ctx context.Context, source Source, modelID string) (int64, error)
}

// FileInfo is one downloadable file of a hub model.
type FileInfo struct {
	// Path is the file's path relative to the model root.
	Path string
	// Size is the file's size in bytes (0 when the hub omits it).
	Size int64
}

// TotalBytes implements SizeReporter.
func (d *HTTPDownloader) TotalBytes(ctx context.Context, source Source, modelID string) (int64, error) {
	files, err := d.listFiles(ctx, source, modelID)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}
	return total, nil
}

// Options configures an HTTPDownloader.
type Options struct {
	// BaseURL overrides the hub base URL (used by tests). Empty uses
	// the production hub URLs.
	BaseURL string
	// HTTPClient is the HTTP client used for listing and downloading.
	// Defaults to http.DefaultClient.
	HTTPClient *http.Client
	// MaxConcurrency bounds concurrent file downloads. Defaults to 4.
	MaxConcurrency int
}

// HTTPDownloader is the default Downloader backed by the hub HTTP APIs.
type HTTPDownloader struct {
	baseURL string
	client  *http.Client
	sem     chan struct{}
}

// NewHTTPDownloader builds an HTTPDownloader from options.
func NewHTTPDownloader(opts Options) *HTTPDownloader {
	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	conc := opts.MaxConcurrency
	if conc <= 0 {
		conc = 4
	}
	return &HTTPDownloader{
		baseURL: strings.TrimRight(opts.BaseURL, "/"),
		client:  client,
		sem:     make(chan struct{}, conc),
	}
}

// hubBaseURL returns the effective base URL for a source.
func (d *HTTPDownloader) hubBaseURL(source Source) string {
	if d.baseURL != "" {
		return d.baseURL
	}
	switch source {
	case SourceModelScope:
		return "https://modelscope.cn"
	case SourceHuggingFace:
		return "https://huggingface.co"
	default:
		return ""
	}
}

// Download implements Downloader.
func (d *HTTPDownloader) Download(ctx context.Context, source Source, modelID, destDir string) error {
	files, err := d.listFiles(ctx, source, modelID)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("modelhub: model %q has no downloadable files", modelID)
	}
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return fmt.Errorf("modelhub: create dest dir: %w", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(files))
	for _, f := range files {
		wg.Add(1)
		go func(file string) {
			defer wg.Done()
			if err := d.downloadFile(ctx, source, modelID, file, destDir); err != nil {
				errCh <- err
			}
		}(f.Path)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

// listFiles returns the relative file paths of a model on the hub.
func (d *HTTPDownloader) listFiles(ctx context.Context, source Source, modelID string) ([]FileInfo, error) {
	base := d.hubBaseURL(source)
	if base == "" {
		return nil, fmt.Errorf("modelhub: unsupported source %q", source)
	}
	var listURL string
	switch source {
	case SourceModelScope:
		listURL = base + "/api/v1/models/" + url.PathEscape(modelID) + "/repo/files?Revision=master&Recursive=true"
	case SourceHuggingFace:
		listURL = base + "/api/models/" + url.PathEscape(modelID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("modelhub: list files: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("modelhub: list files: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("modelhub: read list response: %w", err)
	}
	switch source {
	case SourceModelScope:
		return parseModelScopeFiles(body)
	case SourceHuggingFace:
		return parseHuggingFaceFiles(body)
	}
	return nil, fmt.Errorf("modelhub: unsupported source %q", source)
}

// modelscopeListResponse is the ModelScope repo-files API response.
type modelscopeListResponse struct {
	Data struct {
		Files []struct {
			Path string `json:"Path"`
			Size int64  `json:"Size"`
		} `json:"Files"`
	} `json:"Data"`
}

// parseModelScopeFiles extracts the files from a ModelScope repo-files
// response.
func parseModelScopeFiles(body []byte) ([]FileInfo, error) {
	var resp modelscopeListResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("modelhub: decode modelscope list: %w", err)
	}
	out := make([]FileInfo, 0, len(resp.Data.Files))
	for _, f := range resp.Data.Files {
		if strings.TrimSpace(f.Path) != "" {
			out = append(out, FileInfo{Path: f.Path, Size: f.Size})
		}
	}
	return out, nil
}

// huggingFaceListResponse is the HuggingFace model-info API response.
// blobId, lfs.size and size are the three places HuggingFace reports a
// file's byte count.
type huggingFaceListResponse struct {
	Siblings []struct {
		Rfilename string `json:"rfilename"`
		Size      int64  `json:"size"`
		LFS       *struct {
			Size int64 `json:"size"`
		} `json:"lfs"`
	} `json:"siblings"`
}

// parseHuggingFaceFiles extracts the files from a HuggingFace model-info
// response.
func parseHuggingFaceFiles(body []byte) ([]FileInfo, error) {
	var resp huggingFaceListResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("modelhub: decode huggingface list: %w", err)
	}
	out := make([]FileInfo, 0, len(resp.Siblings))
	for _, s := range resp.Siblings {
		if strings.TrimSpace(s.Rfilename) == "" {
			continue
		}
		size := s.Size
		if s.LFS != nil && s.LFS.Size > 0 {
			size = s.LFS.Size
		}
		out = append(out, FileInfo{Path: s.Rfilename, Size: size})
	}
	return out, nil
}

// downloadFile downloads one model file into destDir, preserving its
// relative path. The download URL is derived per source.
func (d *HTTPDownloader) downloadFile(ctx context.Context, source Source, modelID, file, destDir string) error {
	d.sem <- struct{}{}
	defer func() { <-d.sem }()

	base := d.hubBaseURL(source)
	var dlURL string
	switch source {
	case SourceModelScope:
		dlURL = base + "/models/" + url.PathEscape(modelID) + "/resolve/master/" + pathEscapePath(file)
	case SourceHuggingFace:
		dlURL = base + "/" + url.PathEscape(modelID) + "/resolve/main/" + pathEscapePath(file)
	default:
		return fmt.Errorf("modelhub: unsupported source %q", source)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("modelhub: download %s: %w", file, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("modelhub: download %s: unexpected status %d", file, resp.StatusCode)
	}

	dest := filepath.Join(destDir, filepath.FromSlash(file))
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return fmt.Errorf("modelhub: mkdir %s: %w", filepath.Dir(dest), err)
	}
	// #nosec G304 -- dest is derived from the validated model file list
	// and the caller-provided destination directory.
	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("modelhub: create %s: %w", dest, err)
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("modelhub: write %s: %w", dest, err)
	}
	return nil
}

// pathEscapePath escapes each path segment so nested files survive URL
// encoding while slashes are preserved.
func pathEscapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// CleanModelID sanitizes a hub model id into a safe directory name.
// Hub ids are "owner/name" pairs; the slash is replaced so the id can
// be used as a single path segment.
func CleanModelID(modelID string) string {
	return strings.ReplaceAll(strings.TrimSpace(modelID), "/", "__")
}

// JoinPath joins a base directory and a model id into the download
// destination directory, sanitizing the id.
func JoinPath(baseDir, modelID string) string {
	return path.Join(baseDir, CleanModelID(modelID))
}
