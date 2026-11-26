// Chunked, resumable model upload.
//
// The legacy model:upload endpoint took a single multipart archive and the
// gateway had to fit the whole body — which is why a multi-GB archive hit
// "413 Request Entity Too Large". This replaces it with a resumable, chunked
// flow. Every endpoint here is a path route (the bodies are binary), and no
// single request carries more than one chunk, so no gateway sees an enormous
// body.
//
//	POST /api/v1/admin/models/uploads                create a session
//	GET  /api/v1/admin/models/uploads                list uploads
//	GET  /api/v1/admin/models/uploads/{id}            session detail
//	GET  /api/v1/admin/models/uploads/{id}/parts      received part numbers
//	PUT  /api/v1/admin/models/uploads/{id}/parts/{n}  upload one chunk
//	POST /api/v1/admin/models/uploads/{id}:complete   finalize + dispatch job
//	POST /api/v1/admin/models/uploads/{id}:interrupt  mark the browser gone
//	DELETE /api/v1/admin/models/uploads/{id}          cancel
//
// The browser slices the file, PUTs each chunk, and can resume an interrupted
// upload by asking which parts already arrived. Completing the upload hands
// the composed archive to a K8s Job that unpacks it into the target backend;
// the server registers the model when that Job reports success.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
)

// ModelUploadSessionView is the JSON projection of one upload session.
type ModelUploadSessionView struct {
	UploadID       string `json:"uploadId"`
	Name           string `json:"name"`
	HubModelID     string `json:"hubModelId"`
	Version        string `json:"version"`
	Description    string `json:"description"`
	StorageBackend string `json:"storageBackend"`
	ArchiveName    string `json:"archiveName"`
	TotalBytes     int64  `json:"totalBytes"`
	PartSizeBytes  int64  `json:"partSizeBytes"`
	State          string `json:"state"`
	BytesReceived  int64  `json:"bytesReceived"`
	ModelID        string `json:"modelId,omitempty"`
	Message        string `json:"message,omitempty"`
	// created_at/updated_at are Unix seconds, like every other console
	// timestamp: the web formatTime() reads them as seconds.
	CreatedAt int64 `json:"createdAt"`
	UpdatedAt int64 `json:"updatedAt"`
}

// ModelUploadCreateRequest is the create-session body.
type ModelUploadCreateRequest struct {
	Name           string `json:"name"`
	HubModelID     string `json:"hubModelId"`
	Description    string `json:"description"`
	StorageBackend string `json:"storageBackend"`
	ArchiveName    string `json:"archiveName"`
	TotalBytes     int64  `json:"totalBytes"`
}

// ModelUploadSessionAPI is implemented by the model module. The handlers here
// are thin adapters; session and progress logic lives in the model service.
type ModelUploadSessionAPI interface {
	CreateUploadSession(ctx context.Context, req ModelUploadCreateRequest) (*ModelUploadSessionView, error)
	GetUploadSession(ctx context.Context, uploadID string) (*ModelUploadSessionView, error)
	ListUploadSessions(ctx context.Context, limit int) ([]*ModelUploadSessionView, error)
	ListUploadedParts(ctx context.Context, uploadID string) ([]int, error)
	UploadPart(ctx context.Context, uploadID string, partNumber int, body io.Reader, size int64) error
	CompleteUploadSession(ctx context.Context, uploadID string) (*ModelUploadSessionView, error)
	// MarkUploadInterrupted records that the browser driving the upload is
	// gone, so the session can be resumed instead of looking stuck.
	MarkUploadInterrupted(ctx context.Context, uploadID string) error
	CancelUploadSession(ctx context.Context, uploadID string) error
}

// SetModelUploadSession installs the chunked-upload API. It must be called
// before Init; nil leaves the routes unregistered.
func (s *commonServer) SetModelUploadSession(api ModelUploadSessionAPI) {
	s.modelUploadSession = api
}
func (s *commonServer) registerModelUploadSessionRoutes() {
	if s.gatewayMux == nil || s.modelUploadSession == nil {
		return
	}
	routes := []struct {
		method  string
		path    string
		handler func(w http.ResponseWriter, r *http.Request)
	}{
		{http.MethodPost, "/api/v1/admin/models/uploads", s.uploadSessionCreateHandler},
		{http.MethodGet, "/api/v1/admin/models/uploads", s.uploadSessionListHandler},
		{http.MethodGet, "/api/v1/admin/models/uploads/{id}", s.uploadSessionGetHandler},
		{http.MethodGet, "/api/v1/admin/models/uploads/{id}/parts", s.uploadSessionPartsHandler},
		{http.MethodPut, "/api/v1/admin/models/uploads/{id}/parts/{n}", s.uploadSessionPartHandler},
		{http.MethodPost, "/api/v1/admin/models/uploads/{id}:complete", s.uploadSessionCompleteHandler},
		{http.MethodPost, "/api/v1/admin/models/uploads/{id}:interrupt", s.uploadSessionInterruptHandler},
		{http.MethodDelete, "/api/v1/admin/models/uploads/{id}", s.uploadSessionCancelHandler},
	}
	for _, r := range routes {
		path, handler := r.path, r.handler
		if err := s.gatewayMux.HandlePath(r.method, path,
			func(w http.ResponseWriter, req *http.Request, params map[string]string) {
				ctx := uploadContext(req)
				ctx = context.WithValue(ctx, uploadRouteParams{}, params)
				handler(w, req.Clone(ctx))
			}); err != nil {
			logger.S().Fatalf("gateway register %s failed: %v", path, err)
		}
	}
}

