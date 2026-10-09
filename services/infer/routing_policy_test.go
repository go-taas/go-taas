package infer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/services/audit"
)

// routingAdminCtx returns a context carrying the admin request path
// metadata (feature #45, AD6).
func routingAdminCtx() context.Context {
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("x-request-path", "/api/v1/admin/routing-policies", organizationMetadataKey, "org-a"))
}

// newRoutingTestService wires a service with the admin session seams
// and migrates the routing tables.
func newRoutingTestService(t *testing.T) (*Service, *recordingClient, *gorm.DB) {
	t.Helper()
	svc, client, db := newInferTestService(t)
	require.NoError(t, db.AutoMigrate(&RoutingPolicy{}, &RoutingPolicyRevision{}, &RoutingPolicyOutbox{}))
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "admin-1"})
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})
	svc.SetRoleGuard(fakeRoleGuard{allowed: true})
	return svc, client, db
}

// seedRoutingModel registers a model, activates its version and returns
// the model id.
func seedRoutingModel(t *testing.T, svc *Service, name, version string) string {
	t.Helper()
	modelRepo, err := svc.modelRepository()
	require.NoError(t, err)
	id, err := modelRepo.RegisterModelOrCreateVersion(context.Background(), name, "", version, name+"/"+version)
	require.NoError(t, err)
	_, err = modelRepo.ActivateVersion(context.Background(), id, version)
	require.NoError(t, err)
	return id
}

// seedRoutingService inserts an inference service row directly.
func seedRoutingService(t *testing.T, svc *Service, modelID, version, state string, endpoints []string) string {
	t.Helper()
	repo, err := svc.repository()
	require.NoError(t, err)
	row := &InferenceService{
		ID:             NewServiceID(),
		OrganizationID: "org-a",
		Name:           "svc-" + state + "-" + NewServiceID()[:8],
		ModelID:        modelID,
		ModelVersion:   version,
		ImageID:        "img-1",
		Replicas:       1,
		Accelerator:    "nvidia",
		State:          state,
		Endpoints:      endpoints,
	}
	require.NoError(t, repo.Create(context.Background(), row))
	return row.ID
}

// routingUpdate builds a valid update request for the given model.
func routingUpdate(modelID string, enabled bool, serviceIDs []string, attempts int32, retryOn []inferv1.RetryCategory, expected int64) *inferv1.UpdateRoutingPolicyRequest {
	return &inferv1.UpdateRoutingPolicyRequest{
		ModelId:          modelID,
		ModelVersion:     "v1",
		Enabled:          enabled,
		ServiceIds:       serviceIDs,
		MaxAttempts:      attempts,
		RetryOn:          retryOn,
		ExpectedRevision: expected,
	}
}

// TestRoutingPolicyAdminEnforcement verifies every routing-policy RPC
// fails closed without an admin session (feature #45, AD6): a missing
// role guard, a missing session user or a denied role yields 10027,
// never data.
func TestRoutingPolicyAdminEnforcement(t *testing.T) {
	svc, _, db := newRoutingTestService(t)
	modelID := seedRoutingModel(t, svc, "qwen-3b", "v1")
	readyID := seedRoutingService(t, svc, modelID, "v1", StateRunning, []string{"http://10.0.0.1:8000"})

	// Missing role guard: fail closed.
	svc.SetRoleGuard(nil)
	_, err := svc.ListRoutingPolicies(routingAdminCtx(), &inferv1.ListRoutingPoliciesRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeUnauthorized, apierrors.CodeOf(err))

	// Missing session user: fail closed.
	svc.SetRoleGuard(fakeRoleGuard{allowed: true})
	svc.SetSessionUserResolver(nil)
	_, err = svc.GetRoutingPolicy(routingAdminCtx(), &inferv1.GetRoutingPolicyRequest{ModelId: modelID})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeUnauthorized, apierrors.CodeOf(err))

	// Denied role: 10036.
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "admin-1"})
	svc.SetRoleGuard(fakeRoleGuard{allowed: false})
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, true, []string{readyID}, 1, nil, 0))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// Empty session user id: fail closed.
	svc.SetRoleGuard(fakeRoleGuard{allowed: true})
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: ""})
	_, err = svc.ListRoutingTargetHealth(routingAdminCtx(), &inferv1.ListRoutingTargetHealthRequest{ModelId: modelID})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeUnauthorized, apierrors.CodeOf(err))

	// Restored admin session: the list succeeds.
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "admin-1"})
	_, err = svc.ListRoutingPolicies(routingAdminCtx(), &inferv1.ListRoutingPoliciesRequest{})
	require.NoError(t, err)
	_ = db
}

