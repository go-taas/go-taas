package batch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	batchv1 "github.com/go-taas/go-taas/proto/taas/batch/v1"
)

// orgCtx injects the transitional X-Organization-Id metadata.
func orgCtx(orgID string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(organizationMetadataKey, orgID))
}

// fakeRoleGuard allows or denies admin role checks.
type fakeRoleGuard struct {
	deny bool
}

func (f *fakeRoleGuard) RequireRole(_ context.Context, _ string, _ string, _ string) error {
	if f.deny {
		return apierrors.New(apierrors.CodeForbidden)
	}
	return nil
}

// fakeSessionUserResolver returns a fixed user id.
type fakeSessionUserResolver struct{}

func (f *fakeSessionUserResolver) SessionUserID(_ context.Context) (string, error) {
	return "user-1", nil
}

func TestServiceCreateBatchJob(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewMemFileStore())
	ctx := orgCtx("org-a")

	resp, err := svc.CreateBatchJob(ctx, &batchv1.CreateBatchJobRequest{
		InputFile:        validJSONL(),
		CompletionWindow: "24h",
	})
	require.NoError(t, err)
	assert.Equal(t, batchv1.BatchJobStatus_BATCH_JOB_STATUS_VALIDATING, resp.GetBatchJob().GetStatus())
	assert.Equal(t, "gpt-4o", resp.GetBatchJob().GetModel())
	assert.Equal(t, int64(2), resp.GetBatchJob().GetTotalRequests())
}

func TestServiceCreateBatchJobInvalid(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewMemFileStore())
	ctx := orgCtx("org-a")

	// Invalid JSONL -> 12603.
	_, err := svc.CreateBatchJob(ctx, &batchv1.CreateBatchJobRequest{InputFile: []byte(`{"bad`)})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchInputInvalid, apierrors.CodeOf(err))

	// Invalid completion window -> 12603.
	_, err = svc.CreateBatchJob(ctx, &batchv1.CreateBatchJobRequest{InputFile: validJSONL(), CompletionWindow: "99h"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchInputInvalid, apierrors.CodeOf(err))
}

func TestServiceListAndGetBatchJob(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewMemFileStore())
	ctx := orgCtx("org-a")

	created, err := svc.CreateBatchJob(ctx, &batchv1.CreateBatchJobRequest{InputFile: validJSONL()})
	require.NoError(t, err)
	id := created.GetBatchJob().GetBatchId()

	list, err := svc.ListBatchJobs(ctx, &batchv1.ListBatchJobsRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetBatchJobs(), 1)

	got, err := svc.GetBatchJob(ctx, &batchv1.GetBatchJobRequest{BatchId: id})
	require.NoError(t, err)
	assert.Equal(t, id, got.GetBatchJob().GetBatchId())

	// Unknown id -> 12601.
	_, err = svc.GetBatchJob(ctx, &batchv1.GetBatchJobRequest{BatchId: "00000000-0000-0000-0000-000000000000"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchJobNotFound, apierrors.CodeOf(err))
}

func TestServiceCancelBatchJob(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewMemFileStore())
	ctx := orgCtx("org-a")

	created, err := svc.CreateBatchJob(ctx, &batchv1.CreateBatchJobRequest{InputFile: validJSONL()})
	require.NoError(t, err)
	id := created.GetBatchJob().GetBatchId()

	cancelled, err := svc.CancelBatchJob(ctx, &batchv1.CancelBatchJobRequest{BatchId: id})
	require.NoError(t, err)
	assert.Equal(t, batchv1.BatchJobStatus_BATCH_JOB_STATUS_CANCELLED, cancelled.GetBatchJob().GetStatus())

	// Cancel in terminal state -> 12602.
	_, err = svc.CancelBatchJob(ctx, &batchv1.CancelBatchJobRequest{BatchId: id})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchJobStateInvalid, apierrors.CodeOf(err))
}

func TestServiceDownloadBeforeCompleted(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewMemFileStore())
	ctx := orgCtx("org-a")

	created, err := svc.CreateBatchJob(ctx, &batchv1.CreateBatchJobRequest{InputFile: validJSONL()})
	require.NoError(t, err)
	id := created.GetBatchJob().GetBatchId()

	// Download before completed -> 12606.
	_, err = svc.DownloadBatchResult(ctx, &batchv1.DownloadBatchResultRequest{BatchId: id})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchNotCompleted, apierrors.CodeOf(err))
}

func TestServiceAdminRoleGuard(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewMemFileStore())
	svc.roleGuard = &fakeRoleGuard{deny: true}
	svc.sessionUserResolver = &fakeSessionUserResolver{}
	ctx := orgCtx("org-a")

	_, err := svc.AdminListBatchJobs(ctx, &batchv1.AdminListBatchJobsRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

func TestServiceAdminListAndCancel(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewMemFileStore())
	svc.roleGuard = &fakeRoleGuard{}
	svc.sessionUserResolver = &fakeSessionUserResolver{}
	ctx := orgCtx("org-a")

	created, err := svc.CreateBatchJob(ctx, &batchv1.CreateBatchJobRequest{InputFile: validJSONL()})
	require.NoError(t, err)
	id := created.GetBatchJob().GetBatchId()

	list, err := svc.AdminListBatchJobs(ctx, &batchv1.AdminListBatchJobsRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetBatchJobs(), 1)

	got, err := svc.AdminGetBatchJob(ctx, &batchv1.AdminGetBatchJobRequest{BatchId: id})
	require.NoError(t, err)
	assert.Equal(t, id, got.GetBatchJob().GetBatchId())

	cancelled, err := svc.AdminCancelBatchJob(ctx, &batchv1.AdminCancelBatchJobRequest{BatchId: id})
	require.NoError(t, err)
	assert.Equal(t, batchv1.BatchJobStatus_BATCH_JOB_STATUS_CANCELLED, cancelled.GetBatchJob().GetStatus())
}