// uploadRouteParams carries path variables through the request context.
type uploadRouteParams struct{}

func param(r *http.Request, key string) string {
	v, _ := r.Context().Value(uploadRouteParams{}).(map[string]string)
	if v != nil {
		return v[key]
	}
	return ""
}

func (s *commonServer) uploadSessionCreateHandler(w http.ResponseWriter, r *http.Request) {
	if s.modelUploadSession == nil {
		writeBusinessError(w, int(apierrors.CodeModelPathInvalid), "model: upload is not configured")
		return
	}
	var req ModelUploadCreateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeBusinessError(w, int(apierrors.CodeModelPathInvalid), "model: invalid upload request")
		return
	}
	view, err := s.modelUploadSession.CreateUploadSession(r.Context(), req)
	if err != nil {
		code, message := modelUploadError(err)
		writeBusinessError(w, code, message)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *commonServer) uploadSessionGetHandler(w http.ResponseWriter, r *http.Request) {
	id := param(r, "id")
	view, err := s.modelUploadSession.GetUploadSession(r.Context(), id)
	if err != nil {
		code, message := modelUploadError(err)
		writeBusinessError(w, code, message)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *commonServer) uploadSessionListHandler(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	views, err := s.modelUploadSession.ListUploadSessions(r.Context(), limit)
	if err != nil {
		code, message := modelUploadError(err)
		writeBusinessError(w, code, message)
		return
	}
	if views == nil {
		views = []*ModelUploadSessionView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploads": views})
}

func (s *commonServer) uploadSessionPartsHandler(w http.ResponseWriter, r *http.Request) {
	id := param(r, "id")
	parts, err := s.modelUploadSession.ListUploadedParts(r.Context(), id)
	if err != nil {
		code, message := modelUploadError(err)
		writeBusinessError(w, code, message)
		return
	}
	if parts == nil {
		parts = []int{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"parts": parts})
}

func (s *commonServer) uploadSessionPartHandler(w http.ResponseWriter, r *http.Request) {
	id := param(r, "id")
	n, err := strconv.Atoi(param(r, "n"))
	if err != nil || n < 1 {
		writeBusinessError(w, int(apierrors.CodeModelPathInvalid), "model: invalid part number")
		return
	}
	if err := s.modelUploadSession.UploadPart(r.Context(), id, n, r.Body, r.ContentLength); err != nil {
		code, message := modelUploadError(err)
		writeBusinessError(w, code, message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"partNumber": n, "ok": true})
}

func (s *commonServer) uploadSessionCompleteHandler(w http.ResponseWriter, r *http.Request) {
	id := param(r, "id")
	view, err := s.modelUploadSession.CompleteUploadSession(r.Context(), id)
	if err != nil {
		code, message := modelUploadError(err)
		writeBusinessError(w, code, message)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *commonServer) uploadSessionInterruptHandler(w http.ResponseWriter, r *http.Request) {
	if err := s.modelUploadSession.MarkUploadInterrupted(r.Context(), param(r, "id")); err != nil {
		code, message := modelUploadError(err)
		writeBusinessError(w, code, message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"interrupted": true})
}

func (s *commonServer) uploadSessionCancelHandler(w http.ResponseWriter, r *http.Request) {
	id := param(r, "id")
	if err := s.modelUploadSession.CancelUploadSession(r.Context(), id); err != nil {
		code, message := modelUploadError(err)
		writeBusinessError(w, code, message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
}

// modelUploadError maps an upload failure onto the unified envelope.
func modelUploadError(err error) (int, string) {
	if err == nil {
		return int(apierrors.CodeModelPathInvalid), ""
	}
	if ae, ok := apierrors.As(err); ok {
		return int(ae.Code), ae.Message
	}
	if errors.Is(err, context.Canceled) {
		return int(apierrors.CodeModelPathInvalid), "model: upload cancelled"
	}
	logger.S().Warnw("model: upload failed", "err", err)
	return int(apierrors.CodeModelPathInvalid), strings.TrimSpace(err.Error())
}