// TestRoutingPolicyValidationMatrix walks the §5.2 validation matrix:
// unknown model, empty version, out-of-range attempts, duplicate and
// unknown retry categories, wrong-version/terminated targets, >10
// targets, attempts>1 without categories, and enabling with no ready
// target all yield 10312; only the genuine missing model yields 10101.
func TestRoutingPolicyValidationMatrix(t *testing.T) {
	svc, _, _ := newRoutingTestService(t)
	modelID := seedRoutingModel(t, svc, "qwen-3b", "v1")
	readyID := seedRoutingService(t, svc, modelID, "v1", StateRunning, []string{"http://10.0.0.1:8000"})
	seedRoutingService(t, svc, modelID, "v2", StateRunning, []string{"http://10.0.0.2:8000"}) // wrong version
	terminatedID := seedRoutingService(t, svc, modelID, "v1", StateTerminated, nil)

	// Unknown model: 10101.
	_, err := svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate("00000000-0000-0000-0000-000000000001", true, nil, 1, nil, 0))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeModelNotFound, apierrors.CodeOf(err))

	// Empty version: 10312.
	req := routingUpdate(modelID, false, nil, 1, nil, 0)
	req.ModelVersion = ""
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), req)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingPolicyInvalid, apierrors.CodeOf(err))

	// Attempts out of range: 10312.
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, false, nil, 0, nil, 0))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingPolicyInvalid, apierrors.CodeOf(err))
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, false, nil, 4, nil, 0))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingPolicyInvalid, apierrors.CodeOf(err))

	// Duplicate target: 10312.
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, false, []string{readyID, readyID}, 1, nil, 0))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingPolicyInvalid, apierrors.CodeOf(err))

	// Wrong-version target: 10312.
	wrongVersion := seedRoutingService(t, svc, modelID, "v2", StateRunning, []string{"http://10.0.0.3:8000"})
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, false, []string{wrongVersion}, 1, nil, 0))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingPolicyInvalid, apierrors.CodeOf(err))

	// Terminated target: 10312.
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, false, []string{terminatedID}, 1, nil, 0))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingPolicyInvalid, apierrors.CodeOf(err))

	// Unknown target: 10312.
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, false, []string{"no-such-service"}, 1, nil, 0))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingPolicyInvalid, apierrors.CodeOf(err))

	// Attempts > 1 without retry categories: 10312.
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, false, []string{readyID}, 2, nil, 0))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingPolicyInvalid, apierrors.CodeOf(err))

	// Duplicate retry category: 10312.
	dup := []inferv1.RetryCategory{inferv1.RetryCategory_RETRY_CATEGORY_HTTP_429, inferv1.RetryCategory_RETRY_CATEGORY_HTTP_429}
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, false, []string{readyID}, 2, dup, 0))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingPolicyInvalid, apierrors.CodeOf(err))

	// Unspecified retry category: 10312.
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, false, []string{readyID}, 2,
		[]inferv1.RetryCategory{inferv1.RetryCategory_RETRY_CATEGORY_UNSPECIFIED}, 0))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingPolicyInvalid, apierrors.CodeOf(err))

	// Enabling with no ready target: 10312.
	pendingID := seedRoutingService(t, svc, modelID, "v1", StatePending, nil)
	otherModel := seedRoutingModel(t, svc, "llama-8b", "v1")
	seedRoutingService(t, svc, otherModel, "v1", StatePending, nil)
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(otherModel, true, []string{pendingID}, 1, nil, 0))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingPolicyInvalid, apierrors.CodeOf(err))

        // Enabling with only non-ready selected targets is rejected even
        // when other eligible services of the same model are ready: the
        // policy routes only to its ordered target list (feature #45,
        // §5.2; e2e AC4).
        _, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, true, []string{pendingID}, 1, nil, 0))
        require.Error(t, err)
        assert.Equal(t, apierrors.CodeRoutingPolicyInvalid, apierrors.CodeOf(err))

	// More than 10 targets: 10312.
	ids := make([]string, 0, MaxRoutingTargets+1)
	for i := 0; i <= MaxRoutingTargets; i++ {
		ids = append(ids, seedRoutingService(t, svc, modelID, "v1", StateRunning, []string{"http://10.0.0.4:8000"}))
	}
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, false, ids, 1, nil, 0))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingPolicyInvalid, apierrors.CodeOf(err))

	// Exactly 10 distinct ready targets with a disabled policy: valid.
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, false, ids[:MaxRoutingTargets], 1, nil, 0))
	require.NoError(t, err)
}

