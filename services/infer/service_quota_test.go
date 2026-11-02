package infer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
)

// TestCreateInferenceServiceIsNotQuotaLimited documents the removal of
// the service-count quota: inference services are platform assets that
// only a platform administrator creates, so no organization, project or
// personal budget caps them. Creation succeeds without any quota seam.
func TestCreateInferenceServiceIsNotQuotaLimited(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	repo, err := svc.repository()
	require.NoError(t, err)
	// Seed a running service: with the old organization cap this would
	// have been the moment the next create was rejected.
	seedService(t, repo, "org-1", "existing", StateRunning, time.Now())

	for _, name := range []string{"svc-a", "svc-b", "svc-c"} {
		resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
			Name:         name,
			ModelId:      modelID,
			ModelVersion: "v1",
			ImageId:      "img-vllm-nvidia",
			Replicas:     1,
			Accelerator:  "nvidia",
		})
		require.NoError(t, err, "create %s must not be quota-limited", name)
		assert.NotEmpty(t, resp.GetServiceId())
	}
}

var _ = assert.EqualValues
