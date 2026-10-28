// Image upload over HTTP.
//
// The API is gRPC-JSON transcoded, and transcoding cannot carry a binary
// body: a `docker save` archive is far too large for a JSON field and
// base64-ing it would triple the memory the server needs. The upload is
// therefore a plain streaming HTTP endpoint:
//
//	POST /api/v1/admin/images:upload   (multipart/form-data)
//	  file        the archive (.tar or .tar.gz)
//	  accelerator / engine / description / name / tag / sourceReference
//
// The server streams the file to a temp file, then hands the path to the
// image module through the ImageUploadFunc seam. Authentication is the same
// as every other admin call: the request's Authorization (and
// x-organization-id) headers are forwarded into the gRPC metadata the
// session resolver reads, and the caller's session roles are parked on the
// context the same way the unary interceptor does it.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"google.golang.org/grpc/metadata"

	"github.com/go-taas/go-taas/pkg/authctx"
	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
)

// ImageUploadPath is the admin endpoint that accepts an image archive.
const ImageUploadPath = "/api/v1/admin/images:upload"

// ImageUploadRequest is one upload, already staged to a temp file.
type ImageUploadRequest struct {
	// ArchivePath is the staged archive. The handler owns the file and
	// removes it when the call returns.
	ArchivePath string
	Name        string
	Tag         string
	// SourceReference is the archive's image reference as the operator
	// typed it, used when the archive carries no tag.
	SourceReference string
	Accelerator     string
	Engine          string
	Description     string
	// Actor is the authenticated caller's user id, for the audit trail.
	Actor string
}

// ImageUploadResult is the catalog entry the upload produced.
type ImageUploadResult struct {
	ImageID   string `json:"imageId"`
	Reference string `json:"reference"`
	Name      string `json:"name"`
	Tag       string `json:"tag"`
}

// ImageUploadFunc performs an upload. It is implemented by the image module
// and registered at wiring time.
type ImageUploadFunc func(ctx context.Context, req ImageUploadRequest) (*ImageUploadResult, error)

// SetImageUpload installs the image-upload handler. It must be called before
// Init. Nil leaves the endpoint unregistered.
func (s *commonServer) SetImageUpload(fn ImageUploadFunc) {
	s.imageUpload = fn
}

// registerUploadRoute mounts the upload endpoint on the gateway mux. It is a
// path route rather than a transcoded RPC because the body is a binary
// stream; the realm guard and the console handler wrap the mux and treat it
// like any other admin path.
func (s *commonServer) registerUploadRoute() {
	if s.gatewayMux == nil || s.imageUpload == nil {
		return
	}
	if err := s.gatewayMux.HandlePath(http.MethodPost, ImageUploadPath,
		func(w http.ResponseWriter, r *http.Request, _ map[string]string) {
			s.imageUploadHandler(w, r)
		}); err != nil {
		logger.S().Fatalf("gateway register %s failed: %v", ImageUploadPath, err)
	}
}