// TestRoutingPolicyRevisionConflict verifies a stale expected_revision
// yields 10313 and never overwrites the newer revision (feature #45,
// §5.2).
func TestRoutingPolicyRevisionConflict(t *testing.T) {
	svc, _, _ := newRoutingTestService(t)
	modelID := seedRoutingModel(t, svc, "qwen-3b", "v1")
	readyID := seedRoutingService(t, svc, modelID, "v1", StateRunning, []string{"http://10.0.0.1:8000"})

	// First write: revision 1.
	resp, err := svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, true, []string{readyID}, 1, nil, 0))
	require.NoError(t, err)
	assert.Equal(t, int64(1), resp.Policy.Revision)

	// Stale expected revision: 10313.
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, false, nil, 1, nil, 0))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingPolicyRevisionConflict, apierrors.CodeOf(err))

	// The stored policy is unchanged.
	stored, err := svc.GetRoutingPolicy(routingAdminCtx(), &inferv1.GetRoutingPolicyRequest{ModelId: modelID})
	require.NoError(t, err)
	assert.True(t, stored.Policy.Enabled)
	assert.Equal(t, int64(1), stored.Policy.Revision)

	// Matching revision: succeeds and bumps to 2.
	resp, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, true, []string{readyID}, 2,
		[]inferv1.RetryCategory{inferv1.RetryCategory_RETRY_CATEGORY_HTTP_429}, 1))
	require.NoError(t, err)
	assert.Equal(t, int64(2), resp.Policy.Revision)
	assert.Equal(t, int32(2), resp.Policy.MaxAttempts)
}

// TestRoutingPolicyAtomicity verifies the update writes the active row,
// the immutable revision row and the outbox row in one transaction
// (feature #45, AD5): a failing audit hook rolls all three back.
func TestRoutingPolicyAtomicity(t *testing.T) {
	svc, _, db := newRoutingTestService(t)
	modelID := seedRoutingModel(t, svc, "qwen-3b", "v1")
	readyID := seedRoutingService(t, svc, modelID, "v1", StateRunning, []string{"http://10.0.0.1:8000"})

	repo, err := svc.routingPolicyRepository()
	require.NoError(t, err)

	// A failing audit hook aborts the whole unit of work.
	_, _, err = repo.CompareAndSwapUpdate(context.Background(), RoutingPolicyChange{
		ModelID: modelID, ModelVersion: "v1", Enabled: true,
		ServiceIDs: []string{readyID}, MaxAttempts: 1, ExpectedRev: 0,
		Actor: "admin-1", Summary: "policy created",
	}, func(context.Context) error { return assert.AnError })
	require.Error(t, err)

	// Nothing persisted: no active row, no revision, no outbox.
	row, err := repo.GetCurrent(context.Background(), modelID)
	require.NoError(t, err)
	assert.Nil(t, row)
	var revCount, outboxCount int64
	require.NoError(t, db.Model(&RoutingPolicyRevision{}).Where("model_id = ?", modelID).Count(&revCount).Error)
	require.NoError(t, db.Model(&RoutingPolicyOutbox{}).Where("model_id = ?", modelID).Count(&outboxCount).Error)
	assert.Zero(t, revCount)
	assert.Zero(t, outboxCount)

	// A succeeding audit hook persists all three atomically.
	stored, revRow, err := repo.CompareAndSwapUpdate(context.Background(), RoutingPolicyChange{
		ModelID: modelID, ModelVersion: "v1", Enabled: true,
		ServiceIDs: []string{readyID}, MaxAttempts: 1, ExpectedRev: 0,
		Actor: "admin-1", Summary: "policy created",
	}, func(context.Context) error { return nil })
	require.NoError(t, err)
	assert.Equal(t, int64(1), stored.Revision)
	assert.Equal(t, int64(1), revRow.Revision)
	require.NoError(t, db.Model(&RoutingPolicyOutbox{}).Where("model_id = ?", modelID).Count(&outboxCount).Error)
	assert.Equal(t, int64(1), outboxCount)
}

