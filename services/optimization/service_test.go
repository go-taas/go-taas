package optimization

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	auditapi "github.com/go-taas/go-taas/services/audit"

	optimizationv1 "github.com/go-taas/go-taas/proto/taas/optimization/v1"
)

// fakeRoleGuard is a RoleGuard double.
type fakeRoleGuard struct{ err error }

func (g fakeRoleGuard) RequireRole(context.Context, string, string, string) error { return g.err }

// fakeSessionOrg is a SessionOrgResolver double.
type fakeSessionOrg struct{ org string }

func (r fakeSessionOrg) SessionActiveOrg(context.Context) (string, error) { return r.org, nil }

// fakeSessionUser is a SessionUserResolver double.
type fakeSessionUser struct{ id string }

func (r fakeSessionUser) SessionUserID(context.Context) (string, error) { return r.id, nil }

// fakeAuditRecorder is an AuditRecorder double.
type fakeAuditRecorder struct{ events *[]*auditapi.AuditEvent }

func (r fakeAuditRecorder) Record(_ context.Context, ev *auditapi.AuditEvent) {
	*r.events = append(*r.events, ev)
}

// newTestDB opens a fresh in-memory SQLite database with the three
// optimization tables.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	require.NoError(t, MigrateSchemaForFVT(db))
	return db
}

// newSvc builds an optimization service over a fresh in-memory
// database wired with fake seams: an authenticated admin of org-1.
func newSvc(t *testing.T) *Service {
	t.Helper()
	db := newTestDB(t)
	svc := NewForFVT(db)
	svc.SetSessionUserResolver(fakeSessionUser{id: "admin-1"})
	svc.SetSessionOrgResolver(fakeSessionOrg{org: "org-1"})
	svc.SetRoleGuard(fakeRoleGuard{})
	return svc
}

// seedRecommendation inserts one row directly and returns it.
func seedRecommendation(t *testing.T, db *gorm.DB, rule, orgID, suggestion string, saving float64) *Recommendation {
	t.Helper()
	row := &Recommendation{
		ID:                     "rec-" + rule + "-" + orgID,
		Fingerprint:            Fingerprint(rule, TargetOrgModel, orgID+"/model-1", suggestion),
		Rule:                   rule,
		TargetType:             TargetOrgModel,
		TargetID:               orgID + "/model-1",
		TargetDisplay:          orgID + " / model-1",
		OrganizationID:         orgID,
		ModelID:                "model-1",
		Suggestion:             suggestion,
		EstimatedMonthlySaving: saving,
		Currency:               "CNY",
		Inputs:                 []byte(`{"requests": 100}`),
		Arithmetic:             "a - b = c",
		Alternatives:           []byte(`[]`),
		ActingPageAdmin:        []byte(`{"route":"/admin/models/m2/authorizations","label":"Configure grants"}`),
		ActingPageUser:         []byte(`{"route":"/models","label":"View model catalog"}`),
		FirstSeenAt:            time.Now().UTC().Add(-time.Hour),
		LastComputedAt:         time.Now().UTC(),
	}
	require.NoError(t, db.Create(row).Error)
	return row
}

// TestFingerprintStability covers the identity derivation (D4): stable
// for the same inputs, different for any changed component, and the
// saving does not participate.
func TestFingerprintStability(t *testing.T) {
	a := Fingerprint(RuleSubstitution, TargetOrgModel, "org-1/model-a", "switch to b")
	b := Fingerprint(RuleSubstitution, TargetOrgModel, "org-1/model-a", "switch to b")
	assert.Equal(t, a, b)
	assert.Len(t, a, 64) // sha256 hex
	assert.NotEqual(t, a, Fingerprint(RuleBatch, TargetOrgModel, "org-1/model-a", "switch to b"))
	assert.NotEqual(t, a, Fingerprint(RuleSubstitution, TargetAPIKey, "org-1/model-a", "switch to b"))
	assert.NotEqual(t, a, Fingerprint(RuleSubstitution, TargetOrgModel, "org-1/model-x", "switch to b"))
	assert.NotEqual(t, a, Fingerprint(RuleSubstitution, TargetOrgModel, "org-1/model-a", "switch to c"))
}

