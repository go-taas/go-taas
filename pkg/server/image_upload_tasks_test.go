package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// fakeUploadTasks records what the upload handler reports to the history seam.
type fakeUploadTasks struct {
	begun     ImageUploadBeginRequest
	taskID    string
	progress  []int64
	completed string
	archive   string
	failed    string
	listed    []*ImageUploadTaskView
	total     int64
	cancelled string
}

func (f *fakeUploadTasks) BeginImageUpload(_ context.Context, req ImageUploadBeginRequest) (string, error) {
	f.begun = req
	if f.taskID == "" {
		f.taskID = "task-1"
	}
	return f.taskID, nil
}

func (f *fakeUploadTasks) RecordImageUploadProgress(_ context.Context, _ string, received int64) {
	f.progress = append(f.progress, received)
}

func (f *fakeUploadTasks) CompleteImageUpload(_ context.Context, taskID, archivePath string) (*ImageUploadResult, error) {
	f.completed = taskID
	f.archive = archivePath
	return &ImageUploadResult{ImageID: "img-1", Reference: "hub/taas/x:1", Name: "hub/taas/x", Tag: "1"}, nil
}

func (f *fakeUploadTasks) FailImageUpload(_ context.Context, taskID, message string) {
	f.failed = taskID + ": " + message
}

func (f *fakeUploadTasks) ListImageUploadTasks(_ context.Context, _, _ int) ([]*ImageUploadTaskView, int64, error) {
	return f.listed, f.total, nil
}

func (f *fakeUploadTasks) CancelImageUploadTask(_ context.Context, taskID string) error {
	f.cancelled = taskID
	return nil
}

// TestImageUploadHandlerRecordsTask proves the multipart upload opens a
// history row with the form metadata and closes it through the task API, so
// the console lists uploads the way it lists downloads.
func TestImageUploadHandlerRecordsTask(t *testing.T) {
	s := &commonServer{}
	tasks := &fakeUploadTasks{}
	s.SetImageUploadTasks(tasks)
	s.SetImageUpload(func(context.Context, ImageUploadRequest) (*ImageUploadResult, error) {
		t.Fatal("the tracked path must complete through the task API, not the plain seam")
		return nil, nil
	})

	rec := httptest.NewRecorder()
	s.imageUploadHandler(rec, uploadRequest(t, []byte("tar"), map[string]string{
		"name":        "vllm",
		"tag":         "v0.6.3",
		"accelerator": "nvidia",
		"engine":      "vllm",
		"totalBytes":  "1024",
	}))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "vllm", tasks.begun.Name)
	assert.Equal(t, "vllm", tasks.begun.Engine)
	assert.EqualValues(t, 1024, tasks.begun.TotalBytes)
	assert.Equal(t, "task-1", tasks.completed)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "img-1", body["imageId"])
	assert.Equal(t, "task-1", body["taskId"])
}

// TestImageUploadTaskListHandlerShapesPage proves the history endpoint returns
// the rows, the envelope and the pagination the panel reads.
func TestImageUploadTaskListHandlerShapesPage(t *testing.T) {
	s := &commonServer{}
	tasks := &fakeUploadTasks{listed: []*ImageUploadTaskView{{
		TaskID: "task-1", State: "succeeded", Name: "vllm", BytesReceived: 10, TotalBytes: 10,
	}}, total: 1}
	s.SetImageUploadTasks(tasks)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/image-upload-tasks?page.offset=0&page.limit=20", nil)
	s.imageUploadTaskListHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Response map[string]any          `json:"response"`
		Uploads  []*ImageUploadTaskView  `json:"uploads"`
		PageMeta map[string]any          `json:"pageMeta"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Uploads, 1)
	assert.Equal(t, "task-1", body.Uploads[0].TaskID)
	assert.EqualValues(t, 1, body.PageMeta["total"])
	assert.EqualValues(t, 0, body.Response["code"])
}

// TestImageUploadTaskCancelHandler maps the delete verb onto the canceller.
func TestImageUploadTaskCancelHandler(t *testing.T) {
	s := &commonServer{}
	tasks := &fakeUploadTasks{}
	s.SetImageUploadTasks(tasks)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/image-upload-tasks/task-9", nil)
	req = req.WithContext(context.WithValue(req.Context(), uploadRouteParams{}, map[string]string{"id": "task-9"}))
	s.imageUploadTaskCancelHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "task-9", tasks.cancelled)
}

// TestImageUploadTaskListHandlerMapsBusinessError proves a module error keeps
// its code instead of leaking a 500 envelope.
func TestImageUploadTaskListHandlerMapsBusinessError(t *testing.T) {
	s := &commonServer{}
	s.SetImageUploadTasks(errUploadTasks{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/image-upload-tasks", nil)
	s.imageUploadTaskListHandler(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// errUploadTasks fails every call with an input business error.
type errUploadTasks struct{}

func (errUploadTasks) BeginImageUpload(context.Context, ImageUploadBeginRequest) (string, error) {
	return "", apierrors.New(apierrors.CodeImageReferenceInvalid)
}
func (errUploadTasks) RecordImageUploadProgress(context.Context, string, int64) {}
func (errUploadTasks) CompleteImageUpload(context.Context, string, string) (*ImageUploadResult, error) {
	return nil, apierrors.New(apierrors.CodeImageReferenceInvalid)
}
func (errUploadTasks) FailImageUpload(context.Context, string, string) {}
func (errUploadTasks) ListImageUploadTasks(context.Context, int, int) ([]*ImageUploadTaskView, int64, error) {
	return nil, 0, apierrors.New(apierrors.CodeImageReferenceInvalid)
}
func (errUploadTasks) CancelImageUploadTask(context.Context, string) error { return nil }
