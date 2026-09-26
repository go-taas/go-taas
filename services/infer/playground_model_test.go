package infer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// fakeSessionOrgResolver is an injected SessionOrgResolver (feature-17
// AD6).
type fakeSessionOrgResolver struct {
	org string
	err error
}

func (f fakeSessionOrgResolver) SessionActiveOrg(context.Context) (string, error) {
	return f.org, f.err
}

// TestPlaygroundModel covers the user-realm model playground (feature-17
// AD8): org resolution, model authorization (10105), no-ready-service
// (10301), and a request_id on success.
func TestPlaygroundModel(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	// No ready service for the model → 10301.
	_, err := svc.PlaygroundModel(orgContext("org-a"), &inferv1.PlaygroundModelRequest{
		ModelId: modelID, Prompt: "hello",
	})
	assert.EqualValues(t, apierrors.CodeInferServiceNotFound, apierrors.CodeOf(err))

	// Create a running service for the model.
	repo, err := svc.repository()
	require.NoError(t, err)
	svcRow := &InferenceService{
		ID:             NewServiceID(),
		OrganizationID: "org-a",
		Name:           "svc",
		ModelID:        modelID,
		ModelVersion:   "v1",
		State:          StateRunning,
		Endpoints:      []string{"http://e"},
		UpdatedAt:      time.Now(),
	}
	require.NoError(t, repo.Create(context.Background(), svcRow))

	// A restricted model the org is not granted → 10105.
	grantModelAccess(t, db, modelID, "org-b")
	_, err = svc.PlaygroundModel(orgContext("org-a"), &inferv1.PlaygroundModelRequest{
		ModelId: modelID, Prompt: "hello",
	})
	assert.EqualValues(t, apierrors.CodeModelUnauthorized, apierrors.CodeOf(err))

	// Grant org-a → success with a request_id.
	grantModelAccess(t, db, modelID, "org-a")
	resp, err := svc.PlaygroundModel(orgContext("org-a"), &inferv1.PlaygroundModelRequest{
		ModelId: modelID, Prompt: "hello",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetRequestId())

	// Session resolver wins over the header (feature-17 AD6).
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})
	resp, err = svc.PlaygroundModel(orgContext("org-b"), &inferv1.PlaygroundModelRequest{
		ModelId: modelID, Prompt: "hello",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetRequestId())

	// Empty prompt → error.
	_, err = svc.PlaygroundModel(orgContext("org-a"), &inferv1.PlaygroundModelRequest{
		ModelId: modelID, Prompt: "",
	})
	require.Error(t, err)
}