// TestAdminListAndDetail covers the admin list (ranking, filters,
// pagination) and detail projection.
func TestAdminListAndDetail(t *testing.T) {
	svc := newSvc(t)
	db := svc.repo.db(context.Background())
	seedRecommendation(t, db, RuleSubstitution, "org-1", "switch to b", 500)
	seedRecommendation(t, db, RuleCache, "org-1", "cache prompts", 200)
	seedRecommendation(t, db, RuleSubstitution, "org-2", "switch to c", 900)

	ctx := context.Background()

	// Ranked by saving DESC.
	resp, err := svc.ListOptimizationRecommendations(ctx, &optimizationv1.ListOptimizationRecommendationsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetRecommendations(), 3)
	assert.Equal(t, "900.00", resp.GetRecommendations()[0].GetEstimatedMonthlySaving())
	assert.Equal(t, "500.00", resp.GetRecommendations()[1].GetEstimatedMonthlySaving())
	assert.Equal(t, int64(3), resp.GetPageMeta().GetTotal())

	// Rule filter.
	resp, err = svc.ListOptimizationRecommendations(ctx, &optimizationv1.ListOptimizationRecommendationsRequest{Rule: RuleCache})
	require.NoError(t, err)
	require.Len(t, resp.GetRecommendations(), 1)
	assert.Equal(t, RuleCache, resp.GetRecommendations()[0].GetRule())

	// Org filter.
	resp, err = svc.ListOptimizationRecommendations(ctx, &optimizationv1.ListOptimizationRecommendationsRequest{OrganizationId: "org-2"})
	require.NoError(t, err)
	require.Len(t, resp.GetRecommendations(), 1)

	// Min-saving filter.
	resp, err = svc.ListOptimizationRecommendations(ctx, &optimizationv1.ListOptimizationRecommendationsRequest{MinSaving: "600"})
	require.NoError(t, err)
	require.Len(t, resp.GetRecommendations(), 1)

	// Invalid rule -> 13205.
	_, err = svc.ListOptimizationRecommendations(ctx, &optimizationv1.ListOptimizationRecommendationsRequest{Rule: "r9"})
	assert.Equal(t, apierrors.CodeOptimizationRangeInvalid, apierrors.CodeOf(err))

	// Invalid min-saving -> 13205.
	_, err = svc.ListOptimizationRecommendations(ctx, &optimizationv1.ListOptimizationRecommendationsRequest{MinSaving: "abc"})
	assert.Equal(t, apierrors.CodeOptimizationRangeInvalid, apierrors.CodeOf(err))

	// Detail: the acting page is the admin variant (AD16).
	detail, err := svc.GetOptimizationRecommendation(ctx, &optimizationv1.GetOptimizationRecommendationRequest{RecommendationId: "rec-" + RuleSubstitution + "-org-1"})
	require.NoError(t, err)
	assert.Equal(t, StatusActive, detail.GetRecommendation().GetStatus())
	assert.Equal(t, "/admin/models/m2/authorizations", detail.GetRecommendation().GetActingPage().GetRoute())

	// Unknown id -> 13201.
	_, err = svc.GetOptimizationRecommendation(ctx, &optimizationv1.GetOptimizationRecommendationRequest{RecommendationId: "missing"})
	assert.Equal(t, apierrors.CodeRecommendationNotFound, apierrors.CodeOf(err))
}

