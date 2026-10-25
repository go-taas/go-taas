package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func newModelTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// A busy timeout keeps the background download-job writers (which run
	// on their own goroutine) from colliding with the test's reads on the
	// shared in-memory database.
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_busy_timeout=5000", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Model{}, &Version{}, &Authorization{}))
	// The version-history deployment count joins inference_services
	// (feature #32, AD4). The model module owns no infer schema, so the
	// test creates a minimal inference_services table for the join.
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS inference_services (
		id TEXT PRIMARY KEY,
		organization_id TEXT,
		name TEXT,
		model_id TEXT,
		model_version TEXT,
		image_id TEXT,
		replicas INTEGER,
		accelerator TEXT,
		accelerator_type TEXT,
		state TEXT,
		failure_reason TEXT,
		endpoints TEXT,
		autoscaling TEXT,
		autoscaling_state TEXT,
		autoscaling_current_replicas INTEGER,
		autoscaling_desired_replicas INTEGER,
		autoscaling_current_concurrency INTEGER,
		autoscaling_target_concurrency INTEGER,
		autoscaling_last_scaling_event_at DATETIME,
		autoscaling_error_reason TEXT,
		created_at DATETIME,
		updated_at DATETIME
	)`).Error)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func newModelTestRepo(t *testing.T) *Repository {
	t.Helper()
	return NewRepository(newModelTestDB(t))
}

func TestRepositoryCreateAndFindByName(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()

	m := &Model{Name: "qwen-3b", Description: "Qwen 3B"}
	require.NoError(t, repo.CreateModel(ctx, m))
	assert.NotEmpty(t, m.ID)

	found, err := repo.FindByName(ctx, "qwen-3b")
	require.NoError(t, err)
	assert.Equal(t, m.ID, found.ID)
	assert.Equal(t, "Qwen 3B", found.Description)

	// Missing model maps to 10101.
	_, err = repo.FindByName(ctx, "missing")
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelNotFound, ae.Code)
}

func TestRepositoryCreateModelDuplicate(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.CreateModel(ctx, &Model{Name: "qwen-3b"}))
	err := repo.CreateModel(ctx, &Model{Name: "qwen-3b"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelExists, ae.Code)
}

func TestRepositoryListModels(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i, name := range []string{"alpha", "beta", "gamma"} {
		require.NoError(t, repo.CreateModel(ctx, &Model{
			Name:      name,
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
		}))
	}

	rows, total, err := repo.ListModels(ctx, 0, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	require.Len(t, rows, 3)
	// Newest first.
	assert.Equal(t, "gamma", rows[0].Name)
	assert.Equal(t, "beta", rows[1].Name)
	assert.Equal(t, "alpha", rows[2].Name)

	// Pagination.
	rows, total, err = repo.ListModels(ctx, 1, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	require.Len(t, rows, 2)
	assert.Equal(t, "beta", rows[0].Name)
}

func TestRepositoryVersions(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	m := &Model{Name: "qwen-3b", CreatedAt: base}
	require.NoError(t, repo.CreateModel(ctx, m))

	v1 := &Version{ModelID: m.ID, Version: "v1", WeightPath: "qwen/v1", CreatedAt: base}
	v2 := &Version{ModelID: m.ID, Version: "v2", WeightPath: "qwen/v2", CreatedAt: base.Add(time.Hour)}
	require.NoError(t, repo.CreateVersion(ctx, v1))
	require.NoError(t, repo.CreateVersion(ctx, v2))

	// Duplicate (model, version) maps to 10102.
	err := repo.CreateVersion(ctx, &Version{ModelID: m.ID, Version: "v1"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelExists, ae.Code)

	// Ordered newest first.
	versions, err := repo.ListVersionsByModel(ctx, m.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.Equal(t, "v2", versions[0].Version)
	assert.Equal(t, "v1", versions[1].Version)

	// FindVersion.
	found, err := repo.FindVersion(ctx, m.ID, "v1")
	require.NoError(t, err)
	assert.Equal(t, "qwen/v1", found.WeightPath)

	_, err = repo.FindVersion(ctx, m.ID, "v9")
	require.Error(t, err)
	ae, _ = apierrors.As(err)
	assert.Equal(t, apierrors.CodeModelVersionNotFound, ae.Code)

	// LatestVersion.
	latest, err := repo.LatestVersion(ctx, m.ID)
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Equal(t, "v2", latest.Version)
}

func TestRepositoryActiveVersionAndActivate(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	m := &Model{Name: "qwen-3b", CreatedAt: base}
	require.NoError(t, repo.CreateModel(ctx, m))
	require.NoError(t, repo.CreateVersion(ctx, &Version{ModelID: m.ID, Version: "v1", WeightPath: "qwen/v1", CreatedAt: base}))
	require.NoError(t, repo.CreateVersion(ctx, &Version{ModelID: m.ID, Version: "v2", WeightPath: "qwen/v2", CreatedAt: base.Add(time.Hour)}))

	// No active version initially.
	active, err := repo.ActiveVersion(ctx, m.ID)
	require.NoError(t, err)
	assert.Nil(t, active)

	// Activate v1.
	activeVersion, err := repo.ActivateVersion(ctx, m.ID, "v1")
	require.NoError(t, err)
	assert.Equal(t, "v1", activeVersion)

	active, err = repo.ActiveVersion(ctx, m.ID)
	require.NoError(t, err)
	require.NotNil(t, active)
	assert.Equal(t, "v1", active.Version)

	// Activating the already-active version is a no-op success.
	activeVersion, err = repo.ActivateVersion(ctx, m.ID, "v1")
	require.NoError(t, err)
	assert.Equal(t, "v1", activeVersion)

	// Activating v2 clears v1 (one active per model).
	activeVersion, err = repo.ActivateVersion(ctx, m.ID, "v2")
	require.NoError(t, err)
	assert.Equal(t, "v2", activeVersion)

	active, err = repo.ActiveVersion(ctx, m.ID)
	require.NoError(t, err)
	require.NotNil(t, active)
	assert.Equal(t, "v2", active.Version)

	// Only one active row.
	var count int64
	require.NoError(t, repo.db.DB(ctx).Model(&Version{}).Where("model_id = ? AND is_active = ?", m.ID, true).Count(&count).Error)
	assert.Equal(t, int64(1), count)

	// Unknown version → 10103.
	_, err = repo.ActivateVersion(ctx, m.ID, "nope")
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelVersionNotFound, ae.Code)
}

// TestRepositoryActiveVersionPerModel verifies the one-active-per-model
// invariant (feature #32, AD2): activating a version on one model must
// never be blocked by an active version on a different model. This is
// enforced by the partial unique index (model_id) WHERE is_active, which
// SQLite does not enforce the same way as PostgreSQL, so the invariant
// is asserted at the repository layer.
func TestRepositoryActiveVersionPerModel(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	modelA := &Model{Name: "qwen-3b", CreatedAt: base}
	require.NoError(t, repo.CreateModel(ctx, modelA))
	require.NoError(t, repo.CreateVersion(ctx, &Version{ModelID: modelA.ID, Version: "v1", WeightPath: "qwen/v1", CreatedAt: base}))

	modelB := &Model{Name: "qwen-7b", CreatedAt: base}
	require.NoError(t, repo.CreateModel(ctx, modelB))
	require.NoError(t, repo.CreateVersion(ctx, &Version{ModelID: modelB.ID, Version: "v1", WeightPath: "qwen/v1", CreatedAt: base}))

	// Activate v1 on model A.
	activeVersion, err := repo.ActivateVersion(ctx, modelA.ID, "v1")
	require.NoError(t, err)
	assert.Equal(t, "v1", activeVersion)

	// Activating v1 on model B succeeds independently of model A's
	// active version (the per-model invariant).
	activeVersion, err = repo.ActivateVersion(ctx, modelB.ID, "v1")
	require.NoError(t, err)
	assert.Equal(t, "v1", activeVersion)

	// Both models have exactly one active version each.
	for _, id := range []string{modelA.ID, modelB.ID} {
		var count int64
		require.NoError(t, repo.db.DB(ctx).Model(&Version{}).Where("model_id = ? AND is_active = ?", id, true).Count(&count).Error)
		assert.Equal(t, int64(1), count)
	}

	// Re-activating on model A does not disturb model B's active row.
	activeVersion, err = repo.ActivateVersion(ctx, modelA.ID, "v1")
	require.NoError(t, err)
	assert.Equal(t, "v1", activeVersion)
	var count int64
	require.NoError(t, repo.db.DB(ctx).Model(&Version{}).Where("model_id = ? AND is_active = ?", modelB.ID, true).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestRepositoryListVersionsWithCounts(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	m := &Model{Name: "qwen-3b", CreatedAt: base}
	require.NoError(t, repo.CreateModel(ctx, m))
	require.NoError(t, repo.CreateVersion(ctx, &Version{ModelID: m.ID, Version: "v1", WeightPath: "qwen/v1", CreatedAt: base}))
	require.NoError(t, repo.CreateVersion(ctx, &Version{ModelID: m.ID, Version: "v2", WeightPath: "qwen/v2", CreatedAt: base.Add(time.Hour)}))
	_, err := repo.ActivateVersion(ctx, m.ID, "v1")
	require.NoError(t, err)

	rows, activeVersion, err := repo.ListVersionsWithCounts(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, "v1", activeVersion)
	require.Len(t, rows, 2)
	// Newest first.
	assert.Equal(t, "v2", rows[0].Version.Version)
	assert.Equal(t, "v1", rows[1].Version.Version)
	assert.Equal(t, true, rows[1].IsActive)
	assert.Equal(t, int64(0), rows[0].DeploymentCount)
}

func TestRepositoryDeleteModelCascadesVersions(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()

	m := &Model{Name: "qwen-3b"}
	require.NoError(t, repo.CreateModel(ctx, m))
	require.NoError(t, repo.CreateVersion(ctx, &Version{ModelID: m.ID, Version: "v1", WeightPath: "qwen/v1"}))

	require.NoError(t, repo.DeleteModel(ctx, m.ID))

	_, err := repo.FindByName(ctx, "qwen-3b")
	require.Error(t, err)

	var count int64
	require.NoError(t, repo.db.DB(ctx).Model(&Version{}).Where("model_id = ?", m.ID).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestRepositoryRegisterModelOrCreateVersion(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()

	// New model: creates model + first version.
	id1, err := repo.RegisterModelOrCreateVersion(ctx, "qwen-3b", "Qwen 3B", "", "v1", "qwen/v1")
	require.NoError(t, err)
	assert.NotEmpty(t, id1)

	// Same model, new version: appends.
	id2, err := repo.RegisterModelOrCreateVersion(ctx, "qwen-3b", "ignored description", "", "v2", "qwen/v2")
	require.NoError(t, err)
	assert.Equal(t, id1, id2)

	versions, err := repo.ListVersionsByModel(ctx, id1)
	require.NoError(t, err)
	assert.Len(t, versions, 2)

	// Same model, same version: conflict 10102.
	_, err = repo.RegisterModelOrCreateVersion(ctx, "qwen-3b", "", "", "v1", "qwen/v1")
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelExists, ae.Code)
}

func TestRepositoryFindByHubModelID(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()

	// Unbound lookup and an empty id both miss.
	_, err := repo.FindByHubModelID(ctx, "MiniMax/MiniMax-M3.1-Flash")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeModelNotFound, apierrors.CodeOf(err))
	_, err = repo.FindByHubModelID(ctx, "")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeModelNotFound, apierrors.CodeOf(err))

	id, err := repo.RegisterModelOrCreateVersion(ctx, "minimax", "", "MiniMax/MiniMax-M3.1-Flash", "v1", "minimax/v1")
	require.NoError(t, err)

	found, err := repo.FindByHubModelID(ctx, "MiniMax/MiniMax-M3.1-Flash")
	require.NoError(t, err)
	assert.Equal(t, id, found.ID)
	assert.Equal(t, "minimax", found.Name)

	// Two models may exist without a hub id (the unique index is partial).
	_, err = repo.RegisterModelOrCreateVersion(ctx, "a", "", "", "v1", "a/v1")
	require.NoError(t, err)
	_, err = repo.RegisterModelOrCreateVersion(ctx, "b", "", "", "v1", "b/v1")
	require.NoError(t, err)
}

func TestRepositoryHubModelIDUniqueness(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()

	_, err := repo.RegisterModelOrCreateVersion(ctx, "minimax", "", "MiniMax/MiniMax-M3.1-Flash", "v1", "minimax/v1")
	require.NoError(t, err)

	// Another model cannot claim the same hub id.
	_, err = repo.RegisterModelOrCreateVersion(ctx, "other", "", "MiniMax/MiniMax-M3.1-Flash", "v1", "other/v1")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeModelExists, apierrors.CodeOf(err))

	// Rebinding an existing model to a different hub id conflicts.
	_, err = repo.RegisterModelOrCreateVersion(ctx, "minimax", "", "MiniMax/MiniMax-M1", "v2", "minimax/v2")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeModelExists, apierrors.CodeOf(err))

	// A model registered without a hub id can gain one later.
	_, err = repo.RegisterModelOrCreateVersion(ctx, "local", "", "", "v1", "local/v1")
	require.NoError(t, err)
	_, err = repo.RegisterModelOrCreateVersion(ctx, "local", "", "Acme/Local-7B", "v2", "local/v2")
	require.NoError(t, err)
	bound, err := repo.FindByHubModelID(ctx, "Acme/Local-7B")
	require.NoError(t, err)
	assert.Equal(t, "local", bound.Name)
}
