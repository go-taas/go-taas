package batch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&BatchJob{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func seedJob(t *testing.T, db *gorm.DB, orgID string) *BatchJob {
	t.Helper()
	repo := NewRepository(db)
	job := &BatchJob{
		OrganizationID:   orgID,
		Model:            "gpt-4o",
		Status:           StatusValidating,
		CompletionWindow: "24h",
		TotalRequests:    2,
		Currency:         "USD",
		CreatedAt:        time.Now().UTC(),
	}
	id, err := repo.InsertBatchJob(context.Background(), job)
	require.NoError(t, err)
	job.BatchID = id
	return job
}

func TestInsertAndFindBatchJob(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	job := seedJob(t, db, "org-a")
	got, err := repo.FindBatchJobByID(ctx, "org-a", job.BatchID)
	require.NoError(t, err)
	assert.Equal(t, job.BatchID, got.BatchID)
	assert.Equal(t, "gpt-4o", got.Model)

	// Unknown id -> 12601.
	_, err = repo.FindBatchJobByID(ctx, "org-a", "00000000-0000-0000-0000-000000000000")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchJobNotFound, apierrors.CodeOf(err))

	// Wrong org -> 12601.
	_, err = repo.FindBatchJobByID(ctx, "org-b", job.BatchID)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchJobNotFound, apierrors.CodeOf(err))
}

func TestListBatchJobsFilters(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	seedJob(t, db, "org-a")
	seedJob(t, db, "org-a")
	seedJob(t, db, "org-b")

	rows, total, err := repo.ListBatchJobs(ctx, BatchFilter{OrganizationID: "org-a", Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, rows, 2)

	// Status filter.
	rows, total, err = repo.ListBatchJobs(ctx, BatchFilter{OrganizationID: "org-a", Status: StatusCompleted, Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.Len(t, rows, 0)

	// Model filter.
	rows, total, err = repo.ListBatchJobs(ctx, BatchFilter{OrganizationID: "org-a", Model: "gpt-4o", Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, rows, 2)

	// Pagination.
	rows, total, err = repo.ListBatchJobs(ctx, BatchFilter{OrganizationID: "org-a", Offset: 1, Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, rows, 1)
}

func TestNextInProgressJob(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	// No in_progress job.
	job, err := repo.NextInProgressJob(ctx)
	require.NoError(t, err)
	assert.Nil(t, job)

	seedJob(t, db, "org-a")
	require.NoError(t, repo.SetStatus(ctx, seedJob(t, db, "org-a").BatchID, StatusInProgress, nil, nil))

	job, err = repo.NextInProgressJob(ctx)
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, StatusInProgress, job.Status)
}

func TestIncrementCountersAndSetStatus(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	job := seedJob(t, db, "org-a")
	require.NoError(t, repo.IncrementCounters(ctx, job.BatchID, 1, 1, 0, 100, 50, 25))
	got, err := repo.FindBatchJobByID(ctx, "org-a", job.BatchID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.ProcessedRequests)
	assert.Equal(t, int64(1), got.SucceededRequests)
	assert.Equal(t, int64(100), got.InputTokens)
	assert.Equal(t, int64(50), got.OutputTokens)
	assert.Equal(t, int64(25), got.Cost)

	now := time.Now().UTC()
	require.NoError(t, repo.SetStatus(ctx, job.BatchID, StatusCompleted, &now, &now))
	got, err = repo.FindBatchJobByID(ctx, "org-a", job.BatchID)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, got.Status)
	assert.NotNil(t, got.CompletedAt)
}

func TestFindExpiredFilesAndMarkExpired(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	job := seedJob(t, db, "org-a")
	now := time.Now().UTC()
	past := now.Add(-48 * time.Hour)
	require.NoError(t, repo.SetStatus(ctx, job.BatchID, StatusCompleted, &now, &past))

	rows, err := repo.FindExpiredFiles(ctx, now, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, job.BatchID, rows[0].BatchID)

	require.NoError(t, repo.MarkFilesExpired(ctx, job.BatchID))
	rows, err = repo.FindExpiredFiles(ctx, now, 10)
	require.NoError(t, err)
	assert.Len(t, rows, 0)
}
