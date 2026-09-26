package infer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/image"
)

// TestCreateInferenceServiceCompatibilityEnforcement covers the
// deploy-time matrix enforcement (feature #19, AD13): an unsupported
// combination is blocked, an experimental combination deploys with a
// warning flag, and a supported combination deploys normally.
func TestCreateInferenceServiceCompatibilityEnforcement(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	// Inject a fake compatibility checker.
	svc.SetCompatibilityChecker(func(_ context.Context, _, _, _ string) (string, error) {
		return image.StatusUnsupported, nil
	})

	// AC: an unsupported combination is blocked with 10212.
	_, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia", AcceleratorType: "A800",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeCompatibilityUnsupported, ae.Code)
	// Nothing was published.
	assert.Empty(t, client.published)

	// An experimental combination deploys with a warning flag.
	svc.SetCompatibilityChecker(func(_ context.Context, _, _, _ string) (string, error) {
		return image.StatusExperimental, nil
	})
	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia", AcceleratorType: "A800",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetServiceId())
	assert.NotEmpty(t, resp.GetWarning(), "experimental deploy carries a warning")

	// A supported combination deploys normally (no warning).
	svc.SetCompatibilityChecker(func(_ context.Context, _, _, _ string) (string, error) {
		return image.StatusSupported, nil
	})
	resp, err = svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-svc2", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia", AcceleratorType: "A800",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetServiceId())
	assert.Empty(t, resp.GetWarning(), "supported deploy has no warning")
}

// TestCreateInferenceServiceNoCompatibilityChecker covers the nil-checker
// case: without a wired checker, the deploy proceeds normally (backward
// compatible).
func TestCreateInferenceServiceNoCompatibilityChecker(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia", AcceleratorType: "A800",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetServiceId())
	assert.Empty(t, resp.GetWarning())
}
