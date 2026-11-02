// Per-user quota full-verification suite: the personal tier of the
// three-level quota model (organization, project, user). It exercises
// the admin CRUD, the role gate, the effective-value combination the
// user surface shows, and the migration that folds the retired
// per-project member limits into the personal tier.
package fvt

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/services/tenancy"
)

// TestFVTUserQuotaAdminLifecycle covers the user-quota CRUD and gate.
func TestFVTUserQuotaAdminLifecycle(t *testing.T) {
	env := newOwnershipFvtEnv(t)

	// The list is roster-driven: both members appear with unlimited
	// limits before any budget is set.
	code, body := env.call(t, "33333333-3333-4333-8333-333333333333", http.MethodGet, "/api/v1/admin/tenancy/user-quotas", nil)
	require.Equal(t, http.StatusOK, code, "list: %v", body)
	quotas := body["quotas"].([]any)
	require.Len(t, quotas, 3, "the org roster drives the list")
	aliceRow := findQuotaRow(t, quotas, "11111111-1111-4111-8111-111111111111")
	assert.EqualValues(t, 0, numberOf(t, aliceRow["maxConcurrentRequests"]))
	assert.Equal(t, "member", aliceRow["role"])

	// A plain member cannot read or write the admin surface.
	_, body = env.call(t, "11111111-1111-4111-8111-111111111111", http.MethodGet, "/api/v1/admin/tenancy/user-quotas", nil)
	assert.EqualValues(t, 10036, body["code"], "member list: %v", body)

	// Set the three resource dimensions plus the personal monthly spend cap.
	code, body = env.call(t, "33333333-3333-4333-8333-333333333333", http.MethodPatch, "/api/v1/admin/tenancy/user-quotas/11111111-1111-4111-8111-111111111111", map[string]any{
		"maxConcurrentRequests":     5,
		"maxBatchJobsPerDay":        7,
		"maxOutputTokensPerRequest": 1024,
		"monthlySpendLimitCents":    2500,
	})
	require.Equal(t, http.StatusOK, code, "update: %v", body)
	quota := body["quota"].(map[string]any)
	assert.EqualValues(t, 5, numberOf(t, quota["maxConcurrentRequests"]))
	assert.EqualValues(t, 2500, numberOf(t, quota["monthlySpendLimitCents"]))
	assert.Equal(t, "33333333-3333-4333-8333-333333333333", quota["updatedBy"])
	assert.NotNil(t, body["effectiveQuota"])

	// Get returns the stored values.
	code, body = env.call(t, "33333333-3333-4333-8333-333333333333", http.MethodGet, "/api/v1/admin/tenancy/user-quotas/11111111-1111-4111-8111-111111111111", nil)
	require.Equal(t, http.StatusOK, code, "get: %v", body)
	assert.EqualValues(t, 1024, numberOf(t, body["quota"].(map[string]any)["maxOutputTokensPerRequest"]))

	// Out-of-range values return 13305.
	_, body = env.call(t, "33333333-3333-4333-8333-333333333333", http.MethodPatch, "/api/v1/admin/tenancy/user-quotas/11111111-1111-4111-8111-111111111111", map[string]any{
		"maxConcurrentRequests": 10001,
	})
	assert.EqualValues(t, 13305, body["code"], "invalid: %v", body)
	_, body = env.call(t, "33333333-3333-4333-8333-333333333333", http.MethodPatch, "/api/v1/admin/tenancy/user-quotas/11111111-1111-4111-8111-111111111111", map[string]any{
		"monthlySpendLimitCents": -1,
	})
	assert.EqualValues(t, 13305, body["code"], "negative spend: %v", body)

	// A user outside the organization is rejected.
	_, body = env.call(t, "33333333-3333-4333-8333-333333333333", http.MethodPatch, "/api/v1/admin/tenancy/user-quotas/stranger", map[string]any{
		"maxConcurrentRequests": 1,
	})
	assert.EqualValues(t, 10003, body["code"], "unknown user: %v", body)
}

// TestFVTUserQuotaEffectiveValue covers the three-level combination:
// the effective value is the minimum of the non-zero tiers, and 0 is
// unlimited rather than the smallest value.
func TestFVTUserQuotaEffectiveValue(t *testing.T) {
	env := newOwnershipFvtEnv(t)
	ctx := env.sessionCtx("33333333-3333-4333-8333-333333333333")

	// Organization 10, project 20, person 5 -> effective 5.
	_, err := env.ten.UpdateOrganizationQuota(ctx, &tenancyv1.UpdateOrganizationQuotaRequest{
		OrganizationId:        "org-fvt",
		MaxConcurrentRequests: ptrInt64(10),
		MaxOutputTokensPerRequest: ptrInt64(8192),
	})
	require.NoError(t, err)
	_, err = env.ten.UpdateProjectQuota(ctx, &tenancyv1.UpdateProjectQuotaRequest{
		ProjectId:             "proj-a",
		MaxConcurrentRequests: ptrInt64(20),
	})
	require.NoError(t, err)
	_, err = env.ten.UpdateUserQuota(ctx, &tenancyv1.UpdateUserQuotaRequest{
		UserId:                "11111111-1111-4111-8111-111111111111",
		OrganizationId:        "org-fvt",
		MaxConcurrentRequests: ptrInt64(5),
	})
	require.NoError(t, err)

	effective, err := env.ten.EffectiveQuota(ctx, "org-fvt", "proj-a", "11111111-1111-4111-8111-111111111111")
	require.NoError(t, err)
	assert.EqualValues(t, 5, effective.MaxConcurrentRequests)
	assert.EqualValues(t, 8192, effective.MaxOutputTokensPerRequest)

	// A personal 0 means "inherit": the organization value wins.
	_, err = env.ten.UpdateUserQuota(ctx, &tenancyv1.UpdateUserQuotaRequest{
		UserId:                "11111111-1111-4111-8111-111111111111",
		OrganizationId:        "org-fvt",
		MaxConcurrentRequests: ptrInt64(0),
	})
	require.NoError(t, err)
	effective, err = env.ten.EffectiveQuota(ctx, "org-fvt", "proj-a", "11111111-1111-4111-8111-111111111111")
	require.NoError(t, err)
	assert.EqualValues(t, 10, effective.MaxConcurrentRequests)

	// An empty project/user scope skips those tiers.
	effective, err = env.ten.EffectiveQuota(ctx, "org-fvt", "", "")
	require.NoError(t, err)
	assert.EqualValues(t, 10, effective.MaxConcurrentRequests)
}