// TestRoutingPolicyListAndGet verifies the list projection states
// (default/enabled/unavailable) and the get default projection
// (feature #45, §5).
func TestRoutingPolicyListAndGet(t *testing.T) {
	svc, _, _ := newRoutingTestService(t)
	modelA := seedRoutingModel(t, svc, "qwen-3b", "v1")
	modelB := seedRoutingModel(t, svc, "llama-8b", "v1")
	modelC := seedRoutingModel(t, svc, "mistral-7b", "v1")
	readyA := seedRoutingService(t, svc, modelA, "v1", StateRunning, []string{"http://10.0.0.1:8000"})
	seedRoutingService(t, svc, modelB, "v1", StatePending, nil) // not ready
	_ = modelC

	// Enable A (ready target exists).
	_, err := svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelA, true, []string{readyA}, 1, nil, 0))
	require.NoError(t, err)
	// Enable B with no ready target is rejected; create a disabled
	// policy instead.
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelB, false, nil, 1, nil, 0))
	require.NoError(t, err)

	list, err := svc.ListRoutingPolicies(routingAdminCtx(), &inferv1.ListRoutingPoliciesRequest{})
	require.NoError(t, err)
	byModel := map[string]*inferv1.RoutingPolicySummary{}
	for _, p := range list.Policies {
		byModel[p.ModelId] = p
	}
	require.Len(t, list.Policies, 3)
	// A: enabled with a ready target.
	assert.Equal(t, RoutingPolicyStateEnabled, byModel[modelA].PolicyState)
	assert.True(t, byModel[modelA].Enabled)
	assert.Equal(t, int32(1), byModel[modelA].ReadyCount)
	// B: disabled policy → default routing.
	assert.Equal(t, RoutingPolicyStateDefault, byModel[modelB].PolicyState)
	// C: no policy → default routing.
	assert.Equal(t, RoutingPolicyStateDefault, byModel[modelC].PolicyState)

	// Get on a model without a policy projects revision 0, disabled.
	got, err := svc.GetRoutingPolicy(routingAdminCtx(), &inferv1.GetRoutingPolicyRequest{ModelId: modelC})
	require.NoError(t, err)
	assert.Equal(t, int64(0), got.Policy.Revision)
	assert.False(t, got.Policy.Enabled)
	assert.Equal(t, "v1", got.Policy.ModelVersion)

	// Get on an unknown model: 10101.
	_, err = svc.GetRoutingPolicy(routingAdminCtx(), &inferv1.GetRoutingPolicyRequest{ModelId: "00000000-0000-0000-0000-000000000002"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeModelNotFound, apierrors.CodeOf(err))
}

// TestRoutingPolicyUnavailableState verifies a policy whose targets all
// stopped is reported UNAVAILABLE, not ENABLED (feature #45, §5).
func TestRoutingPolicyUnavailableState(t *testing.T) {
	svc, _, db := newRoutingTestService(t)
	modelID := seedRoutingModel(t, svc, "qwen-3b", "v1")
	readyID := seedRoutingService(t, svc, modelID, "v1", StateRunning, []string{"http://10.0.0.1:8000"})

	_, err := svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, true, []string{readyID}, 1, nil, 0))
	require.NoError(t, err)

	// The target stops being ready.
	require.NoError(t, db.Model(&InferenceService{}).Where("id = ?", readyID).
		Updates(map[string]any{"state": StateFailed, "endpoints": "[]"}).Error)

	list, err := svc.ListRoutingPolicies(routingAdminCtx(), &inferv1.ListRoutingPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, list.Policies, 1)
	assert.Equal(t, RoutingPolicyStateUnavailable, list.Policies[0].PolicyState)
	assert.Equal(t, int32(0), list.Policies[0].ReadyCount)
}

