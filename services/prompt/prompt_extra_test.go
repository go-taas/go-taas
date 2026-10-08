package prompt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	promptv1 "github.com/go-taas/go-taas/proto/taas/prompt/v1"
)

// fakeSessionOrgResolver returns a fixed org.
type fakeSessionOrgResolver struct {
	org string
}

func (f *fakeSessionOrgResolver) SessionActiveOrg(_ context.Context) (string, error) {
	return f.org, nil
}

// errorSessionUserResolver returns an error.
type errorSessionUserResolver struct{}

func (e *errorSessionUserResolver) SessionUserID(_ context.Context) (string, error) {
	return "", apierrors.New(apierrors.CodeInternal)
}

// TestServiceResolveOrgNoHeader covers the missing-org error path.
func TestServiceResolveOrgNoHeader(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	ctx := context.Background()

	_, err := svc.ListPrompts(ctx, &promptv1.ListPromptsRequest{})
	require.Error(t, err)
}

// TestServiceRecordAuditNil covers the nil audit recorder path.
func TestServiceRecordAuditNil(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	// auditRecorder is nil; recordAudit should be a no-op.
	svc.recordAudit(context.Background(), "prompt.created", "org-a", "prompt-1")
}

// TestServiceRequireAdminRoleError covers the session-user error path.
func TestServiceRequireAdminRoleError(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	svc.sessionUserResolver = &errorSessionUserResolver{}
	svc.roleGuard = &fakeRoleGuard{}
	ctx := orgCtx("org-a")

	_, err := svc.AdminListPrompts(ctx, &promptv1.AdminListPromptsRequest{})
	require.Error(t, err)
}

// TestServiceResolveOrgSession covers the session-org resolver path.
func TestServiceResolveOrgSession(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	svc.sessionOrgResolver = &fakeSessionOrgResolver{org: "org-session"}
	ctx := context.Background()

	resp, err := svc.ListPrompts(ctx, &promptv1.ListPromptsRequest{})
	require.NoError(t, err)
	assert.NotNil(t, resp)
}

// TestServiceGetPromptError covers the GetPrompt not-found path.
func TestServiceGetPromptError(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	ctx := orgCtx("org-a")

	_, err := svc.GetPrompt(ctx, &promptv1.GetPromptRequest{PromptId: "00000000-0000-0000-0000-000000000000"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptNotFound, apierrors.CodeOf(err))
}