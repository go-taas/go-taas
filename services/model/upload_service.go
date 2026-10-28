// Local model upload.
//
// The catalog could only ever be filled from a public hub: the downloader
// fetched the weights and registered the model. An operator with weights on
// their own disk (a fine-tuned checkpoint, an offline installation, a model
// the public hubs do not carry) had no way in. This file is that way: an
// archive of a model directory is streamed to the weights filesystem,
// extracted, and registered exactly like a download's result.
//
// The flow mirrors the download job's tail — weights on disk, then a
// catalog registration — so a model that arrived by upload is
// indistinguishable from one that arrived by download (same weight_path
// shape, same deploy path, same delete-time cleanup).

package model

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/audit"
)

// Upload limits. The archive is streamed, so the bound exists to stop a
// runaway upload rather than to fit anything in memory.
const (
	// maxUploadExtractedBytes caps the total size of the extracted files,
	// so a compressed bomb cannot fill the weights filesystem. 4 TiB is far
	// beyond any single model and still bounds the damage.
	maxUploadExtractedBytes = int64(4) << 40
	// maxUploadFiles caps the file count of one archive.
	maxUploadFiles = 200000
	// maxUploadEntryBytes caps one extracted file.
	maxUploadEntryBytes = int64(512) << 30
)

// UploadModelRequest is one staged local upload.
type UploadModelRequest struct {
	// ArchivePath is the staged archive (.tar, .tar.gz, .tgz or .zip).
	ArchivePath string
	// OriginalName is the uploaded file's name, used to pick the format
	// and to name the job in the audit trail.
	OriginalName string
	// Name is the catalog model name (required).
	Name string
	// Version is the catalog version; empty derives one from the upload
	// time.
	Version string
	// Description is the free-text model description.
	Description string
	// Actor is the uploading user's id, for the audit trail.
	Actor string
}

// UploadModelResult is the catalog entry the upload produced.
type UploadModelResult struct {
	ModelID    string `json:"modelId"`
	Version    string `json:"version"`
	WeightPath string `json:"weightPath"`
	Bytes      int64  `json:"bytes"`
	Files      int    `json:"files"`
}

// UploadModel extracts a local archive into the weights filesystem and
// registers the model version.
//
// The destination is "<clean name>/<version>/" under the weights root, which
// is the same relative shape a hub download produces (a directory the engine
// is pointed at), and it keeps two versions of one model apart.
func (s *Service) UploadModel(ctx context.Context, req UploadModelRequest) (*UploadModelResult, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, err
	}
	if s.weightsDir == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: the weights directory is not configured, so a local upload cannot be stored")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: name is required")
	}
	if len(name) > 128 {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: name must be 1-128 characters")
	}
	version := strings.TrimSpace(req.Version)
	if version == "" {
		version = "upload-" + time.Now().UTC().Format("20060102-150405")
	}
	if len(version) > 64 {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: version must be 1-64 characters")
	}
	description := strings.TrimSpace(req.Description)
	if len(description) > 1024 {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: description must be at most 1024 characters")
	}
	format, err := uploadFormat(req.ArchivePath, req.OriginalName)
	if err != nil {
		return nil, err
	}
	if req.ArchivePath == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: the upload needs an archive")
	}

	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	// Refuse an upload that can never register, before unpacking gigabytes:
	// a model of that name bound to a hub id is a different thing entirely.
	if err := s.checkHubBinding(ctx, repo, name, ""); err != nil {
		return nil, err
	}

	// A version that already exists is refused before anything is unpacked:
	// extracting first and failing afterwards would have either overwritten
	// the registered weights or deleted them (the destination directory is
	// cleared when it is stale).
	existing, err := repo.FindByName(ctx, name)
	if err != nil && apierrors.CodeOf(err) != apierrors.CodeModelNotFound {
		return nil, err
	}
	if err == nil {
		if _, versionErr := repo.FindVersion(ctx, existing.ID, version); versionErr == nil {
			return nil, apierrors.Newf(apierrors.CodeModelExists,
				"model: model %q already has version %q", name, version)
		} else if apierrors.CodeOf(versionErr) != apierrors.CodeModelVersionNotFound {
			return nil, versionErr
		}
	}

	rel := path.Join(cleanModelDirName(name), sanitizeVersion(version))
	dest := filepath.Join(s.weightsDir, filepath.FromSlash(rel))
	if existing, statErr := os.Stat(dest); statErr == nil && existing.IsDir() {
		// The directory survives only from an interrupted or orphaned
		// upload; a registered version would have been rejected above.
		if err := os.RemoveAll(dest); err != nil {
			return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
				"model: could not clear the previous upload at %s: %v", rel, err)
		}
	}
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: could not create the weights directory: %v", err)
	}
	extracted, files, err := extractArchive(req.ArchivePath, format, dest)
	if err != nil {
		// A half-extracted directory is worse than none: it would be served
		// by a later deploy as if it were a model.
		_ = os.RemoveAll(dest)
		return nil, err
	}
	if files == 0 {
		_ = os.RemoveAll(dest)
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: the archive contains no files")
	}

	weightPath := rel + "/"
	modelID, err := repo.RegisterModelOrCreateVersion(ctx, name, description, "", version, weightPath)
	if err != nil {
		// The registration was refused (a duplicate version, most likely):
		// the extracted bytes belong to nobody, so they go.
		_ = os.RemoveAll(dest)
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		ActorUserID:  s.grantedBy(ctx),
		ActorType:    "user",
		Action:       "model.upload",
		ResourceType: "model",
		ResourceID:   modelID,
		Result:       "success",
	})
	return &UploadModelResult{
		ModelID:    modelID,
		Version:    version,
		WeightPath: weightPath,
		Bytes:      extracted,
		Files:      files,
	}, nil
}