// TestRoutingTargetHealthProjection verifies the health projection is
// masked: no endpoint URLs, only readiness flags (feature #45, AD7).
func TestRoutingTargetHealthProjection(t *testing.T) {
	svc, _, _ := newRoutingTestService(t)
	modelID := seedRoutingModel(t, svc, "qwen-3b", "v1")
	readyID := seedRoutingService(t, svc, modelID, "v1", StateRunning, []string{"http://10.0.0.1:8000"})
	pendingID := seedRoutingService(t, svc, modelID, "v1", StatePending, nil)

	resp, err := svc.ListRoutingTargetHealth(routingAdminCtx(), &inferv1.ListRoutingTargetHealthRequest{ModelId: modelID})
	require.NoError(t, err)
	require.Len(t, resp.Targets, 2)
	byID := map[string]*inferv1.RoutingTargetHealth{}
	for _, target := range resp.Targets {
		byID[target.ServiceId] = target
	}
	assert.True(t, byID[readyID].EndpointReady)
	assert.Equal(t, int32(1), byID[readyID].ReadyReplicas)
	assert.False(t, byID[pendingID].EndpointReady)
	// Masked: no endpoint URL is present anywhere in the response.
	for _, target := range resp.Targets {
		assert.NotContains(t, target.String(), "10.0.0.1")
	}
}

// TestRoutingPolicyRevisions verifies the immutable revision history
// with before/after snapshots (feature #45, §5).
func TestRoutingPolicyRevisions(t *testing.T) {
	svc, _, _ := newRoutingTestService(t)
	modelID := seedRoutingModel(t, svc, "qwen-3b", "v1")
	readyID := seedRoutingService(t, svc, modelID, "v1", StateRunning, []string{"http://10.0.0.1:8000"})

	_, err := svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, true, []string{readyID}, 1, nil, 0))
	require.NoError(t, err)
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, true, []string{readyID}, 2,
		[]inferv1.RetryCategory{inferv1.RetryCategory_RETRY_CATEGORY_HTTP_429}, 1))
	require.NoError(t, err)
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, false, nil, 1, nil, 2))
	require.NoError(t, err)

	resp, err := svc.ListRoutingPolicyRevisions(routingAdminCtx(), &inferv1.ListRoutingPolicyRevisionsRequest{ModelId: modelID})
	require.NoError(t, err)
	require.Len(t, resp.Revisions, 3)
	// Newest first.
	assert.Equal(t, int64(3), resp.Revisions[0].Revision)
	assert.Equal(t, int64(1), resp.Revisions[2].Revision)
	// Before/after snapshots carry the changed values.
	assert.False(t, resp.Revisions[0].After.Enabled)
	assert.True(t, resp.Revisions[0].Before.Enabled)
	assert.Equal(t, int32(2), resp.Revisions[1].After.MaxAttempts)
	assert.Equal(t, int32(1), resp.Revisions[1].Before.MaxAttempts)
	// The actor and summary are recorded.
	assert.Equal(t, "admin-1", resp.Revisions[0].Actor)
	assert.NotEmpty(t, resp.Revisions[0].Summary)
}

// TestRoutingPolicyPublisherRetryAndIdempotency verifies the outbox
// publisher marks rows published after a successful MQ publish, retries
// after a failure and never loses a committed revision (feature #45,
// AD5).
func TestRoutingPolicyPublisherRetryAndIdempotency(t *testing.T) {
	svc, client, db := newRoutingTestService(t)
	modelID := seedRoutingModel(t, svc, "qwen-3b", "v1")
	readyID := seedRoutingService(t, svc, modelID, "v1", StateRunning, []string{"http://10.0.0.1:8000"})

	_, err := svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, true, []string{readyID}, 1, nil, 0))
	require.NoError(t, err)

	repo, err := svc.routingPolicyRepository()
	require.NoError(t, err)
	publisher := NewRoutingPolicyPublisher(repo, client, 0)

	// First attempt: MQ publish fails → the row stays pending with the
	// attempt recorded.
	client.failNext = true
	publisher.RunOnce(context.Background())
	client.failNext = false
	var outbox RoutingPolicyOutbox
	require.NoError(t, db.Where("model_id = ?", modelID).First(&outbox).Error)
	assert.True(t, outbox.PublishedAt == nil)
	assert.Positive(t, outbox.Attempts)
	assert.NotEmpty(t, outbox.LastError)

	// Second attempt: publish succeeds → the row is marked published.
	publisher.RunOnce(context.Background())
	require.NoError(t, db.Where("model_id = ?", modelID).First(&outbox).Error)
	assert.NotNil(t, outbox.PublishedAt)

	// The snapshot reached the dedicated subject exactly once.
	require.Len(t, client.published, 1)
	assert.Contains(t, string(client.published[0].Body), modelID)
	assert.Equal(t, "1", client.published[0].Headers["revision"])

	// Publication state is reflected on the policy projection.
	got, err := svc.GetRoutingPolicy(routingAdminCtx(), &inferv1.GetRoutingPolicyRequest{ModelId: modelID})
	require.NoError(t, err)
	assert.Equal(t, RoutingPublicationPublished, got.Policy.PublicationState)
}

