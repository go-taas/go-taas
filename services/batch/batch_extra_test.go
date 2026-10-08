package batch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	batchv1 "github.com/go-taas/go-taas/proto/taas/batch/v1"
)

// TestListBatchJobsAnyOrg covers the admin fleet list.
func TestListBatchJobsAnyOrg(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	seedJob(t, db, "org-a")
	seedJob(t, db, "org-b")

	rows, total, err := repo.ListBatchJobsAnyOrg(ctx, BatchFilter{Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, rows, 2)

	// Org filter.
	rows, total, err = repo.ListBatchJobsAnyOrg(ctx, BatchFilter{OrganizationID: "org-a", Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Len(t, rows, 1)

	// Status filter.
	rows, total, err = repo.ListBatchJobsAnyOrg(ctx, BatchFilter{Status: StatusCompleted, Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.Len(t, rows, 0)
}

// TestFindBatchJobAnyOrg covers the admin get.
func TestFindBatchJobAnyOrg(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	job := seedJob(t, db, "org-a")
	got, err := repo.FindBatchJobAnyOrg(ctx, job.BatchID)
	require.NoError(t, err)
	assert.Equal(t, job.BatchID, got.BatchID)

	// Unknown -> 12601.
	_, err = repo.FindBatchJobAnyOrg(ctx, "00000000-0000-0000-0000-000000000000")
	require.Error(t, err)
}

// TestInsertBatchJobError covers the insert error path.
func TestInsertBatchJobError(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	// Insert a job with a duplicate primary key to force an error.
	job := seedJob(t, db, "org-a")
	dup := &BatchJob{
		BatchID:        job.BatchID,
		OrganizationID: "org-a",
		Model:          "gpt-4o",
		Status:         StatusValidating,
	}
	_, err := repo.InsertBatchJob(ctx, dup)
	require.Error(t, err)
}

// TestMemFileStorePutError covers the PutError path.
func TestMemFileStorePutError(t *testing.T) {
	store := NewMemFileStore()
	ctx := context.Background()

	key, err := store.PutError(ctx, "org-a", "batch-1", []byte("error"))
	require.NoError(t, err)
	assert.Equal(t, "batch/org-a/batch-1/error.jsonl", key)

	content, err := store.GetFile(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "error", string(content))
}

// TestLocalFileStorePutError covers the local PutError path.
func TestLocalFileStorePutError(t *testing.T) {
	store := NewLocalFileStore(t.TempDir())
	ctx := context.Background()

	key, err := store.PutError(ctx, "org-a", "batch-1", []byte("error"))
	require.NoError(t, err)
	content, err := store.GetFile(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "error", string(content))
}

// TestServiceResolveOrgNoHeader covers the missing-org error path.
func TestServiceResolveOrgNoHeader(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewMemFileStore())
	ctx := context.Background()

	_, err := svc.ListBatchJobs(ctx, &batchv1.ListBatchJobsRequest{})
	require.Error(t, err)
}

// TestServiceRecordAuditNil covers the nil audit recorder path.
func TestServiceRecordAuditNil(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewMemFileStore())
	// auditRecorder is nil; recordAudit should be a no-op.
	svc.recordAudit(context.Background(), "batch.created", "org-a", "batch-1")
}

// errorSessionUserResolver returns an error.
type errorSessionUserResolver struct{}

func (e *errorSessionUserResolver) SessionUserID(_ context.Context) (string, error) {
	return "", apierrors.New(apierrors.CodeInternal)
}

// TestServiceRequireAdminRoleError covers the session-user error path.
func TestServiceRequireAdminRoleError(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewMemFileStore())
	svc.sessionUserResolver = &errorSessionUserResolver{}
	svc.roleGuard = &fakeRoleGuard{}
	ctx := orgCtx("org-a")

	_, err := svc.AdminListBatchJobs(ctx, &batchv1.AdminListBatchJobsRequest{})
	require.Error(t, err)
}

// TestServiceResolveOrgSession covers the session-org resolver path.
func TestServiceResolveOrgSession(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewMemFileStore())
	svc.sessionOrgResolver = &fakeSessionOrgResolver{org: "org-session"}
	ctx := context.Background()

	resp, err := svc.ListBatchJobs(ctx, &batchv1.ListBatchJobsRequest{})
	require.NoError(t, err)
	assert.NotNil(t, resp)
}

// fakeSessionOrgResolver returns a fixed org.
type fakeSessionOrgResolver struct {
	org string
}

func (f *fakeSessionOrgResolver) SessionActiveOrg(_ context.Context) (string, error) {
	return f.org, nil
}
