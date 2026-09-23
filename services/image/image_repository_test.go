package image

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func newImageTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Image{}, &WarmupTask{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func newImageTestRepo(t *testing.T) *Repository {
	t.Helper()
	return NewRepository(newImageTestDB(t))
}

func newWarmupTestRepo(t *testing.T) *WarmupTaskRepository {
	t.Helper()
	return NewWarmupTaskRepository(newImageTestDB(t))
}

func TestImageRepositoryCreateAndFind(t *testing.T) {
	repo := newImageTestRepo(t)
	ctx := context.Background()

	img := &Image{Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm", Description: "vLLM engine"}
	require.NoError(t, repo.CreateImage(ctx, img))
	assert.NotEmpty(t, img.ID)

	found, err := repo.FindByID(ctx, img.ID)
	require.NoError(t, err)
	assert.Equal(t, "vllm", found.Engine)
	assert.Equal(t, "vLLM engine", found.Description)

	// Missing image maps to 10201.
	_, err = repo.FindByID(ctx, "img-missing")
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageNotFound, ae.Code)
}

func TestImageRepositoryCreateDuplicateTriple(t *testing.T) {
	repo := newImageTestRepo(t)
	ctx := context.Background()

	first := &Image{Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm"}
	require.NoError(t, repo.CreateImage(ctx, first))

	// Same (name, tag, accelerator) triple maps to 10202.
	dup := &Image{Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm"}
	err := repo.CreateImage(ctx, dup)
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageExists, ae.Code)

	// Same name+tag on a different accelerator is allowed.
	other := &Image{Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "iluvatar", Engine: "vllm"}
	require.NoError(t, repo.CreateImage(ctx, other))
}

func TestImageRepositoryFindByTriple(t *testing.T) {
	repo := newImageTestRepo(t)
	ctx := context.Background()

	img := &Image{Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm"}
	require.NoError(t, repo.CreateImage(ctx, img))

	found, err := repo.FindByTriple(ctx, "ghcr.io/go-taas/vllm", "v0.6.3", "nvidia")
	require.NoError(t, err)
	assert.Equal(t, img.ID, found.ID)

	_, err = repo.FindByTriple(ctx, "ghcr.io/go-taas/vllm", "v0.6.3", "metax")
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageNotFound, ae.Code)
}

func TestImageRepositoryListFiltersAndPagination(t *testing.T) {
	repo := newImageTestRepo(t)
	ctx := context.Background()

	seed := []*Image{
		{Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm"},
		{Name: "ghcr.io/go-taas/vllm-iluvatar", Tag: "v0.6.3", Accelerator: "iluvatar", Engine: "vllm"},
		{Name: "ghcr.io/go-taas/sglang", Tag: "v0.1.4", Accelerator: "metax", Engine: "sglang"},
	}
	for _, img := range seed {
		require.NoError(t, repo.CreateImage(ctx, img))
	}

	rows, total, err := repo.ListImages(ctx, "", "", 0, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Len(t, rows, 3)

	rows, total, err = repo.ListImages(ctx, "nvidia", "", 0, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	assert.Equal(t, "ghcr.io/go-taas/vllm", rows[0].Name)

	rows, _, err = repo.ListImages(ctx, "", "vllm", 0, 100)
	require.NoError(t, err)
	assert.Len(t, rows, 2)

	rows, _, err = repo.ListImages(ctx, "nvidia", "sglang", 0, 100)
	require.NoError(t, err)
	assert.Empty(t, rows)

	// Pagination: offset skips the newest row.
	rows, _, err = repo.ListImages(ctx, "", "", 1, 2)
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

func TestImageRepositoryUpdateDescriptionAndDelete(t *testing.T) {
	repo := newImageTestRepo(t)
	ctx := context.Background()

	img := &Image{Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm"}
	require.NoError(t, repo.CreateImage(ctx, img))

	require.NoError(t, repo.UpdateDescription(ctx, img.ID, "updated"))
	found, err := repo.FindByID(ctx, img.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated", found.Description)

	require.NoError(t, repo.Delete(ctx, img.ID))
	_, err = repo.FindByID(ctx, img.ID)
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageNotFound, ae.Code)
}

func TestImageRepositorySeedFromConfig(t *testing.T) {
	repo := newImageTestRepo(t)
	ctx := context.Background()

	entries := []config.ImageRegistryEntry{
		{ImageID: "img-vllm-nvidia-v063", Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm"},
		{ImageID: "img-sglang-metax-v014", Name: "ghcr.io/go-taas/sglang", Tag: "v0.1.4", Accelerator: "metax", Engine: "sglang"},
	}
	require.NoError(t, repo.SeedFromConfig(ctx, entries))

	count, err := repo.CountAll(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)

	// Seed preserves the config imageId values.
	found, err := repo.FindByID(ctx, "img-vllm-nvidia-v063")
	require.NoError(t, err)
	assert.Equal(t, "ghcr.io/go-taas/vllm", found.Name)

	// Re-seeding is insert-only: existing rows are untouched, new rows
	// are added.
	entries = append(entries, config.ImageRegistryEntry{
		ImageID: "img-vllm-iluvatar-v063", Name: "ghcr.io/go-taas/vllm-iluvatar", Tag: "v0.6.3", Accelerator: "iluvatar", Engine: "vllm",
	})
	require.NoError(t, repo.SeedFromConfig(ctx, entries))
	count, err = repo.CountAll(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestWarmupTaskRepositoryCreateAndFind(t *testing.T) {
	repo := newWarmupTestRepo(t)
	ctx := context.Background()

	task := &WarmupTask{ImageID: "img-1", State: TaskStatePending, NodeSelector: []byte(`{}`)}
	require.NoError(t, repo.Create(ctx, task))
	assert.NotEmpty(t, task.ID)

	found, err := repo.FindByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, TaskStatePending, found.State)
	assert.Equal(t, "img-1", found.ImageID)

	// Missing task maps to 10201 (image not found is reused for tasks
	// in the API surface; the repository surfaces the same code).
	_, err = repo.FindByID(ctx, "00000000-0000-0000-0000-000000000000")
	require.Error(t, err)
}

func TestWarmupTaskRepositoryActiveGate(t *testing.T) {
	repo := newWarmupTestRepo(t)
	ctx := context.Background()

	task := &WarmupTask{ImageID: "img-1", State: TaskStatePending, NodeSelector: []byte(`{}`)}
	require.NoError(t, repo.Create(ctx, task))

	active, err := repo.HasActiveByImageID(ctx, "img-1")
	require.NoError(t, err)
	assert.True(t, active)

	require.NoError(t, repo.ApplyStatus(ctx, task.ID, TaskStateSucceeded, nil, nil))

	active, err = repo.HasActiveByImageID(ctx, "img-1")
	require.NoError(t, err)
	assert.False(t, active)
}

func TestWarmupTaskRepositoryApplyStatusTerminalWins(t *testing.T) {
	repo := newWarmupTestRepo(t)
	ctx := context.Background()

	task := &WarmupTask{ImageID: "img-1", State: TaskStatePending, NodeSelector: []byte(`{}`)}
	require.NoError(t, repo.Create(ctx, task))

	require.NoError(t, repo.ApplyStatus(ctx, task.ID, TaskStateRunning, nil, nil))
	require.NoError(t, repo.ApplyStatus(ctx, task.ID, TaskStateSucceeded, nil, nil))

	// A late running report after the terminal state is rejected.
	err := repo.ApplyStatus(ctx, task.ID, TaskStateRunning, nil, nil)
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageNotFound, ae.Code)

	found, err := repo.FindByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, TaskStateSucceeded, found.State)
}

func TestWarmupTaskRepositoryApplyStatusUnknownTask(t *testing.T) {
	repo := newWarmupTestRepo(t)
	ctx := context.Background()

	err := repo.ApplyStatus(ctx, "00000000-0000-0000-0000-000000000000", TaskStateRunning, nil, nil)
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageNotFound, ae.Code)
}

func TestWarmupTaskRepositoryListByImageID(t *testing.T) {
	repo := newWarmupTestRepo(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		require.NoError(t, repo.Create(ctx, &WarmupTask{ImageID: "img-1", State: TaskStateSucceeded, NodeSelector: []byte(`{}`)}))
	}
	require.NoError(t, repo.Create(ctx, &WarmupTask{ImageID: "img-2", State: TaskStatePending, NodeSelector: []byte(`{}`)}))

	rows, total, err := repo.ListByImageID(ctx, "img-1", 0, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Len(t, rows, 3)

	// Newest first: the first row is the newest created.
	first, err := repo.FindByID(ctx, rows[0].ID)
	require.NoError(t, err)
	assert.Equal(t, rows[0].CreatedAt.Unix(), first.CreatedAt.Unix())
}

func TestWarmupTaskRepositoryLatestByImageIDs(t *testing.T) {
	repo := newWarmupTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &WarmupTask{ImageID: "img-1", State: TaskStateSucceeded, NodeSelector: []byte(`{}`)}))
	require.NoError(t, repo.Create(ctx, &WarmupTask{ImageID: "img-1", State: TaskStateFailed, NodeSelector: []byte(`{}`)}))
	require.NoError(t, repo.Create(ctx, &WarmupTask{ImageID: "img-2", State: TaskStateSucceeded, NodeSelector: []byte(`{}`)}))

	latest, err := repo.LatestByImageIDs(ctx, []string{"img-1", "img-2", "img-3"})
	require.NoError(t, err)
	require.Contains(t, latest, "img-1")
	require.Contains(t, latest, "img-2")
	assert.NotContains(t, latest, "img-3")
	assert.Equal(t, TaskStateFailed, latest["img-1"].State)
}

func TestWarmupTaskRepositoryMarkFailed(t *testing.T) {
	repo := newWarmupTestRepo(t)
	ctx := context.Background()

	task := &WarmupTask{ImageID: "img-1", State: TaskStatePending, NodeSelector: []byte(`{}`)}
	require.NoError(t, repo.Create(ctx, task))

	require.NoError(t, repo.MarkFailed(ctx, task.ID, "publish failed: connection lost"))
	found, err := repo.FindByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, TaskStateFailed, found.State)
	require.NotNil(t, found.FailureReason)
	assert.Contains(t, *found.FailureReason, "publish failed")
}
