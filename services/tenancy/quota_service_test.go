package tenancy

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/services/audit"
)

// fakeQuotaSessionOrgResolver returns a fixed org for the session.
type fakeQuotaSessionOrgResolver struct{ org string }

func (f fakeQuotaSessionOrgResolver) SessionActiveOrg(context.Context) (string, error) {
	return f.org, nil
}

// fakeQuotaSessionResolver returns a fixed user id for the session.
type fakeQuotaSessionResolver struct{ user string }

func (f fakeQuotaSessionResolver) SessionUserID(context.Context) (string, error) { return f.user, nil }
func (f fakeQuotaSessionResolver) SessionUserEmail(context.Context) (string, error) {
	return f.user + "@example.com", nil
}

// fakeQuotaAuditRecorder is no longer used; the env uses the real
// audit recorder so the change-history read sees DB rows.

// quotaTestEnv wires a tenancy service with the quota seams.
type quotaTestEnv struct {
	svc   *Service
	ctx   context.Context
	orgID string
}

func newQuotaTestEnv(t *testing.T, roleAllowed bool) *quotaTestEnv {
	t.Helper()
	db := newTestDB(t)
	// The change-history read queries audit_events directly (AD16), so
	// the recorder must write real rows to the DB.
	require.NoError(t, audit.MigrateSchemaForFVT(db))
	svc := NewForFVT(db)
	// The session user id must match the seeded membership's uuid.
	userID := "11111111-1111-1111-1111-111111111111"
	svc.SetSessionOrgResolver(fakeQuotaSessionOrgResolver{org: "org-a"})
	svc.SetSessionResolver(fakeQuotaSessionResolver{user: userID})
	svc.SetAuditRecorder(audit.NewRecorder(audit.NewRepository(db)))

	ctx := context.Background()
	repo := NewRepository(db)
	require.NoError(t, repo.CreateOrganization(ctx, &Organization{ID: "org-a", DisplayName: "Org A", State: StateActive}))

	// Seed a membership so the real RoleGuard can resolve the caller's
	// role. The user id is a uuid column; use a fixed uuid.
	membershipRepo := NewMembershipRepository(db)
	require.NoError(t, membershipRepo.AddMember(ctx, &OrgMember{
		OrganizationID: "org-a",
		UserID:         userID,
		Role:           RoleAdmin,
		JoinedAt:       time.Now().UTC(),
	}))
	svc.SetRoleGuard(NewRoleGuard(db))
	if !roleAllowed {
		// Downgrade the member to viewer so the admin gate denies.
		require.NoError(t, membershipRepo.SetMemberRole(ctx, "org-a", userID, RoleViewer))
	}
	return &quotaTestEnv{svc: svc, ctx: ctx, orgID: "org-a"}
}

func TestValidateQuotaRanges(t *testing.T) {
	// Valid: 0 = unlimited and in-range values.
	assert.NoError(t, ValidateQuota("max_concurrent_requests", 0))
	assert.NoError(t, ValidateQuota("max_concurrent_requests", 1))
	assert.NoError(t, ValidateQuota("max_concurrent_requests", 10000))
	assert.NoError(t, ValidateQuota("max_inference_services", 1000))
	assert.NoError(t, ValidateQuota("max_batch_jobs_per_day", 10000))
	assert.NoError(t, ValidateQuota("max_output_tokens_per_request", 0))
	assert.NoError(t, ValidateQuota("max_output_tokens_per_request", 64))
	assert.NoError(t, ValidateQuota("max_output_tokens_per_request", 32768))

	// Invalid: out of range.
	assert.EqualValues(t, apierrors.CodeQuotaInvalid, apierrors.CodeOf(ValidateQuota("max_concurrent_requests", 10001)))
	assert.EqualValues(t, apierrors.CodeQuotaInvalid, apierrors.CodeOf(ValidateQuota("max_concurrent_requests", -1)))
	assert.EqualValues(t, apierrors.CodeQuotaInvalid, apierrors.CodeOf(ValidateQuota("max_inference_services", 1001)))
	assert.EqualValues(t, apierrors.CodeQuotaInvalid, apierrors.CodeOf(ValidateQuota("max_batch_jobs_per_day", 10001)))
	assert.EqualValues(t, apierrors.CodeQuotaInvalid, apierrors.CodeOf(ValidateQuota("max_output_tokens_per_request", 63)))
	assert.EqualValues(t, apierrors.CodeQuotaInvalid, apierrors.CodeOf(ValidateQuota("max_output_tokens_per_request", 32769)))
	assert.EqualValues(t, apierrors.CodeQuotaInvalid, apierrors.CodeOf(ValidateQuota("bogus", 5)))
}