// TestDismissRestoreLifecycle covers the dismiss/restore flow and its
// error codes (FR3.1/FR3.2).
func TestDismissRestoreLifecycle(t *testing.T) {
	svc := newSvc(t)
	db := svc.repo.db(context.Background())
	row := seedRecommendation(t, db, RuleSubstitution, "org-1", "switch to b", 500)
	ctx := context.Background()

	events := make([]*auditapi.AuditEvent, 0, 4)
	svc.SetAuditRecorder(fakeAuditRecorder{&events})

	// Invalid reason -> 13203.
	_, err := svc.DismissRecommendation(ctx, &optimizationv1.DismissRecommendationRequest{RecommendationId: row.ID, Reason: "bored"})
	assert.Equal(t, apierrors.CodeRecommendationDismissReasonInvalid, apierrors.CodeOf(err))

	// other without a note -> 13203.
	_, err = svc.DismissRecommendation(ctx, &optimizationv1.DismissRecommendationRequest{RecommendationId: row.ID, Reason: ReasonOther})
	assert.Equal(t, apierrors.CodeRecommendationDismissReasonInvalid, apierrors.CodeOf(err))

	// A note over 256 chars -> 13203.
	_, err = svc.DismissRecommendation(ctx, &optimizationv1.DismissRecommendationRequest{RecommendationId: row.ID, Reason: ReasonPlanned, Note: string(make([]byte, 300))})
	assert.Equal(t, apierrors.CodeRecommendationDismissReasonInvalid, apierrors.CodeOf(err))

	// Dismiss with planned.
	resp, err := svc.DismissRecommendation(ctx, &optimizationv1.DismissRecommendationRequest{RecommendationId: row.ID, Reason: ReasonPlanned})
	require.NoError(t, err)
	assert.Equal(t, StatusDismissed, resp.GetRecommendation().GetStatus())
	assert.Equal(t, ReasonPlanned, resp.GetRecommendation().GetDismissedReason())
	assert.Equal(t, "admin-1", resp.GetRecommendation().GetDismissedBy())

	// Double dismiss -> 13202.
	_, err = svc.DismissRecommendation(ctx, &optimizationv1.DismissRecommendationRequest{RecommendationId: row.ID, Reason: ReasonPlanned})
	assert.Equal(t, apierrors.CodeRecommendationDismissed, apierrors.CodeOf(err))

	// The default list hides dismissed rows; status=dismissed shows them.
	list, err := svc.ListOptimizationRecommendations(ctx, &optimizationv1.ListOptimizationRecommendationsRequest{})
	require.NoError(t, err)
	assert.Empty(t, list.GetRecommendations())
	list, err = svc.ListOptimizationRecommendations(ctx, &optimizationv1.ListOptimizationRecommendationsRequest{Status: StatusDismissed})
	require.NoError(t, err)
	assert.Len(t, list.GetRecommendations(), 1)
	list, err = svc.ListOptimizationRecommendations(ctx, &optimizationv1.ListOptimizationRecommendationsRequest{Status: StatusAll})
	require.NoError(t, err)
	assert.Len(t, list.GetRecommendations(), 1)

	// Restore an active row -> 13204 (already restored below first).
	restored, err := svc.RestoreRecommendation(ctx, &optimizationv1.RestoreRecommendationRequest{RecommendationId: row.ID})
	require.NoError(t, err)
	assert.Equal(t, StatusActive, restored.GetRecommendation().GetStatus())

	// Restore again -> 13204.
	_, err = svc.RestoreRecommendation(ctx, &optimizationv1.RestoreRecommendationRequest{RecommendationId: row.ID})
	assert.Equal(t, apierrors.CodeRecommendationRestoreInvalid, apierrors.CodeOf(err))

	// Audit trail: dismissed + restored.
	actions := []string{}
	for _, ev := range events {
		actions = append(actions, ev.Action)
	}
	assert.Contains(t, actions, "optimization.recommendation_dismissed")
	assert.Contains(t, actions, "optimization.recommendation_restored")
}

