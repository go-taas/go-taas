package batch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	batchv1 "github.com/go-taas/go-taas/proto/taas/batch/v1"
)

// completeJob marks a job completed with result/error files written.
func completeJob(t *testing.T, svc *Service, store FileStore, id, orgID string) {
	t.Helper()
	ctx := orgCtx(orgID)
	repo := svc.repo
	now := time.Now().UTC()
	expire := now.Add(24 * time.Hour)
	require.NoError(t, repo.SetStatus(ctx, id, StatusCompleted, &now, &expire))
	_, err := store.PutResult(ctx, orgID, id, []byte(`{"custom_id":"a","response":{}}`+"\n"))
	require.NoError(t, err)
	_, err = store.PutError(ctx, orgID, id, []byte(`{"custom_id":"b","error":"boom"}`+"\n"))
	require.NoError(t, err)
	require.NoError(t, repo.SetFileKeys(ctx, id, resultKey(orgID, id), errorKey(orgID, id)))
}

func TestServiceDownloadResultAndError(t *testing.T) {
	db := newTestDB(t)
	store := NewMemFileStore()
	svc := NewForFVT(db, store)
	ctx := orgCtx("org-a")

	created, err := svc.CreateBatchJob(ctx, &batchv1.CreateBatchJobRequest{InputFile: validJSONL()})
	require.NoError(t, err)
	id := created.GetBatchJob().GetBatchId()

	completeJob(t, svc, store, id, "org-a")

	res, err := svc.DownloadBatchResult(ctx, &batchv1.DownloadBatchResultRequest{BatchId: id})
	require.NoError(t, err)
	assert.Contains(t, string(res.GetContent()), "custom_id")
	assert.Equal(t, "batch-"+id+"-result.jsonl", res.GetFilename())

	errRes, err := svc.DownloadBatchError(ctx, &batchv1.DownloadBatchErrorRequest{BatchId: id})
	require.NoError(t, err)
	assert.Contains(t, string(errRes.GetContent()), "boom")
	assert.Equal(t, "batch-"+id+"-error.jsonl", errRes.GetFilename())
}

func TestServiceDownloadErrorBeforeCompleted(t *testing.T) {
	db := newTestDB(t)
	store := NewMemFileStore()
	svc := NewForFVT(db, store)
	ctx := orgCtx("org-a")

	created, err := svc.CreateBatchJob(ctx, &batchv1.CreateBatchJobRequest{InputFile: validJSONL()})
	require.NoError(t, err)
	id := created.GetBatchJob().GetBatchId()

	_, err = svc.DownloadBatchError(ctx, &batchv1.DownloadBatchErrorRequest{BatchId: id})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchNotCompleted, apierrors.CodeOf(err))
}

func TestServiceAdminGetUnknown(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewMemFileStore())
	svc.roleGuard = &fakeRoleGuard{}
	svc.sessionUserResolver = &fakeSessionUserResolver{}
	ctx := orgCtx("org-a")

	_, err := svc.AdminGetBatchJob(ctx, &batchv1.AdminGetBatchJobRequest{BatchId: "00000000-0000-0000-0000-000000000000"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchJobNotFound, apierrors.CodeOf(err))
}

func TestServiceAdminCancelTerminal(t *testing.T) {
	db := newTestDB(t)
	store := NewMemFileStore()
	svc := NewForFVT(db, store)
	svc.roleGuard = &fakeRoleGuard{}
	svc.sessionUserResolver = &fakeSessionUserResolver{}
	ctx := orgCtx("org-a")

	created, err := svc.CreateBatchJob(ctx, &batchv1.CreateBatchJobRequest{InputFile: validJSONL()})
	require.NoError(t, err)
	id := created.GetBatchJob().GetBatchId()
	completeJob(t, svc, store, id, "org-a")

	_, err = svc.AdminCancelBatchJob(ctx, &batchv1.AdminCancelBatchJobRequest{BatchId: id})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchJobStateInvalid, apierrors.CodeOf(err))
}

func TestMemFileStoreRoundTrip(t *testing.T) {
	store := NewMemFileStore()
	ctx := context.Background()

	key, err := store.PutInput(ctx, "org-a", "batch-1", []byte("input"))
	require.NoError(t, err)
	assert.Equal(t, "batch/org-a/batch-1/input.jsonl", key)

	content, err := store.GetFile(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "input", string(content))

	_, err = store.PutResult(ctx, "org-a", "batch-1", []byte("result"))
	require.NoError(t, err)
	_, err = store.PutError(ctx, "org-a", "batch-1", []byte("error"))
	require.NoError(t, err)

	require.NoError(t, store.DeleteFiles(ctx, "org-a", "batch-1"))
	_, err = store.GetFile(ctx, resultKey("org-a", "batch-1"))
	require.Error(t, err)
	// Input file is retained.
	_, err = store.GetFile(ctx, key)
	require.NoError(t, err)
}

func TestLocalFileStoreRoundTrip(t *testing.T) {
	store := NewLocalFileStore(t.TempDir())
	ctx := context.Background()

	key, err := store.PutInput(ctx, "org-a", "batch-1", []byte("input"))
	require.NoError(t, err)
	content, err := store.GetFile(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "input", string(content))

	_, err = store.PutResult(ctx, "org-a", "batch-1", []byte("result"))
	require.NoError(t, err)
	require.NoError(t, store.DeleteFiles(ctx, "org-a", "batch-1"))
	_, err = store.GetFile(ctx, resultKey("org-a", "batch-1"))
	require.Error(t, err)
}

func TestStatusToProto(t *testing.T) {
	assert.Equal(t, batchv1.BatchJobStatus_BATCH_JOB_STATUS_VALIDATING, statusToProto(StatusValidating))
	assert.Equal(t, batchv1.BatchJobStatus_BATCH_JOB_STATUS_IN_PROGRESS, statusToProto(StatusInProgress))
	assert.Equal(t, batchv1.BatchJobStatus_BATCH_JOB_STATUS_FINALIZING, statusToProto(StatusFinalizing))
	assert.Equal(t, batchv1.BatchJobStatus_BATCH_JOB_STATUS_COMPLETED, statusToProto(StatusCompleted))
	assert.Equal(t, batchv1.BatchJobStatus_BATCH_JOB_STATUS_FAILED, statusToProto(StatusFailed))
	assert.Equal(t, batchv1.BatchJobStatus_BATCH_JOB_STATUS_EXPIRED, statusToProto(StatusExpired))
	assert.Equal(t, batchv1.BatchJobStatus_BATCH_JOB_STATUS_CANCELLED, statusToProto(StatusCancelled))
	assert.Equal(t, batchv1.BatchJobStatus_BATCH_JOB_STATUS_UNSPECIFIED, statusToProto("bogus"))
}