func TestUpdateOrganizationQuotaSetsAndAudits(t *testing.T) {
	env := newQuotaTestEnv(t, true)
	req := &tenancyv1.UpdateOrganizationQuotaRequest{
		OrganizationId:        env.orgID,
		MaxConcurrentRequests: int64Ptr(50),
		MaxInferenceServices:  int64Ptr(20),
	}
	resp, err := env.svc.UpdateOrganizationQuota(env.ctx, req)
	require.NoError(t, err)
	assert.Equal(t, int64(50), resp.GetQuota().GetMaxConcurrentRequests())
	assert.Equal(t, int64(20), resp.GetQuota().GetMaxInferenceServices())
	assert.Equal(t, "11111111-1111-1111-1111-111111111111", resp.GetQuota().GetUpdatedBy())
	assert.NotZero(t, resp.GetQuota().GetUpdatedAt())

	// The change history (read from audit_events, AD16) carries the two
	// changed dimensions.
	hist, err := env.svc.GetOrganizationQuota(env.ctx, &tenancyv1.GetOrganizationQuotaRequest{OrganizationId: env.orgID})
	require.NoError(t, err)
	require.Len(t, hist.GetChanges(), 2)
	assert.Equal(t, "max_concurrent_requests", hist.GetChanges()[0].GetDimension())
	assert.Equal(t, int64(0), hist.GetChanges()[0].GetOldValue())
	assert.Equal(t, int64(50), hist.GetChanges()[0].GetNewValue())
	assert.Equal(t, "max_inference_services", hist.GetChanges()[1].GetDimension())
}