// TestUserSurfaceMasking covers the user surface: org scoping, the R3
// exclusion and the masked acting page (AC8/AC9).
func TestUserSurfaceMasking(t *testing.T) {
	svc := newSvc(t)
	db := svc.repo.db(context.Background())
	own := seedRecommendation(t, db, RuleSubstitution, "org-1", "switch to b", 500)
	seedRecommendation(t, db, RuleCache, "org-1", "cache prompts", 200)
	foreign := seedRecommendation(t, db, RuleSubstitution, "org-2", "switch to c", 900)
	// An R3 row on the caller's own org: excluded from the user surface.
	r3 := &Recommendation{
		ID:                     "rec-r3-org-1",
		Fingerprint:            Fingerprint(RuleIdleService, TargetService, "svc-1", "enable scale-to-zero"),
		Rule:                   RuleIdleService,
		TargetType:             TargetService,
		TargetID:               "svc-1",
		TargetDisplay:          "svc-1",
		OrganizationID:         "org-1",
		ServiceID:              "svc-1",
		Suggestion:             "enable scale-to-zero",
		EstimatedMonthlySaving: 300,
		Currency:               "CNY",
		Inputs:                 []byte(`{}`),
		Alternatives:           []byte(`[]`),
		ActingPageAdmin:        []byte(`{"route":"/admin/autoscaling","label":"Enable scale-to-zero"}`),
		FirstSeenAt:            time.Now().UTC(),
		LastComputedAt:         time.Now().UTC(),
	}
	require.NoError(t, db.Create(r3).Error)

	ctx := context.Background()

	// GetMyOptimization: only the org's own R1/R2/R4 rows, ranked.
	resp, err := svc.GetMyOptimization(ctx, &optimizationv1.GetMyOptimizationRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetRecommendations(), 2)
	assert.Equal(t, int64(2), resp.GetOverview().GetActiveCount())
	// The user acting page variant (AD16).
	assert.Equal(t, "/models", resp.GetRecommendations()[0].GetActingPage().GetRoute())

	// A foreign org's id -> 13201 (AC9).
	_, err = svc.GetMyOptimizationRecommendation(ctx, &optimizationv1.GetMyOptimizationRecommendationRequest{RecommendationId: foreign.ID})
	assert.Equal(t, apierrors.CodeRecommendationNotFound, apierrors.CodeOf(err))

	// An R3 row on the caller's own org -> 13201 (AC9).
	_, err = svc.GetMyOptimizationRecommendation(ctx, &optimizationv1.GetMyOptimizationRecommendationRequest{RecommendationId: r3.ID})
	assert.Equal(t, apierrors.CodeRecommendationNotFound, apierrors.CodeOf(err))

	// Own row resolves with the user acting variant.
	detail, err := svc.GetMyOptimizationRecommendation(ctx, &optimizationv1.GetMyOptimizationRecommendationRequest{RecommendationId: own.ID})
	require.NoError(t, err)
	assert.Equal(t, "/models", detail.GetRecommendation().GetActingPage().GetRoute())

	// The user list excludes R3 and foreign rows.
	list, err := svc.ListMyOptimizationRecommendations(ctx, &optimizationv1.ListMyOptimizationRecommendationsRequest{})
	require.NoError(t, err)
	assert.Len(t, list.GetRecommendations(), 2)

	// The user dismiss flow is keyed (user, fingerprint): independent
	// of the admin surface state.
	_, err = svc.DismissMyRecommendation(ctx, &optimizationv1.DismissMyRecommendationRequest{RecommendationId: own.ID, Reason: ReasonNotApplicable})
	require.NoError(t, err)
	// The admin surface still sees it active.
	adminView, err := svc.GetOptimizationRecommendation(ctx, &optimizationv1.GetOptimizationRecommendationRequest{RecommendationId: own.ID})
	require.NoError(t, err)
	assert.Equal(t, StatusActive, adminView.GetRecommendation().GetStatus())
	// The user surface sees it dismissed.
	userView, err := svc.GetMyOptimizationRecommendation(ctx, &optimizationv1.GetMyOptimizationRecommendationRequest{RecommendationId: own.ID})
	require.NoError(t, err)
	assert.Equal(t, StatusDismissed, userView.GetRecommendation().GetStatus())
	// Restore on the user surface.
	_, err = svc.RestoreMyRecommendation(ctx, &optimizationv1.RestoreMyRecommendationRequest{RecommendationId: own.ID})
	require.NoError(t, err)
}

// TestOverview covers the summary aggregates (FR2.2).
func TestOverview(t *testing.T) {
	svc := newSvc(t)
	db := svc.repo.db(context.Background())
	seedRecommendation(t, db, RuleSubstitution, "org-1", "switch to b", 500)
	seedRecommendation(t, db, RuleCache, "org-1", "cache prompts", 200)
	seedRecommendation(t, db, RuleSubstitution, "org-2", "switch to c", 900)

	ctx := context.Background()
	resp, err := svc.GetOptimizationOverview(ctx, &optimizationv1.GetOptimizationOverviewRequest{})
	require.NoError(t, err)
	ov := resp.GetOverview()
	assert.Equal(t, "1600.00", ov.GetTotalEstimatedMonthlySaving())
	assert.Equal(t, int64(3), ov.GetActiveCount())
	assert.Equal(t, int64(0), ov.GetDismissedCount())
	// Top org by summed saving.
	assert.Equal(t, "org-2", ov.GetTopOrganizationId())
	assert.Equal(t, "900.00", ov.GetTopOrganizationSaving())
	// count_by_rule carries the grouped counts.
	byRule := map[string]int64{}
	for _, rc := range ov.GetCountByRule() {
		byRule[rc.GetRule()] = rc.GetCount()
	}
	assert.Equal(t, int64(2), byRule[RuleSubstitution])
	assert.Equal(t, int64(1), byRule[RuleCache])

	// A successful run row sets last_refresh_at.
	require.NoError(t, db.Create(&Run{ID: "run-1", FinishedAt: time.Now().UTC(), Success: true, RulesEvaluated: 4}).Error)
	resp, err = svc.GetOptimizationOverview(ctx, &optimizationv1.GetOptimizationOverviewRequest{})
	require.NoError(t, err)
	assert.NotZero(t, resp.GetOverview().GetLastRefreshAt())
}

