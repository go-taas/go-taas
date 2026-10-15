package batch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	batchv1 "github.com/go-taas/go-taas/proto/taas/batch/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/tenancy"
)

// fakeBatchOrgQuotaReader returns a fixed quota for the org.
type fakeBatchOrgQuotaReader struct {
	quota tenancy.OrgQuota
	err   error
}

func (f fakeBatchOrgQuotaReader) OrgQuota(context.Context, string) (tenancy.OrgQuota, error) {
	return f.quota, f.err
}

// TestCreateBatchJobDailyQuota covers the daily batch-job guard
// (feature #51, FR3.2, AD15): at the limit the create is rejected with
// 13303; below the limit or unlimited it proceeds.
func TestCreateBatchJobDailyQuota(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewMemFileStore())
	ctx := orgCtx("org-a")

	// Seed one job created today so the daily count is 1.
	repo := NewRepository(db)
	_, err := repo.InsertBatchJob(ctx, &BatchJob{OrganizationID: "org-a", Model: "gpt-4o", Status: StatusValidating})
	require.NoError(t, err)

	// Cap at 1 job/day: the next create is rejected.
	svc.SetOrgQuotaReader(fakeBatchOrgQuotaReader{quota: tenancy.OrgQuota{MaxBatchJobsPerDay: 1}})
	_, err = svc.CreateBatchJob(ctx, &batchv1.CreateBatchJobRequest{InputFile: validJSONL(), CompletionWindow: "24h"})
	assert.EqualValues(t, apierrors.CodeQuotaBatchJobsExceeded, apierrors.CodeOf(err))

	// Cap at 2: the create proceeds.
	svc.SetOrgQuotaReader(fakeBatchOrgQuotaReader{quota: tenancy.OrgQuota{MaxBatchJobsPerDay: 2}})
	resp, err := svc.CreateBatchJob(ctx, &batchv1.CreateBatchJobRequest{InputFile: validJSONL(), CompletionWindow: "24h"})
	require.NoError(t, err)
	assert.Equal(t, batchv1.BatchJobStatus_BATCH_JOB_STATUS_VALIDATING, resp.GetBatchJob().GetStatus())

	// Unlimited (0) skips the check.
	svc.SetOrgQuotaReader(fakeBatchOrgQuotaReader{quota: tenancy.OrgQuota{MaxBatchJobsPerDay: 0}})
	resp, err = svc.CreateBatchJob(ctx, &batchv1.CreateBatchJobRequest{InputFile: validJSONL(), CompletionWindow: "24h"})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetBatchJob().GetBatchId())

	// Nil reader skips the check.
	svc.SetOrgQuotaReader(nil)
	resp, err = svc.CreateBatchJob(ctx, &batchv1.CreateBatchJobRequest{InputFile: validJSONL(), CompletionWindow: "24h"})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetBatchJob().GetBatchId())
}