// uploadFormat decides the archive format from the staged file's extension.
func uploadFormat(archivePath, originalName string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(originalName))
	if name == "" {
		name = strings.ToLower(archivePath)
	}
	switch {
	case strings.HasSuffix(name, ".zip"):
		return "zip", nil
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return "tar.gz", nil
	case strings.HasSuffix(name, ".tar"):
		return "tar", nil
	default:
		return "", apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: the archive must be a .tar, .tar.gz, .tgz or .zip file")
	}
}

// cleanModelDirName turns a catalog name into one safe directory segment.
//
// The hub path helper only replaces "/", which leaves a space or a quote in a
// weight path that the engine command has to carry. Everything outside
// [A-Za-z0-9._-] becomes "-", so the directory the model is extracted into is
// readable and never needs quoting.
func cleanModelDirName(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "model-" + uuid.NewString()[:8]
	}
	return out
}

// sanitizeVersion keeps a version usable as a single path segment.
func sanitizeVersion(version string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", "..", "-", " ", "-")
	out := replacer.Replace(strings.TrimSpace(version))
	if out == "" {
		return "upload-" + uuid.NewString()[:8]
	}
	return out
}

// extractArchive unpacks an archive into dest, returning the number of bytes
// and files written.
//
// Every entry is confined to dest: an absolute path, a ".." segment or a
// symlink that points outside the directory is an error rather than a
// silently-skipped entry, because an operator needs to know their archive was
// not extracted as intended.
func extractArchive(archivePath, format, dest string) (int64, int, error) {
	file, err := os.Open(filepath.Clean(archivePath))
	if err != nil {
		return 0, 0, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: could not open the uploaded archive: %v", err)
	}
	defer func() { _ = file.Close() }()

	switch format {
	case "zip":
		info, statErr := file.Stat()
		if statErr != nil {
			return 0, 0, apierrors.Newf(apierrors.CodeModelPathInvalid,
				"model: could not stat the uploaded archive: %v", statErr)
		}
		return extractZip(file, info.Size(), dest)
	case "tar.gz":
		gz, gzErr := gzip.NewReader(file)
		if gzErr != nil {
			return 0, 0, apierrors.Newf(apierrors.CodeModelPathInvalid,
				"model: the archive is not valid gzip: %v", gzErr)
		}
		defer func() { _ = gz.Close() }()
		return extractTar(gz, dest)
	default:
		return extractTar(file, dest)
	}
}