// TestRoutingPolicyPublicationPendingState verifies a committed but
// unpublished revision reports publication_state pending (feature #45,
// §5).
func TestRoutingPolicyPublicationPendingState(t *testing.T) {
	svc, _, _ := newRoutingTestService(t)
	modelID := seedRoutingModel(t, svc, "qwen-3b", "v1")
	readyID := seedRoutingService(t, svc, modelID, "v1", StateRunning, []string{"http://10.0.0.1:8000"})

	resp, err := svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, true, []string{readyID}, 1, nil, 0))
	require.NoError(t, err)
	assert.Equal(t, RoutingPublicationPending, resp.Policy.PublicationState)
}

// TestRoutingPolicyListFilters verifies the search, state and
// accelerator filters and the sort options (feature #45, §5).
func TestRoutingPolicyListFilters(t *testing.T) {
	svc, _, _ := newRoutingTestService(t)
	modelA := seedRoutingModel(t, svc, "qwen-3b", "v1")
	modelB := seedRoutingModel(t, svc, "llama-8b", "v1")
	readyA := seedRoutingService(t, svc, modelA, "v1", StateRunning, []string{"http://10.0.0.1:8000"})
	seedRoutingService(t, svc, modelB, "v1", StatePending, nil)

	_, err := svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelA, true, []string{readyA}, 1, nil, 0))
	require.NoError(t, err)

	// Search by model name.
	list, err := svc.ListRoutingPolicies(routingAdminCtx(), &inferv1.ListRoutingPoliciesRequest{Search: "qwen"})
	require.NoError(t, err)
	require.Len(t, list.Policies, 1)
	assert.Equal(t, modelA, list.Policies[0].ModelId)

	// State filter: enabled.
	list, err = svc.ListRoutingPolicies(routingAdminCtx(), &inferv1.ListRoutingPoliciesRequest{State: RoutingPolicyStateEnabled})
	require.NoError(t, err)
	require.Len(t, list.Policies, 1)
	assert.Equal(t, modelA, list.Policies[0].ModelId)

	// State filter: default (no policy).
	list, err = svc.ListRoutingPolicies(routingAdminCtx(), &inferv1.ListRoutingPoliciesRequest{State: RoutingPolicyStateDefault})
	require.NoError(t, err)
	require.Len(t, list.Policies, 1)
	assert.Equal(t, modelB, list.Policies[0].ModelId)

	// Accelerator filter.
	list, err = svc.ListRoutingPolicies(routingAdminCtx(), &inferv1.ListRoutingPoliciesRequest{Accelerator: "nvidia"})
	require.NoError(t, err)
	assert.Len(t, list.Policies, 2)

	// Sort by ready count descending.
	list, err = svc.ListRoutingPolicies(routingAdminCtx(), &inferv1.ListRoutingPoliciesRequest{Sort: "-ready"})
	require.NoError(t, err)
	require.Len(t, list.Policies, 2)
	assert.Equal(t, modelA, list.Policies[0].ModelId)
}

// TestRoutingPolicyPageBounds verifies pagination normalization.
func TestRoutingPolicyPageBounds(t *testing.T) {
	defOffset, defLimit := routingPageBounds(nil)
	assert.Equal(t, 0, defOffset)
	assert.Equal(t, 20, defLimit)
	offset, limit := routingPageBounds(&commonv1.PageRequest{Offset: 5, Limit: 200})
	assert.Equal(t, 5, offset)
	assert.Equal(t, 100, limit)
}