// imageUploadHandler streams a multipart archive to a temp file and delegates
// the push to the image module.
//
// The body is read as a stream (r.MultipartReader) rather than parsed into
// memory: an archive is hundreds of megabytes, and the only thing the server
// keeps is the staged file. Form fields are read from their parts with a
// small bound.
func (s *commonServer) imageUploadHandler(w http.ResponseWriter, r *http.Request) {
	if s.imageUpload == nil {
		writeBusinessError(w, int(apierrors.CodeImageReferenceInvalid), "image: upload is not configured")
		return
	}
	maxBytes := int64(8 << 30)
	if cfg := config.GetConfig(); cfg != nil && cfg.Image.Upload.MaxArchiveBytes > 0 {
		maxBytes = cfg.Image.Upload.MaxArchiveBytes
	}
	// Bound the request before reading a byte, so an oversized upload is
	// rejected by the framing rather than by a full disk.
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		writeBusinessError(w, int(apierrors.CodeImageReferenceInvalid),
			fmt.Sprintf("image: the upload must be multipart/form-data (max %d bytes): %v", maxBytes, err))
		return
	}

	fields := map[string]string{}
	archivePath := ""
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			removeStaged(archivePath)
			writeBusinessError(w, int(apierrors.CodeImageReferenceInvalid),
				fmt.Sprintf("image: could not read the upload: %v", err))
			return
		}
		if part.FormName() == "file" {
			path, stageErr := stageUpload(part, part.FileName())
			_ = part.Close()
			if stageErr != nil {
				removeStaged(archivePath)
				logger.S().Errorw("image: stage upload failed", "err", stageErr)
				writeBusinessError(w, int(apierrors.CodeImageReferenceInvalid), "image: could not store the upload")
				return
			}
			removeStaged(archivePath)
			archivePath = path
			continue
		}
		value, readErr := io.ReadAll(io.LimitReader(part, maxUploadFieldBytes))
		_ = part.Close()
		if readErr != nil {
			removeStaged(archivePath)
			writeBusinessError(w, int(apierrors.CodeImageReferenceInvalid), "image: could not read the upload")
			return
		}
		fields[part.FormName()] = string(value)
	}
	if archivePath == "" {
		writeBusinessError(w, int(apierrors.CodeImageReferenceInvalid), "image: the upload needs a file field")
		return
	}
	defer removeStaged(archivePath)

	ctx := uploadContext(r)
	if s.sessionRoles != nil {
		ctx = authctx.WithResolver(ctx, s.sessionRoles)
	}
	result, err := s.imageUpload(ctx, ImageUploadRequest{
		ArchivePath:     archivePath,
		Name:            fields["name"],
		Tag:             fields["tag"],
		SourceReference: fields["sourceReference"],
		Accelerator:     fields["accelerator"],
		Engine:          fields["engine"],
		Description:     fields["description"],
	})
	if err != nil {
		code, message := imageUploadError(err)
		writeBusinessError(w, code, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"response":  map[string]any{"code": 0, "message": "OK"},
		"imageId":   result.ImageID,
		"reference": result.Reference,
		"name":      result.Name,
		"tag":       result.Tag,
	})
}

// maxUploadFieldBytes bounds one form field of the upload.
const maxUploadFieldBytes = 1 << 20

// removeStaged deletes a staged archive, ignoring an empty path and a
// already-removed file.
func removeStaged(path string) {
	if path == "" {
		return
	}
	//nolint:gosec // G703: the path is the server's own temp file, never user input
	_ = os.Remove(path)
}

// stageUpload copies the multipart part into a temp file and returns its
// path. The archive is never extracted, so the only filesystem write is this
// one file in the configured temp directory.
func stageUpload(src io.Reader, filename string) (string, error) {
	dir := ""
	if cfg := config.GetConfig(); cfg != nil {
		dir = cfg.Image.Upload.TempDir
	}
	pattern := "taas-image-*"
	if ext := archiveExtension(filename); ext != "" {
		pattern += ext
	}
	temp, err := os.CreateTemp(dir, pattern)
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

// archiveExtension keeps the archive's extension on the staged file so a
// human (and skopeo's own detection) sees what it is.
func archiveExtension(filename string) string {
	name := strings.ToLower(strings.TrimSpace(filename))
	switch {
	case strings.HasSuffix(name, ".tar.gz"):
		return ".tar.gz"
	case strings.HasSuffix(name, ".tgz"):
		return ".tgz"
	case strings.HasSuffix(name, ".tar"):
		return ".tar"
	default:
		return ""
	}
}

// uploadContext forwards the request's credentials into gRPC metadata, so the
// session resolver sees exactly what it sees for a transcoded call.
func uploadContext(r *http.Request) context.Context {
	pairs := make([]string, 0, 4)
	for _, key := range []string{"authorization", organizationHeaderKey} {
		if value := strings.TrimSpace(r.Header.Get(key)); value != "" {
			pairs = append(pairs, strings.ToLower(key), value)
		}
	}
	ctx := r.Context()
	if len(pairs) > 0 {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(pairs...))
	}
	return ctx
}

// imageUploadError maps an upload failure onto the unified envelope. A
// business error keeps its code; anything else is an internal failure.
func imageUploadError(err error) (int, string) {
	if ae, ok := apierrors.As(err); ok {
		return int(ae.Code), ae.Message
	}
	if errors.Is(err, context.Canceled) {
		return int(apierrors.CodeImageReferenceInvalid), "image: upload cancelled"
	}
	logger.S().Warnw("image: upload failed", "err", err)
	return int(apierrors.CodeImageReferenceInvalid), err.Error()
}
