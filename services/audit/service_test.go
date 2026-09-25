package audit

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	auditv1 "github.com/go-taas/go-taas/proto/taas/audit/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func newServiceTestEnv(t *testing.T) *Service {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AuditEvent{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return NewForFVT(db)
}

func withOrg(ctx context.Context, orgID string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs(organizationMetadataKey, orgID))
}

// assertCode asserts the error is an APIError with the given code.
func assertCode(t *testing.T, err error, code apierrors.Code) {
	t.Helper()
	ae, ok := apierrors.As(err)
	require.True(t, ok, "expected an APIError, got %v", err)
	assert.Equal(t, code, ae.Code)
}

// fakeUserResolver resolves a fixed caller user id.
type fakeUserResolver struct{ userID string }

func (f *fakeUserResolver) SessionUserID(context.Context) (string, error) { return f.userID, nil }

// fakeOrgResolver resolves a fixed active org.
type fakeOrgResolver struct{ orgID string }

func (f *fakeOrgResolver) SessionActiveOrg(context.Context) (string, error) { return f.orgID, nil }

// fakeRoleGuard allows the seeded member (u-1 in org-1) and denies
// everyone else, mirroring tenancy.RoleGuard's behaviour for the audit
// admin role check.
type fakeRoleGuard struct{}

func (fakeRoleGuard) RequireRole(_ context.Context, orgID, userID, _ string) error {
	if orgID == "org-1" && userID == "u-1" {
		return nil
	}
	return apierrors.New(apierrors.CodeForbidden)
}