// TestRoutingPolicyHelpers verifies the enum and snapshot helpers.
func TestRoutingPolicyHelpers(t *testing.T) {
	// Retry category round-trip.
	for _, c := range []inferv1.RetryCategory{
		inferv1.RetryCategory_RETRY_CATEGORY_CONNECT_TIMEOUT,
		inferv1.RetryCategory_RETRY_CATEGORY_HTTP_429,
		inferv1.RetryCategory_RETRY_CATEGORY_HTTP_5XX,
	} {
		value, ok := retryCategoryToString(c)
		require.True(t, ok)
		assert.Equal(t, c, stringToRetryCategory(value))
	}
	_, ok := retryCategoryToString(inferv1.RetryCategory_RETRY_CATEGORY_UNSPECIFIED)
	assert.False(t, ok)
	assert.Equal(t, inferv1.RetryCategory_RETRY_CATEGORY_UNSPECIFIED, stringToRetryCategory("bogus"))

	// Snapshot decoding tolerates empty input.
	snap := routingRevisionSnapshotToProto("")
	assert.NotNil(t, snap)
	assert.Empty(t, snap.ServiceIds)

	// Change summary for a fresh policy.
	assert.Equal(t, "policy created", routingChangeSummary(nil, routingUpdate("m", true, nil, 1, nil, 0), "v1"))
}

// TestRoutingPolicyRevisionHistoryCap verifies only the newest 20
// revisions are retained (feature #45, §4.1).
func TestRoutingPolicyRevisionHistoryCap(t *testing.T) {
	svc, _, db := newRoutingTestService(t)
	modelID := seedRoutingModel(t, svc, "qwen-3b", "v1")
	readyID := seedRoutingService(t, svc, modelID, "v1", StateRunning, []string{"http://10.0.0.1:8000"})

	var lastRev int64
	for i := 0; i < RoutingRevisionHistory+2; i++ {
		enabled := i%2 == 0
		_, err := svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, enabled, []string{readyID}, 1, nil, lastRev))
		require.NoError(t, err)
		lastRev++
	}

	resp, err := svc.ListRoutingPolicyRevisions(routingAdminCtx(), &inferv1.ListRoutingPolicyRevisionsRequest{ModelId: modelID})
	require.NoError(t, err)
	assert.Len(t, resp.Revisions, RoutingRevisionHistory)
	assert.Equal(t, int64(RoutingRevisionHistory+2), resp.Revisions[0].Revision)

	var count int64
	require.NoError(t, db.Model(&RoutingPolicyRevision{}).Where("model_id = ?", modelID).Count(&count).Error)
	assert.Equal(t, int64(RoutingRevisionHistory), count)
}

// TestRoutingPolicyAuditEvent verifies the update emits an audit event
// through the transactional recorder seam (feature #45, §13.1).
func TestRoutingPolicyAuditEvent(t *testing.T) {
	svc, _, _ := newRoutingTestService(t)
	modelID := seedRoutingModel(t, svc, "qwen-3b", "v1")
	readyID := seedRoutingService(t, svc, modelID, "v1", StateRunning, []string{"http://10.0.0.1:8000"})

	events := []*audit.AuditEvent{}
	spy := &auditRecorderSpy{events: &events}
	svc.SetAuditRecorder(spy)

	_, err := svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, true, []string{readyID}, 1, nil, 0))
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "routing_policy.updated", events[0].Action)
	assert.Equal(t, "routing_policy", events[0].ResourceType)
	assert.Equal(t, modelID, events[0].ResourceID)
	assert.Equal(t, "admin-1", events[0].ActorUserID)
}

// auditRecorderSpy captures RecordInTx calls.
type auditRecorderSpy struct {
	events *[]*audit.AuditEvent
}

func (s *auditRecorderSpy) Record(context.Context, *audit.AuditEvent) {}
func (s *auditRecorderSpy) RecordInTx(_ context.Context, ev *audit.AuditEvent) error {
	*s.events = append(*s.events, ev)
	return nil
}

// mq.Client compile guard for the recording client used above.
var _ mq.Client = (*recordingClient)(nil)

