package database

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// testModel is a minimal entity used by repository tests.
type testModel struct {
	ID    int64  `gorm:"primaryKey"`
	Name  string `gorm:"uniqueIndex"`
	Value int
}

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Use a uniquely named shared-cache in-memory database so concurrent
	// tests never share state while all connections of this test see the
	// same data.
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&testModel{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func TestBaseRepositoryCRUD(t *testing.T) {
	db := newTestDB(t)
	mgr := NewManager(db)
	repo := NewBaseRepository[testModel](mgr)
	ctx := context.Background()

	// Create.
	require.NoError(t, repo.Create(ctx, &testModel{ID: 1, Name: "a", Value: 10}))

	// GetByID.
	got, err := repo.GetByID(ctx, int64(1))
	require.NoError(t, err)
	assert.Equal(t, "a", got.Name)

	// GetByID miss.
	_, err = repo.GetByID(ctx, int64(99))
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// Update.
	got.Value = 42
	require.NoError(t, repo.Update(ctx, got))
	got, err = repo.GetByID(ctx, int64(1))
	require.NoError(t, err)
	assert.Equal(t, 42, got.Value)

	// UpdateFields.
	require.NoError(t, repo.UpdateFields(ctx, int64(1), map[string]any{"value": 7}))
	got, err = repo.GetByID(ctx, int64(1))
	require.NoError(t, err)
	assert.Equal(t, 7, got.Value)

	// CreateMany + List + Count.
	require.NoError(t, repo.CreateMany(ctx, []*testModel{{ID: 2, Name: "b"}, {ID: 3, Name: "c"}}))
	items, err := repo.List(ctx, nil, "id asc")
	require.NoError(t, err)
	assert.Len(t, items, 3)
	count, err := repo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)

	// Paginate.
	page, total, err := repo.Paginate(ctx, 1, 2, nil, "id asc")
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Len(t, page, 2)
	assert.Equal(t, int64(2), page[0].ID)
	assert.Equal(t, int64(3), page[1].ID)

	// Invalid pagination.
	_, _, err = repo.Paginate(ctx, 0, 0, nil)
	assert.Error(t, err)

	// Delete.
	require.NoError(t, repo.Delete(ctx, int64(1)))
	count, err = repo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

func TestWithinTxCommitsAndRollsBack(t *testing.T) {
	db := newTestDB(t)
	mgr := NewManager(db)
	repo := NewBaseRepository[testModel](mgr)
	ctx := context.Background()

	// Commit path.
	require.NoError(t, mgr.WithinTx(ctx, func(ctx context.Context) error {
		return repo.Create(ctx, &testModel{ID: 1, Name: "tx-ok"})
	}))
	count, err := repo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// Rollback path.
	require.Error(t, mgr.WithinTx(ctx, func(ctx context.Context) error {
		if err := repo.Create(ctx, &testModel{ID: 2, Name: "tx-fail"}); err != nil {
			return err
		}
		return assert.AnError // trigger rollback
	}))
	count, err = repo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "rolled-back insert must not persist")

	// Nested calls join the outer transaction.
	require.NoError(t, mgr.WithinTx(ctx, func(ctx context.Context) error {
		require.NoError(t, repo.Create(ctx, &testModel{ID: 3, Name: "outer"}))
		return mgr.WithinTx(ctx, func(ctx context.Context) error {
			return repo.Create(ctx, &testModel{ID: 4, Name: "inner"})
		})
	}))
	count, err = repo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestUpsert(t *testing.T) {
	db := newTestDB(t)
	mgr := NewManager(db)
	repo := NewBaseRepository[testModel](mgr)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, &testModel{ID: 1, Name: "first", Value: 1}, []string{"id"}, []string{"name", "value"}))
	require.NoError(t, repo.Upsert(ctx, &testModel{ID: 1, Name: "second", Value: 2}, []string{"id"}, []string{"name", "value"}))

	got, err := repo.GetByID(ctx, int64(1))
	require.NoError(t, err)
	assert.Equal(t, "second", got.Name)
	assert.Equal(t, 2, got.Value)

	count, err := repo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}