// AC1: RecordAuditEvent stores a row.
func TestServiceRecordAuditEvent(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := context.Background()

	resp, err := svc.RecordAuditEvent(ctx, &auditv1.RecordAuditEventRequest{
		OrganizationId: "org-1",
		ActorUserId:    "u-1",
		ActorType:      auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER,
		Action:         "api_key.revoke",
		ResourceType:   "api_key",
		ResourceId:     "key-1",
		Result:         auditv1.AuditResult_AUDIT_RESULT_SUCCESS,
		IpAddress:      "10.0.0.1",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetAuditEventId())

	repo, err := svc.repository()
	require.NoError(t, err)
	row, err := repo.FindAuditEventByID(ctx, resp.GetAuditEventId())
	require.NoError(t, err)
	assert.Equal(t, "api_key.revoke", row.Action)
	assert.Equal(t, "success", row.Result)
}
func TestRecorderBestEffortNonFatal(t *testing.T) {
	// A recorder with a nil repository must not panic and must not
	// return an error.
	var r *Recorder
	r.Record(context.Background(), &AuditEvent{Action: "auth.login"})

	// A recorder bound to a closed database logs and skips.
	db := newAuditTestDB(t)
	repo := NewRepository(db)
	rec := NewRecorder(repo)
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
	// Must not panic.
	rec.Record(context.Background(), &AuditEvent{Action: "auth.login"})
}

// AC3: both results are recorded — a success and a failure event both
// store the correct result.
func TestServiceBothResultsRecorded(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := context.Background()

	_, err := svc.RecordAuditEvent(ctx, &auditv1.RecordAuditEventRequest{
		OrganizationId: "org-1", ActorUserId: "u-1",
		ActorType: auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER,
		Action:    "auth.login", ResourceType: "session", ResourceId: "s1",
		Result: auditv1.AuditResult_AUDIT_RESULT_SUCCESS,
	})
	require.NoError(t, err)
	_, err = svc.RecordAuditEvent(ctx, &auditv1.RecordAuditEventRequest{
		OrganizationId: "org-1", ActorUserId: "u-1",
		ActorType: auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER,
		Action:    "auth.login", ResourceType: "session", ResourceId: "s2",
		Result: auditv1.AuditResult_AUDIT_RESULT_FAILURE,
	})
	require.NoError(t, err)

	repo, err := svc.repository()
	require.NoError(t, err)
	rows, total, err := repo.ListAuditEvents(ctx, AuditEventFilter{Offset: 0, Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	results := map[string]bool{}
	for _, r := range rows {
		results[r.Result] = true
	}
	assert.True(t, results["success"], "a success event is recorded")
	assert.True(t, results["failure"], "a failure event is recorded")
}

// AC4: range validation returns 10603 for since > until or a range >
// 366 days.
func TestServiceRangeValidation(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withOrg(context.Background(), "org-1")

	now := time.Now().Unix()
	_, err := svc.ListAuditEvents(ctx, &auditv1.ListAuditEventsRequest{
		Since: now, Until: now - 10,
	})
	assertCode(t, err, apierrors.CodeAuditRangeInvalid)

	_, err = svc.ListAuditEvents(ctx, &auditv1.ListAuditEventsRequest{
		Since: now - 400*24*3600, Until: now,
	})
	assertCode(t, err, apierrors.CodeAuditRangeInvalid)
}

// AC5: GetAuditEvent returns full metadata; unknown id -> 10601.
func TestServiceGetAuditEvent(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withOrg(context.Background(), "org-1")

	resp, err := svc.RecordAuditEvent(ctx, &auditv1.RecordAuditEventRequest{
		OrganizationId: "org-1", ActorUserId: "u-1",
		ActorType: auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER,
		Action:    "member.add", ResourceType: "member", ResourceId: "m-1",
		Result:   auditv1.AuditResult_AUDIT_RESULT_SUCCESS,
		Metadata: `{"role":"admin"}`,
	})
	require.NoError(t, err)

	got, err := svc.GetAuditEvent(ctx, &auditv1.GetAuditEventRequest{AuditEventId: resp.GetAuditEventId()})
	require.NoError(t, err)
	assert.Equal(t, "member.add", got.GetAuditEvent().GetAction())
	assert.Equal(t, `{"role":"admin"}`, got.GetAuditEvent().GetMetadata())

	_, err = svc.GetAuditEvent(ctx, &auditv1.GetAuditEventRequest{AuditEventId: "00000000-0000-0000-0000-000000000000"})
	assertCode(t, err, apierrors.CodeAuditEventNotFound)
}

// AC6: export format validation returns 10602; the cap returns
// truncated: true.
func TestServiceExport(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withOrg(context.Background(), "org-1")

	for i := 0; i < 3; i++ {
		_, err := svc.RecordAuditEvent(ctx, &auditv1.RecordAuditEventRequest{
			OrganizationId: "org-1", ActorUserId: "u-1",
			ActorType: auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER,
			Action:    "auth.login", ResourceType: "session", ResourceId: fmt.Sprintf("s%d", i),
			Result: auditv1.AuditResult_AUDIT_RESULT_SUCCESS,
		})
		require.NoError(t, err)
	}

	// CSV export.
	resp, err := svc.ExportAuditEvents(ctx, &auditv1.ExportAuditEventsRequest{
		Format: auditv1.AuditExportFormat_AUDIT_EXPORT_FORMAT_CSV,
	})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(resp.GetContent(), "audit_event_id,"), "CSV has a header row")
	assert.False(t, resp.GetTruncated())

	// JSON export.
	resp, err = svc.ExportAuditEvents(ctx, &auditv1.ExportAuditEventsRequest{
		Format: auditv1.AuditExportFormat_AUDIT_EXPORT_FORMAT_JSON,
	})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(resp.GetContent(), "["), "JSON is an array")
	assert.False(t, resp.GetTruncated())

	// Invalid format -> 10602.
	_, err = svc.ExportAuditEvents(ctx, &auditv1.ExportAuditEventsRequest{
		Format: auditv1.AuditExportFormat_AUDIT_EXPORT_FORMAT_UNSPECIFIED,
	})
	assertCode(t, err, apierrors.CodeAuditExportInvalid)

	// Cap of 2 -> truncated.
	svc.SetExportMaxRows(2)
	resp, err = svc.ExportAuditEvents(ctx, &auditv1.ExportAuditEventsRequest{
		Format: auditv1.AuditExportFormat_AUDIT_EXPORT_FORMAT_CSV,
	})
	require.NoError(t, err)
	assert.True(t, resp.GetTruncated(), "over-cap export is truncated")
}

// AC7: ListMyActivity/ExportMyActivity return only the caller's events.
func TestServiceMyActivityScoping(t *testing.T) {
	svc := newServiceTestEnv(t)
	svc.SetSessionUserResolver(&fakeUserResolver{userID: "u-1"})
	ctx := context.Background()

	for _, u := range []string{"u-1", "u-2"} {
		_, err := svc.RecordAuditEvent(ctx, &auditv1.RecordAuditEventRequest{
			OrganizationId: "org-1", ActorUserId: u,
			ActorType: auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER,
			Action:    "auth.login", ResourceType: "session", ResourceId: "s-" + u,
			Result: auditv1.AuditResult_AUDIT_RESULT_SUCCESS,
		})
		require.NoError(t, err)
	}

	resp, err := svc.ListMyActivity(ctx, &auditv1.ListMyActivityRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetAuditEvents(), 1)
	assert.Equal(t, "u-1", resp.GetAuditEvents()[0].GetActorUserId())

	exp, err := svc.ExportMyActivity(ctx, &auditv1.ExportMyActivityRequest{
		Format: auditv1.AuditExportFormat_AUDIT_EXPORT_FORMAT_JSON,
	})
	require.NoError(t, err)
	assert.True(t, strings.Contains(exp.GetContent(), "u-1"), "export contains the caller")
	assert.False(t, strings.Contains(exp.GetContent(), "u-2"), "export never contains another user")
}

// AC8: admin org scoping — an inaccessible org returns 10036.
func TestServiceAdminOrgScoping(t *testing.T) {
	svc := newServiceTestEnv(t)
	svc.SetSessionOrgResolver(&fakeOrgResolver{orgID: "org-1"})
	svc.SetSessionUserResolver(&fakeUserResolver{userID: "u-1"})

	// Seed a member with member role in org-1.
	svc.SetRoleGuard(fakeRoleGuard{})

	ctx := withOrg(context.Background(), "org-1")
	// A member can list org-1 events.
	_, err := svc.ListAuditEvents(ctx, &auditv1.ListAuditEventsRequest{})
	require.NoError(t, err)

	// A non-member in org-2 is forbidden: switch the session org to
	// org-2 where u-1 is not a member.
	svc.SetSessionOrgResolver(&fakeOrgResolver{orgID: "org-2"})
	_, err = svc.ListAuditEvents(ctx, &auditv1.ListAuditEventsRequest{})
	assertCode(t, err, apierrors.CodeForbidden)
}

// AC9: retention via RetainOnce leaves other tables untouched (the
// audit runner only deletes audit_events).
func TestRetentionRunnerRetainOnce(t *testing.T) {
	db := newAuditTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	old := now.Add(-400 * 24 * time.Hour)
	for i := 0; i < 3; i++ {
		_, err := repo.InsertAuditEvent(ctx, &AuditEvent{
			OrganizationID: "org-1", ActorUserID: "u-1", ActorType: "user",
			Action: "auth.login", ResourceType: "session", ResourceID: fmt.Sprintf("s%d", i),
			Result: "success", CreatedAt: old,
		})
		require.NoError(t, err)
	}
	_, err := repo.InsertAuditEvent(ctx, &AuditEvent{
		OrganizationID: "org-1", ActorUserID: "u-1", ActorType: "user",
		Action: "auth.login", ResourceType: "session", ResourceID: "recent",
		Result: "success", CreatedAt: now,
	})
	require.NoError(t, err)

	runner := NewAuditRetentionRunner(repo, 365*24*time.Hour, 2, time.Hour)
	runner.RetainOnce(ctx)

	var count int64
	require.NoError(t, db.Model(&AuditEvent{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "only the recent event survives")
}

// TestEnumMappings covers the actor-type and result enum conversions.
func TestEnumMappings(t *testing.T) {
	assert.Equal(t, "user", actorTypeString(auditv1.AuditActorType_AUDIT_ACTOR_TYPE_UNSPECIFIED))
	assert.Equal(t, "user", actorTypeString(auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER))
	assert.Equal(t, "system", actorTypeString(auditv1.AuditActorType_AUDIT_ACTOR_TYPE_SYSTEM))
	assert.Equal(t, "api_key", actorTypeString(auditv1.AuditActorType_AUDIT_ACTOR_TYPE_API_KEY))

	assert.Equal(t, auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER, actorTypeEnum("user"))
	assert.Equal(t, auditv1.AuditActorType_AUDIT_ACTOR_TYPE_SYSTEM, actorTypeEnum("system"))
	assert.Equal(t, auditv1.AuditActorType_AUDIT_ACTOR_TYPE_API_KEY, actorTypeEnum("api_key"))
	assert.Equal(t, auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER, actorTypeEnum("unknown"))

	assert.Equal(t, "", resultString(auditv1.AuditResult_AUDIT_RESULT_UNSPECIFIED))
	assert.Equal(t, "success", resultString(auditv1.AuditResult_AUDIT_RESULT_SUCCESS))
	assert.Equal(t, "failure", resultString(auditv1.AuditResult_AUDIT_RESULT_FAILURE))

	assert.Equal(t, auditv1.AuditResult_AUDIT_RESULT_SUCCESS, resultEnum("success"))
	assert.Equal(t, auditv1.AuditResult_AUDIT_RESULT_FAILURE, resultEnum("failure"))
	assert.Equal(t, auditv1.AuditResult_AUDIT_RESULT_SUCCESS, resultEnum("unknown"))
}

// TestPaginationAndRange covers pagination clamping and range defaults.
func TestPaginationAndRange(t *testing.T) {
	// Pagination defaults.
	offset, limit := normalizePagination(nil)
	assert.Equal(t, 0, offset)
	assert.Equal(t, 20, limit)

	// Cap at 100.
	offset, limit = normalizePagination(&commonv1.PageRequest{Offset: 5, Limit: 500})
	assert.Equal(t, 5, offset)
	assert.Equal(t, 100, limit)

	// Range defaults: until defaults to now+1, since to until-24h.
	since, until, err := validateRange(0, 0)
	require.NoError(t, err)
	assert.Equal(t, until-24*3600, since)
	assert.True(t, until > time.Now().Unix(), "until defaults to now+1")

	// clampInt32.
	assert.Equal(t, int32(100), clampInt32(100))
	assert.Equal(t, int32(2147483647), clampInt32(1<<40))
	assert.Equal(t, int32(-2147483648), clampInt32(-1<<40))
}

// TestExportMyActivityScoping covers the end-user export path with the
// caller resolver.
func TestExportMyActivityScoping(t *testing.T) {
	svc := newServiceTestEnv(t)
	svc.SetSessionUserResolver(&fakeUserResolver{userID: "u-1"})
	ctx := context.Background()

	_, err := svc.RecordAuditEvent(ctx, &auditv1.RecordAuditEventRequest{
		OrganizationId: "org-1", ActorUserId: "u-1",
		ActorType: auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER,
		Action:    "auth.login", ResourceType: "session", ResourceId: "s1",
		Result: auditv1.AuditResult_AUDIT_RESULT_SUCCESS,
	})
	require.NoError(t, err)

	// Invalid format -> 10602.
	_, err = svc.ExportMyActivity(ctx, &auditv1.ExportMyActivityRequest{
		Format: auditv1.AuditExportFormat_AUDIT_EXPORT_FORMAT_UNSPECIFIED,
	})
	assertCode(t, err, apierrors.CodeAuditExportInvalid)

	// CSV export.
	resp, err := svc.ExportMyActivity(ctx, &auditv1.ExportMyActivityRequest{
		Format: auditv1.AuditExportFormat_AUDIT_EXPORT_FORMAT_CSV,
	})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(resp.GetContent(), "audit_event_id,"))
	assert.False(t, resp.GetTruncated())
}

// TestListMyActivityFilter covers the action/result filters on the
// end-user activity list.
func TestListMyActivityFilter(t *testing.T) {
	svc := newServiceTestEnv(t)
	svc.SetSessionUserResolver(&fakeUserResolver{userID: "u-1"})
	ctx := context.Background()

	for _, r := range []auditv1.AuditResult{
		auditv1.AuditResult_AUDIT_RESULT_SUCCESS,
		auditv1.AuditResult_AUDIT_RESULT_FAILURE,
	} {
		_, err := svc.RecordAuditEvent(ctx, &auditv1.RecordAuditEventRequest{
			OrganizationId: "org-1", ActorUserId: "u-1",
			ActorType: auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER,
			Action:    "auth.login", ResourceType: "session", ResourceId: "s1",
			Result: r,
		})
		require.NoError(t, err)
	}

	resp, err := svc.ListMyActivity(ctx, &auditv1.ListMyActivityRequest{
		Result: auditv1.AuditResult_AUDIT_RESULT_FAILURE,
	})
	require.NoError(t, err)
	require.Len(t, resp.GetAuditEvents(), 1)
	assert.Equal(t, "failure", resultString(resp.GetAuditEvents()[0].GetResult()))
}

// TestGetAuditEventRoleGuard covers the role-guard gated detail path.
func TestGetAuditEventRoleGuard(t *testing.T) {
	svc := newServiceTestEnv(t)
	svc.SetSessionOrgResolver(&fakeOrgResolver{orgID: "org-1"})
	svc.SetSessionUserResolver(&fakeUserResolver{userID: "u-1"})

	svc.SetRoleGuard(fakeRoleGuard{})

	ctx := withOrg(context.Background(), "org-1")
	resp, err := svc.RecordAuditEvent(ctx, &auditv1.RecordAuditEventRequest{
		OrganizationId: "org-1", ActorUserId: "u-1",
		ActorType: auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER,
		Action:    "auth.login", ResourceType: "session", ResourceId: "s1",
		Result: auditv1.AuditResult_AUDIT_RESULT_SUCCESS,
	})
	require.NoError(t, err)

	// A member can drill into the event.
	got, err := svc.GetAuditEvent(ctx, &auditv1.GetAuditEventRequest{AuditEventId: resp.GetAuditEventId()})
	require.NoError(t, err)
	assert.Equal(t, "auth.login", got.GetAuditEvent().GetAction())

	// A non-member in org-2 is forbidden.
	svc.SetSessionOrgResolver(&fakeOrgResolver{orgID: "org-2"})
	_, err = svc.GetAuditEvent(ctx, &auditv1.GetAuditEventRequest{AuditEventId: resp.GetAuditEventId()})
	assertCode(t, err, apierrors.CodeForbidden)
}

// TestExportAuditEventsRoleGuard covers the role-guard gated export
// path and the org filter.
func TestExportAuditEventsRoleGuard(t *testing.T) {
	svc := newServiceTestEnv(t)
	svc.SetSessionOrgResolver(&fakeOrgResolver{orgID: "org-1"})
	svc.SetSessionUserResolver(&fakeUserResolver{userID: "u-1"})

	svc.SetRoleGuard(fakeRoleGuard{})

	ctx := withOrg(context.Background(), "org-1")
	_, err := svc.RecordAuditEvent(ctx, &auditv1.RecordAuditEventRequest{
		OrganizationId: "org-1", ActorUserId: "u-1",
		ActorType: auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER,
		Action:    "auth.login", ResourceType: "session", ResourceId: "s1",
		Result: auditv1.AuditResult_AUDIT_RESULT_SUCCESS,
	})
	require.NoError(t, err)

	// A member can export org-1 events.
	resp, err := svc.ExportAuditEvents(ctx, &auditv1.ExportAuditEventsRequest{
		Format: auditv1.AuditExportFormat_AUDIT_EXPORT_FORMAT_JSON,
	})
	require.NoError(t, err)
	assert.True(t, strings.Contains(resp.GetContent(), "auth.login"))

	// A non-member in org-2 is forbidden.
	svc.SetSessionOrgResolver(&fakeOrgResolver{orgID: "org-2"})
	_, err = svc.ExportAuditEvents(ctx, &auditv1.ExportAuditEventsRequest{
		Format: auditv1.AuditExportFormat_AUDIT_EXPORT_FORMAT_JSON,
	})
	assertCode(t, err, apierrors.CodeForbidden)
}

// TestResolveErrors covers the org/user resolution error paths.
func TestResolveErrors(t *testing.T) {
	svc := newServiceTestEnv(t)
	// No org header and no resolver -> 10001.
	_, err := svc.ListAuditEvents(context.Background(), &auditv1.ListAuditEventsRequest{})
	assertCode(t, err, apierrors.CodeUnauthorized)

	// No user resolver and no org header -> 10001.
	_, err = svc.ListMyActivity(context.Background(), &auditv1.ListMyActivityRequest{})
	assertCode(t, err, apierrors.CodeUnauthorized)
}
