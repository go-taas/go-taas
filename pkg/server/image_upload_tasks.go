// Image upload history: the task rows a console lists under the upload panel,
// the way the model pages list their download and upload jobs.
//
// An archive upload is one long multipart request, so the server records the
// row before the bytes arrive, updates it while they stream, and closes it
// after the push. That is what turns "a progress bar inside the form" into
// "a row in the history table", matching how model uploads report progress.

package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
)

// ImageUploadTaskView is one row of the upload history.
type ImageUploadTaskView struct {
	TaskID        string `json:"taskId"`
	ArchiveName   string `json:"archiveName"`
	Name          string `json:"name"`
	Tag           string `json:"tag"`
	Accelerator   string `json:"accelerator"`
	Engine        string `json:"engine"`
	State         string `json:"state"`
	BytesReceived int64  `json:"bytesReceived"`
	TotalBytes    int64  `json:"totalBytes"`
	ImageID       string `json:"imageId"`
	Reference     string `json:"reference"`
	Message       string `json:"message"`
	Actor         string `json:"actor"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
}

// ImageUploadBeginRequest is the metadata a history row is created with.
type ImageUploadBeginRequest struct {
	ArchiveName     string
	Name            string
	Tag             string
	SourceReference string
	Accelerator     string
	Engine          string
	Description     string
	TotalBytes      int64
	Actor           string
}

// ImageUploadTaskAPI is implemented by the image module: it persists the
// upload history and records progress while the archive streams in.
type ImageUploadTaskAPI interface {
	// BeginImageUpload opens a history row for an upload about to start.
	BeginImageUpload(ctx context.Context, req ImageUploadBeginRequest) (string, error)
	// RecordImageUploadProgress stores the byte count landed so far.
	RecordImageUploadProgress(ctx context.Context, taskID string, received int64)
	// CompleteImageUpload pushes the staged archive and records the outcome.
	CompleteImageUpload(ctx context.Context, taskID, archivePath string) (*ImageUploadResult, error)
	// FailImageUpload records an upload that aborted before the push.
	FailImageUpload(ctx context.Context, taskID, message string)
	// ListImageUploadTasks returns one page of the history, newest first.
	ListImageUploadTasks(ctx context.Context, offset, limit int) ([]*ImageUploadTaskView, int64, error)
	// CancelImageUploadTask stops a running upload.
	CancelImageUploadTask(ctx context.Context, taskID string) error
}

// SetImageUploadTasks installs the upload history API. It must be called
// before Init; nil leaves the history routes unregistered and the upload
// endpoint keeps working without a task row.
func (s *commonServer) SetImageUploadTasks(api ImageUploadTaskAPI) {
	s.imageUploadTasks = api
}

// registerImageUploadTaskRoutes mounts the upload history endpoints. They are
// mounted on the gateway mux like the model upload session routes, so they
// share the admin prefix's authentication.
func (s *commonServer) registerImageUploadTaskRoutes() {
	if s.gatewayMux == nil || s.imageUploadTasks == nil {
		return
	}
	routes := []struct {
		method  string
		path    string
		handler func(w http.ResponseWriter, r *http.Request)
	}{
		{http.MethodGet, "/api/v1/admin/image-upload-tasks", s.imageUploadTaskListHandler},
		{http.MethodDelete, "/api/v1/admin/image-upload-tasks/{id}", s.imageUploadTaskCancelHandler},
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

// imageUploadTaskListHandler answers one page of the upload history.
func (s *commonServer) imageUploadTaskListHandler(w http.ResponseWriter, r *http.Request) {
	offset := 0
	if v := r.URL.Query().Get("page.offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}
	limit := 20
	if v := r.URL.Query().Get("page.limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	views, total, err := s.imageUploadTasks.ListImageUploadTasks(r.Context(), offset, limit)
	if err != nil {
		code, message := imageUploadError(err)
		writeBusinessError(w, code, message)
		return
	}
	if views == nil {
		views = []*ImageUploadTaskView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"response": map[string]any{"code": 0, "message": "OK"},
		"uploads":  views,
		"pageMeta": map[string]any{"offset": offset, "limit": limit, "total": total},
	})
}

// imageUploadTaskCancelHandler stops a running upload.
func (s *commonServer) imageUploadTaskCancelHandler(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(param(r, "id"))
	if id == "" {
		writeBusinessError(w, int(apierrors.CodeImageReferenceInvalid), "image: invalid upload task id")
		return
	}
	if err := s.imageUploadTasks.CancelImageUploadTask(r.Context(), id); err != nil {
		code, message := imageUploadError(err)
		writeBusinessError(w, code, message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
}