// TestRoutingPolicyPublisherRunLoop verifies the runner loop publishes
// pending rows and stops on context cancellation (feature #45, §13.1).
func TestRoutingPolicyPublisherRunLoop(t *testing.T) {
	svc, client, _ := newRoutingTestService(t)
	modelID := seedRoutingModel(t, svc, "qwen-3b", "v1")
	readyID := seedRoutingService(t, svc, modelID, "v1", StateRunning, []string{"http://10.0.0.1:8000"})
	_, err := svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate(modelID, true, []string{readyID}, 1, nil, 0))
	require.NoError(t, err)

	repo, err := svc.routingPolicyRepository()
	require.NoError(t, err)
	publisher := NewRoutingPolicyPublisher(repo, client, 10*time.Millisecond)
	assert.Equal(t, "infer-routing-policy-publisher", publisher.RunnerName())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()
	// The first tick publishes the pending row.
	require.Eventually(t, func() bool {
		return len(client.published) == 1
	}, 2*time.Second, 10*time.Millisecond, "publisher should publish the pending snapshot")
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("publisher did not stop on cancellation")
	}
}

// TestRoutingPolicyClusterNames verifies the masked cluster-name
// lookup (feature #45, AD7).
func TestRoutingPolicyClusterNames(t *testing.T) {
	svc, _, db := newRoutingTestService(t)
	require.NoError(t, db.AutoMigrate(&clusterNameRow{}))
	require.NoError(t, db.Create(&clusterNameRow{ID: "cluster-1", Name: "gpu-east"}).Error)

	repo, err := svc.routingPolicyRepository()
	require.NoError(t, err)
	names, err := repo.ClusterNames(context.Background(), []string{"cluster-1", "cluster-2"})
	require.NoError(t, err)
	assert.Equal(t, "gpu-east", names["cluster-1"])
	assert.Empty(t, names["cluster-2"])

	// Empty input: no query, empty map.
	names, err = repo.ClusterNames(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, names)
}

// TestRoutingPolicyLazyRepositoryResolution verifies the lazy resolver
// falls back to the components path (feature #45, §13.1).
func TestRoutingPolicyLazyRepositoryResolution(t *testing.T) {
	db := newInferTestDB(t)
	require.NoError(t, MigrateSchemaForFVT(db))
	svc := New(&fakeComponents{db: db, mqType: "client"})
	// The lazy resolver resolves through the components.
	repo, err := svc.routingPolicyRepository()
	require.NoError(t, err)
	require.NotNil(t, repo)
	// A second call returns the cached instance.
	repo2, err := svc.routingPolicyRepository()
	require.NoError(t, err)
	assert.Same(t, repo, repo2)
}

// TestRoutingPolicyListSummariesInvalidInputs verifies the invalid
// sort and state filters yield 10312 (feature #45, §5).
func TestRoutingPolicyListSummariesInvalidInputs(t *testing.T) {
	svc, _, _ := newRoutingTestService(t)
	seedRoutingModel(t, svc, "qwen-3b", "v1")

	_, err := svc.ListRoutingPolicies(routingAdminCtx(), &inferv1.ListRoutingPoliciesRequest{Sort: "bogus"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingPolicyInvalid, apierrors.CodeOf(err))

	_, err = svc.ListRoutingPolicies(routingAdminCtx(), &inferv1.ListRoutingPoliciesRequest{State: "bogus"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingPolicyInvalid, apierrors.CodeOf(err))
}

// TestRoutingPolicyRevisionsInvalidModel verifies the revisions and
// health RPCs reject a malformed model id with 10101 (feature #45,
// §5).
func TestRoutingPolicyRevisionsInvalidModel(t *testing.T) {
	svc, _, _ := newRoutingTestService(t)
	_, err := svc.ListRoutingPolicyRevisions(routingAdminCtx(), &inferv1.ListRoutingPolicyRevisionsRequest{ModelId: "not-a-uuid"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeModelNotFound, apierrors.CodeOf(err))
	_, err = svc.ListRoutingTargetHealth(routingAdminCtx(), &inferv1.ListRoutingTargetHealthRequest{ModelId: "not-a-uuid"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeModelNotFound, apierrors.CodeOf(err))
	_, err = svc.GetRoutingPolicy(routingAdminCtx(), &inferv1.GetRoutingPolicyRequest{ModelId: "not-a-uuid"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeModelNotFound, apierrors.CodeOf(err))
	_, err = svc.UpdateRoutingPolicy(routingAdminCtx(), routingUpdate("not-a-uuid", false, nil, 1, nil, 0))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeModelNotFound, apierrors.CodeOf(err))
}