// TestRequireAdminRole covers the admin gate.
func TestRequireAdminRole(t *testing.T) {
	ctx := context.Background()

	// No role guard -> unauthorized.
	svc := newSvc(t)
	svc.SetRoleGuard(nil)
	_, err := svc.GetOptimizationOverview(ctx, &optimizationv1.GetOptimizationOverviewRequest{})
	assert.Equal(t, apierrors.CodeUnauthorized, apierrors.CodeOf(err))

	// A denied role propagates.
	svc2 := newSvc(t)
	svc2.SetRoleGuard(fakeRoleGuard{err: apierrors.New(apierrors.CodeForbidden)})
	_, err = svc2.GetOptimizationOverview(ctx, &optimizationv1.GetOptimizationOverviewRequest{})
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// No session org resolver on the user surface -> unauthorized.
	svc3 := newSvc(t)
	svc3.SetSessionOrgResolver(nil)
	_, err = svc3.GetMyOptimization(ctx, &optimizationv1.GetMyOptimizationRequest{})
	assert.Equal(t, apierrors.CodeUnauthorized, apierrors.CodeOf(err))
}

// TestValidateDismissal covers the reason/note validation matrix.
func TestValidateDismissal(t *testing.T) {
	_, _, err := validateDismissal("not_applicable", "")
	require.NoError(t, err)
	_, _, err = validateDismissal("planned", "next quarter")
	require.NoError(t, err)
	_, _, err = validateDismissal("other", "custom reason")
	require.NoError(t, err)
	_, _, err = validateDismissal("other", "")
	require.Error(t, err)
	_, _, err = validateDismissal("invalid", "")
	require.Error(t, err)
	_, _, err = validateDismissal("planned", string(make([]byte, 300)))
	require.Error(t, err)
}

// TestInPeakWindow covers the peak-window evaluation (R2's offline
// shape).
func TestInPeakWindow(t *testing.T) {
	cfg := config.OptimizationConfig{
		PeakHours:        []string{"09:00-18:00"},
		PeakWeekdaysOnly: true,
		Timezone:         "UTC",
	}
	// 2026-01-05 is a Monday. 10:00 UTC is inside 09:00-18:00.
	inside := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	assert.True(t, inPeakWindow(inside, cfg))
	// 20:00 UTC is outside.
	outside := time.Date(2026, 1, 5, 20, 0, 0, 0, time.UTC)
	assert.False(t, inPeakWindow(outside, cfg))
	// Saturday is outside when weekdays-only.
	saturday := time.Date(2026, 1, 10, 10, 0, 0, 0, time.UTC)
	assert.False(t, inPeakWindow(saturday, cfg))
	// Weekends count when the weekdays-only flag is off.
	cfg.PeakWeekdaysOnly = false
	assert.True(t, inPeakWindow(saturday, cfg))
	// A non-UTC timezone shifts the evaluation.
	cfg.PeakWeekdaysOnly = true
	cfg.Timezone = "Asia/Shanghai"
	// 02:00 UTC = 10:00 Shanghai (inside).
	shanghaiInside := time.Date(2026, 1, 5, 2, 0, 0, 0, time.UTC)
	assert.True(t, inPeakWindow(shanghaiInside, cfg))
}

// TestEffectiveAutoscaling covers the R3 policy resolution.
func TestEffectiveAutoscaling(t *testing.T) {
	global := &PolicyRow{Enabled: true, ScaleToZero: false}
	// Empty JSON inherits the global.
	en, s2z, err := effectiveAutoscaling("{}", global)
	require.NoError(t, err)
	assert.True(t, en)
	assert.False(t, s2z)
	// The service JSON overrides.
	en, s2z, err = effectiveAutoscaling(`{"enabled":false,"scale_to_zero":false}`, global)
	require.NoError(t, err)
	assert.False(t, en)
	assert.False(t, s2z)
	// Invalid JSON errors.
	_, _, err = effectiveAutoscaling("{bad", global)
	require.Error(t, err)
	// Nil global with empty JSON: zero policy (candidate).
	en, s2z, err = effectiveAutoscaling("{}", nil)
	require.NoError(t, err)
	assert.False(t, en)
	assert.False(t, s2z)
}
