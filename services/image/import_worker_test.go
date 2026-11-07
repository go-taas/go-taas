package image

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/registry"
)

func newImportWorkerTest(t *testing.T) (*ImportWorker, *ImportTaskRepository, *Repository, *fakeImporter) {
	t.Helper()
	db := newImageTestDB(t)
	tasks := NewImportTaskRepository(db)
	images := NewRepository(db)
	imp := &fakeImporter{}
	setTestHarbor("hub.example.com", "admin", "secret", "taas")
	return NewImportWorker(tasks, images, imp, 1, time.Millisecond), tasks, images, imp
}

func TestImportWorkerProcessesTask(t *testing.T) {
	worker, tasks, images, imp := newImportWorkerTest(t)
	ctx := context.Background()

	task := &ImportTask{
		SourceReference:    "docker.io/library/vllm:v0.6.3",
		DestinationReference: "hub.example.com/taas/vllm:v0.6.3",
		Accelerator:        "nvidia",
		Engine:             "vllm",
		State:              ImportStatePending,
	}
	require.NoError(t, tasks.Create(ctx, task))

	worker.RunOnce(ctx)

	assert.Equal(t, "docker.io/library/vllm:v0.6.3", imp.srcRef)
	assert.Equal(t, "hub.example.com/taas/vllm:v0.6.3", imp.dstRef)

	found, err := tasks.FindByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, ImportStateSucceeded, found.State)
	assert.NotEmpty(t, found.ImageID)

	// The catalog row was registered.
	img, err := images.FindByID(ctx, found.ImageID)
	require.NoError(t, err)
	assert.Equal(t, "hub.example.com/taas/vllm", img.Name)
	assert.Equal(t, "v0.6.3", img.Tag)
	assert.Equal(t, "nvidia", img.Accelerator)
}

func TestImportWorkerFailure(t *testing.T) {
	worker, tasks, _, imp := newImportWorkerTest(t)
	imp.err = errors.New("manifest unknown")
	ctx := context.Background()

	task := &ImportTask{
		SourceReference:    "docker.io/library/vllm:v0.6.3",
		DestinationReference: "hub.example.com/taas/vllm:v0.6.3",
		Accelerator:        "nvidia",
		Engine:             "vllm",
		State:              ImportStatePending,
	}
	require.NoError(t, tasks.Create(ctx, task))

	worker.RunOnce(ctx)

	found, err := tasks.FindByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, ImportStateFailed, found.State)
	assert.Contains(t, found.FailureReason, "manifest unknown")
}

func TestImportWorkerNoPendingTask(t *testing.T) {
	worker, _, _, imp := newImportWorkerTest(t)
	ctx := context.Background()

	worker.RunOnce(ctx)
	assert.Empty(t, imp.srcRef, "no pending task means no import")
}

func TestImportWorkerUnconfiguredHarbor(t *testing.T) {
	worker, tasks, _, _ := newImportWorkerTest(t)
	setTestHarbor("", "", "", "taas")
	ctx := context.Background()

	task := &ImportTask{
		SourceReference:    "docker.io/library/vllm:v0.6.3",
		DestinationReference: "hub.example.com/taas/vllm:v0.6.3",
		Accelerator:        "nvidia",
		Engine:             "vllm",
		State:              ImportStatePending,
	}
	require.NoError(t, tasks.Create(ctx, task))

	worker.RunOnce(ctx)

	found, err := tasks.FindByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, ImportStateFailed, found.State)
	assert.Contains(t, found.FailureReason, "not configured")
}

func TestImportWorkerRunnerName(t *testing.T) {
	worker, _, _, _ := newImportWorkerTest(t)
	assert.Equal(t, "image-import-worker", worker.RunnerName())
}

// fakeImporterOnly implements the registry importer but not the archive
// one, so the worker can be exercised with a real importer seam.
var _ registry.Importer = (*fakeImporter)(nil)