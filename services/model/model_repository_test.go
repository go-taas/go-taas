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
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Model{}, &Version{}))
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
	id1, err := repo.RegisterModelOrCreateVersion(ctx, "qwen-3b", "v1", "qwen/v1", "Qwen 3B")
	require.NoError(t, err)
	assert.NotEmpty(t, id1)

	// Same model, new version: appends.
	id2, err := repo.RegisterModelOrCreateVersion(ctx, "qwen-3b", "v2", "qwen/v2", "ignored description")
	require.NoError(t, err)
	assert.Equal(t, id1, id2)

	versions, err := repo.ListVersionsByModel(ctx, id1)
	require.NoError(t, err)
	assert.Len(t, versions, 2)

	// Same model, same version: conflict 10102.
	_, err = repo.RegisterModelOrCreateVersion(ctx, "qwen-3b", "v1", "qwen/v1", "")
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelExists, ae.Code)
}
