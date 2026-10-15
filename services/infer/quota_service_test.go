package infer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/tenancy"
)

// fakeInferOrgQuotaReader returns a fixed quota for the org.
type fakeInferOrgQuotaReader struct {
	quota tenancy.OrgQuota
	err   error
}

func (f fakeInferOrgQuotaReader) OrgQuota(context.Context, string) (tenancy.OrgQuota, error) {
	return f.quota, f.err
}

// TestCreateInferenceServiceServiceQuota covers the service-count guard
// (feature #51, FR3.1): at the limit the create is rejected with 13302;
// below the limit or unlimited it proceeds.
func TestCreateInferenceServiceServiceQuota(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	// Seed one running service for org-1.
	repo, err := svc.repository()
	require.NoError(t, err)
	seedService(t, repo, "org-1", "existing", StateRunning, time.Now())

	// Cap the org at 1 service: the next create is rejected.
	svc.SetOrgQuotaReader(fakeInferOrgQuotaReader{quota: tenancy.OrgQuota{MaxInferenceServices: 1}})
	_, err = svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name:         "demo-svc",
		ModelId:      modelID,
		ModelVersion: "v1",
		ImageId:      "img-vllm-nvidia",
		Replicas:     1,
		Accelerator:  "nvidia",
	})
	assert.EqualValues(t, apierrors.CodeQuotaServiceCountExceeded, apierrors.CodeOf(err))

	// Cap at 2: the create proceeds.
	svc.SetOrgQuotaReader(fakeInferOrgQuotaReader{quota: tenancy.OrgQuota{MaxInferenceServices: 2}})
	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name:         "demo-svc",
		ModelId:      modelID,
		ModelVersion: "v1",
		ImageId:      "img-vllm-nvidia",
		Replicas:     1,
		Accelerator:  "nvidia",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetServiceId())

	// Unlimited (0) skips the check.
	svc.SetOrgQuotaReader(fakeInferOrgQuotaReader{quota: tenancy.OrgQuota{MaxInferenceServices: 0}})
	resp, err = svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name:         "demo-svc-2",
		ModelId:      modelID,
		ModelVersion: "v1",
		ImageId:      "img-vllm-nvidia",
		Replicas:     1,
		Accelerator:  "nvidia",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetServiceId())

	// Nil reader skips the check.
	svc.SetOrgQuotaReader(nil)
	resp, err = svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name:         "demo-svc-3",
		ModelId:      modelID,
		ModelVersion: "v1",
		ImageId:      "img-vllm-nvidia",
		Replicas:     1,
		Accelerator:  "nvidia",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetServiceId())
}
