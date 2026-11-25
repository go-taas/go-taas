package image

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// TestUploadTaskLifecycle proves an upload's history row walks the states the
// console renders: uploading with a byte count, then a terminal outcome.
func TestUploadTaskLifecycle(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()

	id, err := svc.BeginUploadTask(ctx, &UploadTaskBegin{
		ArchiveName: "engine.tar.gz",
		Name:        "vllm",
		Tag:         "v0.6.3",
		Accelerator: "nvidia",
		Engine:      "vllm",
		TotalBytes:  1024,
		Actor:       "admin",
	})
	require.NoError(t, err)
	require.NotEmpty(t, id)

	repo, err := svc.uploadTaskRepository()
	require.NoError(t, err)
	row, err := repo.FindByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, UploadStateUploading, row.State)
	assert.EqualValues(t, 1024, row.TotalBytes)
	assert.Equal(t, "engine.tar.gz", row.ArchiveName)
	assert.Equal(t, "admin", row.Actor)

	// Progress is recorded while the archive streams in.
	svc.RecordUploadProgress(ctx, id, 512)
	row, err = repo.FindByID(ctx, id)
	require.NoError(t, err)
	assert.EqualValues(t, 512, row.BytesReceived)

	// A finished row reports the full byte count so the console stops at 100%.
	require.NoError(t, repo.MarkProcessing(ctx, id))
	require.NoError(t, repo.ApplySuccess(ctx, id, "img-1", "hub.example.com/taas/vllm:v0.6.3"))
	row, err = repo.FindByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, UploadStateSucceeded, row.State)
	assert.EqualValues(t, 1024, row.BytesReceived)
	assert.Equal(t, "img-1", row.ImageID)
	assert.Equal(t, "hub.example.com/taas/vllm:v0.6.3", row.Reference)
}

// TestUploadTaskFailureKeepsReason proves a failed upload records why, so the
// history table can show the server's message instead of a bare "failed".
func TestUploadTaskFailureKeepsReason(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()

	id, err := svc.BeginUploadTask(ctx, &UploadTaskBegin{Name: "vllm", Engine: "vllm", TotalBytes: 10})
	require.NoError(t, err)
	svc.FailUploadTask(ctx, id, "image: could not store the upload")

	repo, err := svc.uploadTaskRepository()
	require.NoError(t, err)
	row, err := repo.FindByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, UploadStateFailed, row.State)
	assert.Equal(t, "image: could not store the upload", row.Message)
}

// TestUploadTaskCancelRefusesCompletion proves a cancelled upload never hands
// its staged archive to the push: the operator stopped it deliberately.
func TestUploadTaskCancelRefusesCompletion(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()

	id, err := svc.BeginUploadTask(ctx, &UploadTaskBegin{Name: "vllm", Engine: "vllm", TotalBytes: 10})
	require.NoError(t, err)
	require.NoError(t, svc.CancelUploadTask(ctx, id))

	_, err = svc.CompleteUploadTask(ctx, id, "/tmp/whatever.tar")
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageReferenceInvalid, ae.Code)

	repo, err := svc.uploadTaskRepository()
	require.NoError(t, err)
	row, err := repo.FindByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, UploadStateCancelled, row.State)
}

// TestUploadTaskListNewestFirst proves the history table reads newest first
// with a total, so the panel can paginate like the model lists.
func TestUploadTaskListNewestFirst(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()

	first, err := svc.BeginUploadTask(ctx, &UploadTaskBegin{Name: "old", Engine: "vllm"})
	require.NoError(t, err)
	require.NoError(t, svc.CancelUploadTask(ctx, first))
	// GORM keeps CreatedAt at insert time; separate the rows so the ordering
	// is deterministic even within one second.
	time.Sleep(5 * time.Millisecond)
	second, err := svc.BeginUploadTask(ctx, &UploadTaskBegin{Name: "new", Engine: "vllm"})
	require.NoError(t, err)

	views, total, err := svc.ListUploadTasks(ctx, 0, 20)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	require.Len(t, views, 2)
	assert.Equal(t, second, views[0].TaskID)
	assert.Equal(t, first, views[1].TaskID)
	assert.Equal(t, UploadStateUploading, views[0].State)
	assert.Equal(t, UploadStateCancelled, views[1].State)
}