// extractTar reads a tar stream into dest.
func extractTar(src io.Reader, dest string) (int64, int, error) {
	reader := tar.NewReader(src)
	var total int64
	files := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return total, files, nil
		}
		if err != nil {
			return total, files, apierrors.Newf(apierrors.CodeModelPathInvalid,
				"model: the archive is not a valid tar: %v", err)
		}
		if header.Typeflag == tar.TypeDir {
			if _, err := safeJoin(dest, header.Name); err != nil {
				return total, files, err
			}
			continue
		}
		if header.Typeflag != tar.TypeReg {
			// Links, devices and fifos have no business in a model
			// directory and are the classic escape vector.
			return total, files, apierrors.Newf(apierrors.CodeModelPathInvalid,
				"model: the archive contains an unsupported entry %q (only files and directories are allowed)",
				header.Name)
		}
		files++
		if files > maxUploadFiles {
			return total, files, apierrors.Newf(apierrors.CodeModelPathInvalid,
				"model: the archive contains more than %d files", maxUploadFiles)
		}
		if header.Size > maxUploadEntryBytes || total+header.Size > maxUploadExtractedBytes {
			return total, files, apierrors.Newf(apierrors.CodeModelPathInvalid,
				"model: the archive expands beyond the %d byte limit", maxUploadExtractedBytes)
		}
		written, err := writeEntry(dest, header.Name, reader)
		if err != nil {
			return total, files, err
		}
		total += written
	}
}

// extractZip reads a zip archive into dest.
func extractZip(file *os.File, size int64, dest string) (int64, int, error) {
	reader, err := zip.NewReader(file, size)
	if err != nil {
		return 0, 0, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: the archive is not a valid zip: %v", err)
	}
	var total int64
	files := 0
	for _, entry := range reader.File {
		if _, err := safeJoin(dest, entry.Name); err != nil {
			return total, files, err
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return total, files, apierrors.Newf(apierrors.CodeModelPathInvalid,
				"model: the archive contains the symlink %q, which is not allowed", entry.Name)
		}
		files++
		if files > maxUploadFiles {
			return total, files, apierrors.Newf(apierrors.CodeModelPathInvalid,
				"model: the archive contains more than %d files", maxUploadFiles)
		}
		//nolint:gosec // G115: the zip header's size is bounded by the archive's actual size
		size := int64(entry.UncompressedSize64)
		if size > maxUploadEntryBytes || total+size > maxUploadExtractedBytes {
			return total, files, apierrors.Newf(apierrors.CodeModelPathInvalid,
				"model: the archive expands beyond the %d byte limit", maxUploadExtractedBytes)
		}
		opened, openErr := entry.Open()
		if openErr != nil {
			return total, files, apierrors.Newf(apierrors.CodeModelPathInvalid,
				"model: could not read %q from the archive: %v", entry.Name, openErr)
		}
		written, writeErr := writeEntry(dest, entry.Name, opened)
		_ = opened.Close()
		if writeErr != nil {
			return total, files, writeErr
		}
		total += written
	}
	return total, files, nil
}

// writeEntry writes one archive entry under dest, creating its directory.
func writeEntry(dest, name string, src io.Reader) (int64, error) {
	target, err := safeJoin(dest, name)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return 0, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: could not create the directory for %q: %v", name, err)
	}
	// 0644 matches what the hub downloader writes: the weights directory is a
	// shared read-only mount for engine pods, which may not run as this
	// process's user.
	//nolint:gosec // G302/G304: world-readable is intended here, and target is resolved under the weights root by safeJoin
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: could not write %q: %v", name, err)
	}
	written, copyErr := io.Copy(out, io.LimitReader(src, maxUploadEntryBytes))
	closeErr := out.Close()
	if copyErr != nil {
		return written, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: could not write %q: %v", name, copyErr)
	}
	if closeErr != nil {
		return written, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: could not write %q: %v", name, closeErr)
	}
	return written, nil
}

// safeJoin resolves an archive entry name under dest and refuses anything
// that would land outside it.
//
// A ".." segment is rejected outright rather than normalised away: silently
// relocating an entry would extract a file the operator did not pack in the
// place they expected, and a ".." in a model archive is a mistake or an attack,
// never a layout choice.
func safeJoin(dest, name string) (string, error) {
	clean := strings.ReplaceAll(name, "\\", "/")
	for _, segment := range strings.Split(clean, "/") {
		if segment == ".." {
			return "", apierrors.Newf(apierrors.CodeModelPathInvalid,
				"model: the archive entry %q escapes the weights directory", name)
		}
	}
	cleaned := path.Clean("/" + clean)
	target := filepath.Join(dest, filepath.FromSlash(cleaned))
	root := filepath.Clean(dest)
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: the archive entry %q escapes the weights directory", name)
	}
	return target, nil
}