func TestUpdateOrganizationQuotaNoOpWritesNothing(t *testing.T) {
	env := newQuotaTestEnv(t, true)
	// First set a value.
	_, err := env.svc.UpdateOrganizationQuota(env.ctx, &tenancyv1.UpdateOrganizationQuotaRequest{
		OrganizationId:        env.orgID,
		MaxConcurrentRequests: int64Ptr(50),
	})
	require.NoError(t, err)

	// A no-op patch (same value) writes nothing and audits nothing.
	resp, err := env.svc.UpdateOrganizationQuota(env.ctx, &tenancyv1.UpdateOrganizationQuotaRequest{
		OrganizationId:        env.orgID,
		MaxConcurrentRequests: int64Ptr(50),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(50), resp.GetQuota().GetMaxConcurrentRequests())

	// The change history still has exactly one entry (the first update).
	hist, err := env.svc.GetOrganizationQuota(env.ctx, &tenancyv1.GetOrganizationQuotaRequest{OrganizationId: env.orgID})
	require.NoError(t, err)
	require.Len(t, hist.GetChanges(), 1)
}

func TestUpdateOrganizationQuotaInvalidValue(t *testing.T) {
	env := newQuotaTestEnv(t, true)
	_, err := env.svc.UpdateOrganizationQuota(env.ctx, &tenancyv1.UpdateOrganizationQuotaRequest{
		OrganizationId:        env.orgID,
		MaxConcurrentRequests: int64Ptr(10001),
	})
	assert.EqualValues(t, apierrors.CodeQuotaInvalid, apierrors.CodeOf(err))
}

func TestUpdateOrganizationQuotaUnknownOrg(t *testing.T) {
	env := newQuotaTestEnv(t, true)
	_, err := env.svc.UpdateOrganizationQuota(env.ctx, &tenancyv1.UpdateOrganizationQuotaRequest{
		OrganizationId:        "org-missing",
		MaxConcurrentRequests: int64Ptr(50),
	})
	assert.EqualValues(t, apierrors.CodeQuotaNotFound, apierrors.CodeOf(err))
}

func TestUpdateOrganizationQuotaRoleDenied(t *testing.T) {
	env := newQuotaTestEnv(t, false)
	_, err := env.svc.UpdateOrganizationQuota(env.ctx, &tenancyv1.UpdateOrganizationQuotaRequest{
		OrganizationId:        env.orgID,
		MaxConcurrentRequests: int64Ptr(50),
	})
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

func TestGetOrganizationQuotaReturnsHistory(t *testing.T) {
	env := newQuotaTestEnv(t, true)
	// Two updates produce two audit events.
	_, err := env.svc.UpdateOrganizationQuota(env.ctx, &tenancyv1.UpdateOrganizationQuotaRequest{
		OrganizationId:        env.orgID,
		MaxConcurrentRequests: int64Ptr(50),
	})
	require.NoError(t, err)
	_, err = env.svc.UpdateOrganizationQuota(env.ctx, &tenancyv1.UpdateOrganizationQuotaRequest{
		OrganizationId:       env.orgID,
		MaxInferenceServices: int64Ptr(20),
	})
	require.NoError(t, err)

	resp, err := env.svc.GetOrganizationQuota(env.ctx, &tenancyv1.GetOrganizationQuotaRequest{OrganizationId: env.orgID})
	require.NoError(t, err)
	assert.Equal(t, int64(50), resp.GetQuota().GetMaxConcurrentRequests())
	assert.Equal(t, int64(20), resp.GetQuota().GetMaxInferenceServices())
	// Newest first: the second event (services) comes first.
	require.Len(t, resp.GetChanges(), 2)
	assert.Equal(t, "max_inference_services", resp.GetChanges()[0].GetDimension())
	assert.Equal(t, "max_concurrent_requests", resp.GetChanges()[1].GetDimension())
}

func TestGetOrganizationQuotaUnknownOrg(t *testing.T) {
	env := newQuotaTestEnv(t, true)
	_, err := env.svc.GetOrganizationQuota(env.ctx, &tenancyv1.GetOrganizationQuotaRequest{OrganizationId: "org-missing"})
	assert.EqualValues(t, apierrors.CodeQuotaNotFound, apierrors.CodeOf(err))
}

func TestListOrganizationQuotasFilters(t *testing.T) {
	env := newQuotaTestEnv(t, true)
	// org-a is unlimited by default.
	resp, err := env.svc.ListOrganizationQuotas(env.ctx, &tenancyv1.ListOrganizationQuotasRequest{
		Page: &commonv1.PageRequest{Limit: 50},
	})
	require.NoError(t, err)
	require.Len(t, resp.GetQuotas(), 1)
	assert.Equal(t, "org-a", resp.GetQuotas()[0].GetQuota().GetOrganizationId())

	// Limit a dimension, then the limited filter matches.
	_, err = env.svc.UpdateOrganizationQuota(env.ctx, &tenancyv1.UpdateOrganizationQuotaRequest{
		OrganizationId:        env.orgID,
		MaxConcurrentRequests: int64Ptr(50),
	})
	require.NoError(t, err)
	limited, err := env.svc.ListOrganizationQuotas(env.ctx, &tenancyv1.ListOrganizationQuotasRequest{
		LimitState: "limited",
		Page:       &commonv1.PageRequest{Limit: 50},
	})
	require.NoError(t, err)
	require.Len(t, limited.GetQuotas(), 1)
	unlimited, err := env.svc.ListOrganizationQuotas(env.ctx, &tenancyv1.ListOrganizationQuotasRequest{
		LimitState: "unlimited",
		Page:       &commonv1.PageRequest{Limit: 50},
	})
	require.NoError(t, err)
	assert.Len(t, unlimited.GetQuotas(), 0)

	// Invalid limit_state -> 13305.
	_, err = env.svc.ListOrganizationQuotas(env.ctx, &tenancyv1.ListOrganizationQuotasRequest{LimitState: "bogus"})
	assert.EqualValues(t, apierrors.CodeQuotaInvalid, apierrors.CodeOf(err))
}

func TestGetMyQuotaOrgScoped(t *testing.T) {
	env := newQuotaTestEnv(t, true)
	_, err := env.svc.UpdateOrganizationQuota(env.ctx, &tenancyv1.UpdateOrganizationQuotaRequest{
		OrganizationId:        env.orgID,
		MaxConcurrentRequests: int64Ptr(50),
	})
	require.NoError(t, err)

	// The session org resolver returns org-a; GetMyQuota returns org-a.
	resp, err := env.svc.GetMyQuota(env.ctx, &tenancyv1.GetMyQuotaRequest{})
	require.NoError(t, err)
	assert.Equal(t, "org-a", resp.GetQuota().GetOrganizationId())
	assert.Equal(t, int64(50), resp.GetQuota().GetMaxConcurrentRequests())
}

func TestGetMyQuotaNoOrgContext(t *testing.T) {
	env := newQuotaTestEnv(t, true)
	// A context without the session org resolver and without the header.
	env.svc.SetSessionOrgResolver(nil)
	ctx := context.Background()
	_, err := env.svc.GetMyQuota(ctx, &tenancyv1.GetMyQuotaRequest{})
	assert.EqualValues(t, apierrors.CodeUnauthorized, apierrors.CodeOf(err))
}

func TestGetMyQuotaTransitionalHeader(t *testing.T) {
	env := newQuotaTestEnv(t, true)
	env.svc.SetSessionOrgResolver(nil)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-organization-id", "org-a"))
	resp, err := env.svc.GetMyQuota(ctx, &tenancyv1.GetMyQuotaRequest{})
	require.NoError(t, err)
	assert.Equal(t, "org-a", resp.GetQuota().GetOrganizationId())
}

func TestOrgQuotaReaderUnknownOrgReturnsUnlimited(t *testing.T) {
	env := newQuotaTestEnv(t, true)
	q, err := env.svc.OrgQuota(env.ctx, "org-missing")
	require.NoError(t, err)
	assert.Equal(t, OrgQuota{}, q)
}

func TestConcurrencyConsumerSnapshot(t *testing.T) {
	consumer := NewConcurrencyConsumer(mq.NewFake(), time.Minute)
	// No entry -> 0.
	assert.Equal(t, int64(0), consumer.CurrentInFlight("org-a"))

	// A report stores the snapshot.
	body, _ := json.Marshal(concurrencyReport{OrganizationID: "org-a", InFlight: 12, ReportedAt: time.Now().Unix()})
	consumer.HandleForTest(mq.Message{Subject: "quota.orgs.concurrency", Body: body})
	assert.Equal(t, int64(12), consumer.CurrentInFlight("org-a"))

	// A stale entry reads as 0.
	consumer.staleAfter = time.Nanosecond
	time.Sleep(time.Millisecond)
	assert.Equal(t, int64(0), consumer.CurrentInFlight("org-a"))
}

func TestConcurrencyConsumerMalformed(t *testing.T) {
	consumer := NewConcurrencyConsumer(mq.NewFake(), time.Minute)
	consumer.HandleForTest(mq.Message{Subject: "quota.orgs.concurrency", Body: []byte("{bad")})
	assert.Equal(t, int64(0), consumer.CurrentInFlight("org-a"))
}

func int64Ptr(v int64) *int64 { return &v }