// TestFVTGetMyQuotaShowsThreeTiers covers the user-surface projection.
func TestFVTGetMyQuotaShowsThreeTiers(t *testing.T) {
	env := newOwnershipFvtEnv(t)
	ctx := env.sessionCtx("33333333-3333-4333-8333-333333333333")

	_, err := env.ten.UpdateOrganizationQuota(ctx, &tenancyv1.UpdateOrganizationQuotaRequest{
		OrganizationId:        "org-fvt",
		MaxConcurrentRequests: ptrInt64(10),
	})
	require.NoError(t, err)
	_, err = env.ten.UpdateUserQuota(ctx, &tenancyv1.UpdateUserQuotaRequest{
		UserId:                "11111111-1111-4111-8111-111111111111",
		OrganizationId:        "org-fvt",
		MaxConcurrentRequests: ptrInt64(4),
	})
	require.NoError(t, err)

	// The user surface resolves the caller from the session.
	code, body := env.call(t, "11111111-1111-4111-8111-111111111111", http.MethodGet, "/api/v1/quotas", nil)
	require.Equal(t, http.StatusOK, code, "my quota: %v", body)
	assert.EqualValues(t, 10, numberOf(t, body["quota"].(map[string]any)["maxConcurrentRequests"]))
	assert.EqualValues(t, 4, numberOf(t, body["personalQuota"].(map[string]any)["maxConcurrentRequests"]))
	assert.EqualValues(t, 4, numberOf(t, body["effectiveQuota"].(map[string]any)["maxConcurrentRequests"]))
}

// TestFVTProjectMemberQuotaMigration covers folding the retired
// per-project member limits into the personal tier: the strictest
// non-zero value of each dimension wins and an existing budget is never
// overwritten.
func TestFVTProjectMemberQuotaMigration(t *testing.T) {
	env := newOwnershipFvtEnv(t)
	ctx := env.sessionCtx("33333333-3333-4333-8333-333333333333")

	second := &tenancy.Project{ID: "proj-b", OrganizationID: "org-fvt", DisplayName: "B", State: tenancy.StateActive}
	require.NoError(t, env.db.Create(second).Error)
	// Alice carries 30 in proj-a and 20 in proj-b: the strictest (20) wins.
	require.NoError(t, env.db.Model(&tenancy.ProjectMember{}).
		Where("project_id = ? AND user_id = ?", "proj-a", "11111111-1111-4111-8111-111111111111").
		Update("max_concurrent_requests", 30).Error)
	require.NoError(t, env.db.Create(&tenancy.ProjectMember{
		ProjectID: "proj-b", UserID: "11111111-1111-4111-8111-111111111111", Role: tenancy.RoleMember, JoinedAt: time.Now().UTC(),
		MaxConcurrentRequests: 20,
	}).Error)
	// Bob already has an edited personal budget: the migration keeps it.
	require.NoError(t, env.db.Create(&tenancy.UserQuota{
		OrganizationID: "org-fvt", UserID: "22222222-2222-4222-8222-222222222222", MaxConcurrentRequests: 99,
	}).Error)
	require.NoError(t, env.db.Model(&tenancy.ProjectMember{}).
		Where("project_id = ? AND user_id = ?", "proj-a", "22222222-2222-4222-8222-222222222222").
		Update("max_concurrent_requests", 2).Error)

	repo := tenancy.NewRepository(env.db)
	require.NoError(t, repo.MigrateProjectMemberQuotas(ctx))

	var aliceRow tenancy.UserQuota
	require.NoError(t, env.db.Where("organization_id = ? AND user_id = ?", "org-fvt", "11111111-1111-4111-8111-111111111111").
		First(&aliceRow).Error)
	assert.EqualValues(t, 20, aliceRow.MaxConcurrentRequests)

	var bobRow tenancy.UserQuota
	require.NoError(t, env.db.Where("organization_id = ? AND user_id = ?", "org-fvt", "22222222-2222-4222-8222-222222222222").
		First(&bobRow).Error)
	assert.EqualValues(t, 99, bobRow.MaxConcurrentRequests, "an edited budget is never overwritten")

	// Re-running is a no-op.
	require.NoError(t, repo.MigrateProjectMemberQuotas(ctx))
	require.NoError(t, env.db.Where("organization_id = ? AND user_id = ?", "org-fvt", "11111111-1111-4111-8111-111111111111").
		First(&aliceRow).Error)
	assert.EqualValues(t, 20, aliceRow.MaxConcurrentRequests)
}

// findQuotaRow returns the row for one user id.
func findQuotaRow(t *testing.T, rows []any, userID string) map[string]any {
	t.Helper()
	for _, raw := range rows {
		row := raw.(map[string]any)
		if row["userId"] == userID {
			return row
		}
	}
	t.Fatalf("user %q not found in %v", userID, rows)
	return nil
}

// ensure the gateway interceptor import is used (the env wires the
// production chain in the ownership suite).
var _ = grpcmiddleware.UnaryServerInterceptor
