package image

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func newImportTestRepo(t *testing.T) *ImportTaskRepository {
	t.Helper()
	return NewImportTaskRepository(newImageTestDB(t))
}

func TestImportTaskRepositoryCreateAndFind(t *testing.T) {
	repo := newImportTestRepo(t)
	ctx := context.Background()

	task := &ImportTask{
		SourceReference:    "docker.io/library/vllm:v0.6.3",
		DestinationReference: "hub.example.com/taas/vllm:v0.6.3",
		Accelerator:        "nvidia",
		Engine:             "vllm",
		State:              ImportStatePending,
	}
	require.NoError(t, repo.Create(ctx, task))
	assert.NotEmpty(t, task.ID)

	found, err := repo.FindByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, ImportStatePending, found.State)
	assert.Equal(t, "docker.io/library/vllm:v0.6.3", found.SourceReference)

	// Missing task maps to 10201 (image not found is reused for tasks).
	_, err = repo.FindByID(ctx, "00000000-0000-0000-0000-000000000000")
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageNotFound, ae.Code)
}

func TestImportTaskRepositoryNextPendingClaimsAtomically(t *testing.T) {
	repo := newImportTestRepo(t)
	ctx := context.Background()

	first := &ImportTask{SourceReference: "a:1", DestinationReference: "h/p/a:1", Accelerator: "nvidia", Engine: "vllm", State: ImportStatePending}
	second := &ImportTask{SourceReference: "b:1", DestinationReference: "h/p/b:1", Accelerator: "nvidia", Engine: "vllm", State: ImportStatePending}
	require.NoError(t, repo.Create(ctx, first))
	require.NoError(t, repo.Create(ctx, second))

	// The oldest pending task is claimed and marked running.
	got, err := repo.NextPending(ctx)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, first.ID, got.ID)
	claimed, err := repo.FindByID(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, ImportStateRunning, claimed.State)

	// A second claim returns the next pending task, not the running one.
	got2, err := repo.NextPending(ctx)
	require.NoError(t, err)
	require.NotNil(t, got2)
	assert.Equal(t, second.ID, got2.ID)

	// No pending tasks remain.
	got3, err := repo.NextPending(ctx)
	require.NoError(t, err)
	assert.Nil(t, got3)
}

func TestImportTaskRepositoryApplySuccessAndFailure(t *testing.T) {
	repo := newImportTestRepo(t)
	ctx := context.Background()

	task := &ImportTask{SourceReference: "a:1", DestinationReference: "h/p/a:1", Accelerator: "nvidia", Engine: "vllm", State: ImportStatePending, SourcePassword: "secret"}
	require.NoError(t, repo.Create(ctx, task))

	require.NoError(t, repo.ApplySuccess(ctx, task.ID, "h/p/a:1", "img-1"))
	found, err := repo.FindByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, ImportStateSucceeded, found.State)
	assert.Equal(t, "img-1", found.ImageID)
	assert.Empty(t, found.SourcePassword, "the source password is cleared on success")

	// A failed task records the reason and clears the password.
	task2 := &ImportTask{SourceReference: "b:1", DestinationReference: "h/p/b:1", Accelerator: "nvidia", Engine: "vllm", State: ImportStatePending, SourcePassword: "secret"}
	require.NoError(t, repo.Create(ctx, task2))
	require.NoError(t, repo.ApplyFailure(ctx, task2.ID, "manifest unknown"))
	found2, err := repo.FindByID(ctx, task2.ID)
	require.NoError(t, err)
	assert.Equal(t, ImportStateFailed, found2.State)
	assert.Equal(t, "manifest unknown", found2.FailureReason)
	assert.Empty(t, found2.SourcePassword)
}

func TestImportTaskRepositoryHasActiveByDestination(t *testing.T) {
	repo := newImportTestRepo(t)
	ctx := context.Background()

	task := &ImportTask{SourceReference: "a:1", DestinationReference: "h/p/a:1", Accelerator: "nvidia", Engine: "vllm", State: ImportStatePending}
	require.NoError(t, repo.Create(ctx, task))

	active, err := repo.HasActiveByDestination(ctx, "h/p/a:1")
	require.NoError(t, err)
	assert.True(t, active)

	require.NoError(t, repo.ApplySuccess(ctx, task.ID, "h/p/a:1", "img-1"))
	active, err = repo.HasActiveByDestination(ctx, "h/p/a:1")
	require.NoError(t, err)
	assert.False(t, active)
}

func TestImportTaskRepositoryRecoverInterrupted(t *testing.T) {
	repo := newImportTestRepo(t)
	ctx := context.Background()

	pending := &ImportTask{SourceReference: "a:1", DestinationReference: "h/p/a:1", Accelerator: "nvidia", Engine: "vllm", State: ImportStatePending}
	running := &ImportTask{SourceReference: "b:1", DestinationReference: "h/p/b:1", Accelerator: "nvidia", Engine: "vllm", State: ImportStateRunning}
	done := &ImportTask{SourceReference: "c:1", DestinationReference: "h/p/c:1", Accelerator: "nvidia", Engine: "vllm", State: ImportStateSucceeded}
	require.NoError(t, repo.Create(ctx, pending))
	require.NoError(t, repo.Create(ctx, running))
	require.NoError(t, repo.Create(ctx, done))

	n, err := repo.RecoverInterruptedTasks(ctx, "server restart")
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	found, err := repo.FindByID(ctx, pending.ID)
	require.NoError(t, err)
	assert.Equal(t, ImportStateFailed, found.State)
	assert.Equal(t, "server restart", found.FailureReason)

	// The succeeded task is untouched.
	foundDone, err := repo.FindByID(ctx, done.ID)
	require.NoError(t, err)
	assert.Equal(t, ImportStateSucceeded, foundDone.State)
}

func TestImportTaskRepositoryList(t *testing.T) {
	repo := newImportTestRepo(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		require.NoError(t, repo.Create(ctx, &ImportTask{
			SourceReference:    "a:1",
			DestinationReference: "h/p/a:1",
			Accelerator:        "nvidia",
			Engine:             "vllm",
			State:              ImportStatePending,
		}))
	}
	rows, total, err := repo.List(ctx, 0, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Len(t, rows, 2)
}