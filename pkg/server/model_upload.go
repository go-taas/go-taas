// Model upload over HTTP.
//
// A model archive is far too large for a JSON field: a gRPC-JSON transcoded
// call would have to buffer and base64 it, which triples the memory the
// server needs before a byte lands on disk. The upload is therefore a plain
// streaming endpoint:
//
//	POST /api/v1/admin/models:upload   (multipart/form-data)
//	  file         the archive (.tar, .tar.gz, .tgz or .zip)
//	  name         the catalog model name
//	  version      the catalog version (optional)
//	  description  the model description (optional)
//
// The server streams the archive to a temp file, then hands the path to the
// model module, which extracts it into the weights filesystem and registers
// the version. The staged file is removed when the call returns, so a failed
// upload leaves nothing behind.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/go-taas/go-taas/pkg/authctx"
	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
)

// ModelUploadPath is the admin endpoint that accepts a model archive.
const ModelUploadPath = "/api/v1/admin/models:upload"

// modelUploadDefaultMaxBytes bounds an upload when the configuration says
// nothing: 200 GiB covers every open model in one archive.
const modelUploadDefaultMaxBytes = int64(200) << 30

// ModelUploadRequest is one upload, already staged to a temp file.
type ModelUploadRequest struct {
	// ArchivePath is the staged archive; the handler removes it.
	ArchivePath  string
	OriginalName string
	Name         string
	Version      string
	Description  string
}

// ModelUploadResult is the catalog entry the upload produced.
type ModelUploadResult struct {
	ModelID    string `json:"modelId"`
	Version    string `json:"version"`
	WeightPath string `json:"weightPath"`
	Bytes      int64  `json:"bytes"`
	Files      int    `json:"files"`
}

// ModelUploadFunc performs an upload. It is implemented by the model module
// and registered at wiring time.
type ModelUploadFunc func(ctx context.Context, req ModelUploadRequest) (*ModelUploadResult, error)

// SetModelUpload installs the model-upload handler. It must be called before
// Init. Nil leaves the endpoint unregistered.
func (s *commonServer) SetModelUpload(fn ModelUploadFunc) {
	s.modelUpload = fn
}

// registerModelUploadRoute mounts the upload endpoint on the gateway mux.
// Like the image upload it is a path route, because the body is a binary
// stream; the realm guard and the console handler wrap the mux and treat it
// as any other admin path.
func (s *commonServer) registerModelUploadRoute() {
	if s.gatewayMux == nil || s.modelUpload == nil {
		return
	}
	if err := s.gatewayMux.HandlePath(http.MethodPost, ModelUploadPath,
		func(w http.ResponseWriter, r *http.Request, _ map[string]string) {
			s.modelUploadHandler(w, r)
		}); err != nil {
		logger.S().Fatalf("gateway register %s failed: %v", ModelUploadPath, err)
	}
}

// modelUploadHandler streams the multipart archive and delegates the
// extraction and registration to the model module.
func (s *commonServer) modelUploadHandler(w http.ResponseWriter, r *http.Request) {
	if s.modelUpload == nil {
		writeBusinessError(w, int(apierrors.CodeModelPathInvalid), "model: upload is not configured")
		return
	}
	maxBytes := modelUploadDefaultMaxBytes
	if cfg := config.GetConfig(); cfg != nil && cfg.Model.Upload.MaxArchiveBytes > 0 {
		maxBytes = cfg.Model.Upload.MaxArchiveBytes
	}
	// Bound the request before reading a byte, so an oversized upload is
	// rejected by the framing rather than by a full disk.
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		writeBusinessError(w, int(apierrors.CodeModelPathInvalid),
			fmt.Sprintf("model: the upload must be multipart/form-data (max %d bytes): %v", maxBytes, err))
		return
	}

	fields := map[string]string{}
	archivePath := ""
	originalName := ""
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			removeStaged(archivePath)
			writeBusinessError(w, int(apierrors.CodeModelPathInvalid),
				fmt.Sprintf("model: could not read the upload: %v", err))
			return
		}
		if part.FormName() == "file" {
			path, stageErr := stageModelUpload(part)
			_ = part.Close()
			if stageErr != nil {
				removeStaged(archivePath)
				logger.S().Errorw("model: stage upload failed", "err", stageErr)
				writeBusinessError(w, int(apierrors.CodeModelPathInvalid), "model: could not store the upload")
				return
			}
			removeStaged(archivePath)
			archivePath = path
			originalName = part.FileName()
			continue
		}
		value, readErr := io.ReadAll(io.LimitReader(part, maxUploadFieldBytes))
		_ = part.Close()
		if readErr != nil {
			removeStaged(archivePath)
			writeBusinessError(w, int(apierrors.CodeModelPathInvalid), "model: could not read the upload")
			return
		}
		fields[part.FormName()] = string(value)
	}
	if archivePath == "" {
		writeBusinessError(w, int(apierrors.CodeModelPathInvalid), "model: the upload needs a file field")
		return
	}
	defer removeStaged(archivePath)

	ctx := uploadContext(r)
	if s.sessionRoles != nil {
		ctx = authctx.WithResolver(ctx, s.sessionRoles)
	}
	result, err := s.modelUpload(ctx, ModelUploadRequest{
		ArchivePath:  archivePath,
		OriginalName: originalName,
		Name:         fields["name"],
		Version:      fields["version"],
		Description:  fields["description"],
	})
	if err != nil {
		code, message := modelUploadError(err)
		writeBusinessError(w, code, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"response":   map[string]any{"code": 0, "message": "OK"},
		"modelId":    result.ModelID,
		"version":    result.Version,
		"weightPath": result.WeightPath,
		"bytes":      result.Bytes,
		"files":      result.Files,
	})
}

// stageModelUpload copies the multipart part into a temp file and returns its
// path. Only the archive is written here: the extraction happens in the model
// module, under the weights filesystem.
func stageModelUpload(src io.Reader) (string, error) {
	dir := ""
	if cfg := config.GetConfig(); cfg != nil {
		dir = cfg.Model.Upload.TempDir
	}
	temp, err := os.CreateTemp(dir, "taas-model-*")
	if err != nil {
		return "", err
	}
	path := temp.Name()
	if _, err := io.Copy(temp, src); err != nil {
		_ = temp.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// modelUploadError maps an upload failure onto the unified envelope. A
// business error keeps its code; anything else is an internal failure.
func modelUploadError(err error) (int, string) {
	if ae, ok := apierrors.As(err); ok {
		return int(ae.Code), ae.Message
	}
	if errors.Is(err, context.Canceled) {
		return int(apierrors.CodeModelPathInvalid), "model: upload cancelled"
	}
	logger.S().Warnw("model: upload failed", "err", err)
	return int(apierrors.CodeModelPathInvalid), err.Error()
}
