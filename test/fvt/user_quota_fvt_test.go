// Per-user quota full-verification suite: the personal tier of the
// three-level quota model (organization, project, user). It exercises
// the admin CRUD, the role gate, the effective-value combination the
// user surface shows, and the migration that folds the retired
// per-project member limits into the personal tier.
package fvt

import (
	"net/http"
	"testing"

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
		OrganizationId:            "org-fvt",
		MaxConcurrentRequests:     ptrInt64(10),
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
// TestFVTProjectMemberQuotaTier covers the member tier of the quota ladder: a
// project administrator caps one member inside the project, the cap is stored
// with its spend dimension, and the effective tuple the data plane receives is
// the tighter of that cap and the person's organization-wide budget.
func TestFVTProjectMemberQuotaTier(t *testing.T) {
	env := newOwnershipFvtEnv(t)

	const alice = "11111111-1111-4111-8111-111111111111"

	// The person's organization-wide budget is looser than the member cap.
	require.NoError(t, env.db.Create(&tenancy.UserQuota{
		OrganizationID: "org-fvt", UserID: alice, MaxConcurrentRequests: 9, MonthlySpendLimitCents: 9000,
	}).Error)

	admin := "33333333-3333-4333-8333-333333333333"
	code, body := env.call(t, admin, http.MethodPatch,
		"/api/v1/admin/tenancy/projects/proj-a/member-quotas/"+alice,
		map[string]any{"maxConcurrentRequests": 3, "monthlySpendLimitCents": 2500, "maxOutputTokensPerRequest": 512})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	quota, _ := body["quota"].(map[string]any)
	require.NotNil(t, quota)
	assert.EqualValues(t, 3, numberOf(t, quota["maxConcurrentRequests"]))
	assert.EqualValues(t, 2500, numberOf(t, quota["monthlySpendLimitCents"]))

	effective, _ := quota["effective"].(map[string]any)
	require.NotNil(t, effective, "the response carries the combined limits: %v", quota)
	assert.EqualValues(t, 3, numberOf(t, effective["maxConcurrentRequests"]), "the member cap tightens")
	assert.EqualValues(t, 2500, numberOf(t, effective["monthlySpendLimitCents"]))
	assert.EqualValues(t, 512, numberOf(t, effective["maxOutputTokensPerRequest"]))

	// Reading the roster shows the same numbers.
	code, body = env.call(t, admin, http.MethodGet, "/api/v1/admin/tenancy/projects/proj-a/member-quotas", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	rows, _ := body["quotas"].([]any)
	row := findQuotaRow(t, rows, alice)
	assert.EqualValues(t, 2500, numberOf(t, row["monthlySpendLimitCents"]))

	// A member cap cannot loosen the organization-wide budget: raising it above
	// the personal value leaves the effective limit at the personal one.
	code, body = env.call(t, admin, http.MethodPatch,
		"/api/v1/admin/tenancy/projects/proj-a/member-quotas/"+alice,
		map[string]any{"maxConcurrentRequests": 40})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	quota, _ = body["quota"].(map[string]any)
	effective, _ = quota["effective"].(map[string]any)
	assert.EqualValues(t, 40, numberOf(t, quota["maxConcurrentRequests"]), "the stored cap is what was set")
	assert.EqualValues(t, 9, numberOf(t, effective["maxConcurrentRequests"]), "the effective cap stays at the stricter personal budget")
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

// TestFVTGetMyQuotaShowsProjectMemberTiers covers the member-facing ladder: the
// caller sees their budget in each project they belong to, and the effective
// value that budget produces for that project's keys.
func TestFVTGetMyQuotaShowsProjectMemberTiers(t *testing.T) {
	env := newOwnershipFvtEnv(t)
	const alice = "11111111-1111-4111-8111-111111111111"

	// The project administrator caps Alice inside proj-a.
	require.NoError(t, env.db.Model(&tenancy.ProjectMember{}).
		Where("project_id = ? AND user_id = ?", "proj-a", alice).
		Updates(map[string]any{
			"max_concurrent_requests": 3, "monthly_spend_limit_cents": 2500,
		}).Error)

	code, body := env.call(t, alice, http.MethodGet, "/api/v1/quotas", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	rows, _ := body["projectMemberQuotas"].([]any)
	require.Len(t, rows, 1, "Alice belongs to one project: %v", body)
	row := rows[0].(map[string]any)
	assert.Equal(t, "proj-a", row["projectId"])
	require.NotEmpty(t, row["projectName"])

	quota, _ := row["quota"].(map[string]any)
	require.NotNil(t, quota)
	assert.EqualValues(t, 3, numberOf(t, quota["maxConcurrentRequests"]))
	assert.EqualValues(t, 2500, numberOf(t, quota["monthlySpendLimitCents"]))

	effective, _ := row["effective"].(map[string]any)
	require.NotNil(t, effective)
	assert.EqualValues(t, 3, numberOf(t, effective["maxConcurrentRequests"]))
	assert.EqualValues(t, 2500, numberOf(t, effective["monthlySpendLimitCents"]))
}
