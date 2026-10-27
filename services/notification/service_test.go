package notification

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	notificationv1 "github.com/go-taas/go-taas/proto/taas/notification/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/audit"
)

func newServiceTestEnv(t *testing.T) *Service {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Notification{}, &NotificationPreference{}, &NotificationThreshold{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	svc := NewForFVT(db)
	svc.SetRoleGuard(fakeRoleGuard{})
	svc.SetSessionUserResolver(&fakeUserResolver{userID: "u-1"})
	svc.SetSessionOrgResolver(&fakeOrgResolver{orgID: "org-1"})
	return svc
}

func withOrg(ctx context.Context, orgID string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs(organizationMetadataKey, orgID))
}

func withAdminPath(ctx context.Context) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set("x-request-path", "/api/v1/admin/notifications")
	return metadata.NewIncomingContext(ctx, md)
}

func withUserPath(ctx context.Context) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set("x-request-path", "/api/v1/notifications")
	return metadata.NewIncomingContext(ctx, md)
}

func assertCode(t *testing.T, err error, code apierrors.Code) {
	t.Helper()
	ae, ok := apierrors.As(err)
	require.True(t, ok, "expected an APIError, got %v", err)
	assert.Equal(t, code, ae.Code)
}

type fakeUserResolver struct{ userID string }

func (f *fakeUserResolver) SessionUserID(context.Context) (string, error) { return f.userID, nil }

type fakeOrgResolver struct{ orgID string }

func (f *fakeOrgResolver) SessionActiveOrg(ctx context.Context) (string, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get(organizationMetadataKey); len(vals) > 0 && vals[0] != "" {
			return vals[0], nil
		}
	}
	return f.orgID, nil
}

type fakeRoleGuard struct{}

func (fakeRoleGuard) RequireRole(_ context.Context, orgID, userID, _ string) error {
	if orgID == "org-1" && userID == "u-1" {
		return nil
	}
	return apierrors.New(apierrors.CodeForbidden)
}

type recordingRecorder struct{ events []*audit.AuditEvent }

func (r *recordingRecorder) Record(_ context.Context, ev *audit.AuditEvent) {
	r.events = append(r.events, ev)
}

func seedNotification(ctx context.Context, t *testing.T, svc *Service, eventType string) string {
	t.Helper()
	repo, err := svc.repository()
	require.NoError(t, err)
	id, err := repo.InsertNotification(ctx, &Notification{
		OrganizationID: "org-1", UserID: "u-1", Surface: SurfaceUser,
		EventType: eventType, Title: "t", Body: "b", Severity: SeverityInfo, Data: `{}`,
	})
	require.NoError(t, err)
	return id
}

// AC1: ListNotifications returns the surface's notifications with
// filters and pagination; GetUnreadCount returns the unread count.
func TestServiceListNotifications(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withUserPath(withOrg(context.Background(), "org-1"))
	seedNotification(ctx, t, svc, "billing.balance_low")
	seedNotification(ctx, t, svc, "billing.invoice_created")

	resp, err := svc.ListNotifications(ctx, &notificationv1.ListNotificationsRequest{
		Page: &commonv1.PageRequest{Offset: 0, Limit: 20},
	})
	require.NoError(t, err)
	assert.Len(t, resp.GetNotifications(), 2)
	assert.Equal(t, int64(2), resp.GetPageMeta().GetTotal())

	// Event filter.
	resp, err = svc.ListNotifications(ctx, &notificationv1.ListNotificationsRequest{
		Page: &commonv1.PageRequest{Offset: 0, Limit: 20}, EventType: "billing.balance_low",
	})
	require.NoError(t, err)
	assert.Len(t, resp.GetNotifications(), 1)

	// Invalid event type -> 11005.
	_, err = svc.ListNotifications(ctx, &notificationv1.ListNotificationsRequest{
		Page: &commonv1.PageRequest{Offset: 0, Limit: 20}, EventType: "deployment.status_changed",
	})
	assertCode(t, err, apierrors.CodeNotificationEventTypeInvalid)

	// Unread count.
	uc, err := svc.GetUnreadCount(ctx, &notificationv1.GetUnreadCountRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(2), uc.GetUnreadCount())
}

// AC2: MarkNotificationRead and MarkAllNotificationsRead; unknown -> 11001.
func TestServiceMarkRead(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withUserPath(withOrg(context.Background(), "org-1"))
	id := seedNotification(ctx, t, svc, "billing.balance_low")
	seedNotification(ctx, t, svc, "billing.invoice_created")

	got, err := svc.MarkNotificationRead(ctx, &notificationv1.MarkNotificationReadRequest{NotificationId: id})
	require.NoError(t, err)
	assert.True(t, got.GetNotification().GetRead())

	_, err = svc.MarkNotificationRead(ctx, &notificationv1.MarkNotificationReadRequest{NotificationId: "00000000-0000-0000-0000-000000000000"})
	assertCode(t, err, apierrors.CodeNotificationNotFound)

	all, err := svc.MarkAllNotificationsRead(ctx, &notificationv1.MarkAllNotificationsReadRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), all.GetMarkedCount())
}

// AC3: DeleteNotification; unknown -> 11001.
func TestServiceDeleteNotification(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withUserPath(withOrg(context.Background(), "org-1"))
	id := seedNotification(ctx, t, svc, "billing.balance_low")
	_, err := svc.DeleteNotification(ctx, &notificationv1.DeleteNotificationRequest{NotificationId: id})
	require.NoError(t, err)
	_, err = svc.DeleteNotification(ctx, &notificationv1.DeleteNotificationRequest{NotificationId: "00000000-0000-0000-0000-000000000000"})
	assertCode(t, err, apierrors.CodeNotificationNotFound)
}

// AC4: GetNotificationPreferences returns all enabled by default;
// UpdateNotificationPreferences persists and validates.
func TestServicePreferences(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withUserPath(withOrg(context.Background(), "org-1"))

	got, err := svc.GetNotificationPreferences(ctx, &notificationv1.GetNotificationPreferencesRequest{})
	require.NoError(t, err)
	assert.Len(t, got.GetPreferences(), 4)
	for _, p := range got.GetPreferences() {
		assert.True(t, p.GetEnabled())
	}

	// Update: disable balance_low.
	upd, err := svc.UpdateNotificationPreferences(ctx, &notificationv1.UpdateNotificationPreferencesRequest{
		Preferences: []*notificationv1.NotificationPreference{
			{EventType: "billing.balance_low", Enabled: false},
			{EventType: "billing.invoice_created", Enabled: true},
			{EventType: "billing.invoice_paid", Enabled: true},
			{EventType: "billing.spend_limit_breached", Enabled: true},
		},
	})
	require.NoError(t, err)
	for _, p := range upd.GetPreferences() {
		if p.GetEventType() == "billing.balance_low" {
			assert.False(t, p.GetEnabled())
		}
	}

	// Event type outside the catalog -> 11005.
	_, err = svc.UpdateNotificationPreferences(ctx, &notificationv1.UpdateNotificationPreferencesRequest{
		Preferences: []*notificationv1.NotificationPreference{{EventType: "deployment.status_changed", Enabled: true}},
	})
	assertCode(t, err, apierrors.CodeNotificationEventTypeInvalid)

	// Malformed set -> 11002.
	_, err = svc.UpdateNotificationPreferences(ctx, &notificationv1.UpdateNotificationPreferencesRequest{
		Preferences: []*notificationv1.NotificationPreference{{EventType: "", Enabled: true}},
	})
	assertCode(t, err, apierrors.CodeNotificationPreferencesInvalid)
}

// AC5: CreateNotificationThreshold valid/invalid; Update/Delete with
// 11003.
func TestServiceThresholds(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withUserPath(withOrg(context.Background(), "org-1"))

	created, err := svc.CreateNotificationThreshold(ctx, &notificationv1.CreateNotificationThresholdRequest{
		Name: "low balance", Metric: "balance_low",
		Operator: notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_LT, Value: 1000, Enabled: true,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, created.GetThreshold().GetThresholdId())

	// Invalid metric -> 11004.
	_, err = svc.CreateNotificationThreshold(ctx, &notificationv1.CreateNotificationThresholdRequest{
		Name: "bad", Metric: "autoscaling_replicas",
		Operator: notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_GT, Value: 1000,
	})
	assertCode(t, err, apierrors.CodeNotificationThresholdInvalid)

	// Invalid value -> 11004.
	_, err = svc.CreateNotificationThreshold(ctx, &notificationv1.CreateNotificationThresholdRequest{
		Name: "bad", Metric: "balance_low",
		Operator: notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_LT, Value: -1,
	})
	assertCode(t, err, apierrors.CodeNotificationThresholdInvalid)

	// List.
	list, err := svc.ListNotificationThresholds(ctx, &notificationv1.ListNotificationThresholdsRequest{
		Page: &commonv1.PageRequest{Offset: 0, Limit: 20},
	})
	require.NoError(t, err)
	assert.Len(t, list.GetThresholds(), 1)

	// Update.
	updated, err := svc.UpdateNotificationThreshold(ctx, &notificationv1.UpdateNotificationThresholdRequest{
		ThresholdId: created.GetThreshold().GetThresholdId(), Enabled: false,
	})
	require.NoError(t, err)
	assert.False(t, updated.GetThreshold().GetEnabled())

	// Unknown -> 11003.
	_, err = svc.UpdateNotificationThreshold(ctx, &notificationv1.UpdateNotificationThresholdRequest{
		ThresholdId: "00000000-0000-0000-0000-000000000000", Enabled: false,
	})
	assertCode(t, err, apierrors.CodeNotificationThresholdNotFound)

	// Delete.
	_, err = svc.DeleteNotificationThreshold(ctx, &notificationv1.DeleteNotificationThresholdRequest{
		ThresholdId: created.GetThreshold().GetThresholdId(),
	})
	require.NoError(t, err)
	_, err = svc.DeleteNotificationThreshold(ctx, &notificationv1.DeleteNotificationThresholdRequest{
		ThresholdId: "00000000-0000-0000-0000-000000000000",
	})
	assertCode(t, err, apierrors.CodeNotificationThresholdNotFound)
}

// AC15: admin org scoping returns 10036 for an inaccessible org.
func TestServiceAdminRoleScoping(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withAdminPath(withOrg(context.Background(), "org-other"))
	_, err := svc.ListNotifications(ctx, &notificationv1.ListNotificationsRequest{
		Page: &commonv1.PageRequest{Offset: 0, Limit: 20},
	})
	assertCode(t, err, apierrors.CodeForbidden)
}

// AD12: preference changes and threshold mutations are audited.
func TestServiceAudit(t *testing.T) {
	svc := newServiceTestEnv(t)
	rec := &recordingRecorder{}
	svc.SetAuditRecorder(rec)
	ctx := withUserPath(withOrg(context.Background(), "org-1"))

	_, err := svc.UpdateNotificationPreferences(ctx, &notificationv1.UpdateNotificationPreferencesRequest{
		Preferences: []*notificationv1.NotificationPreference{{EventType: "billing.balance_low", Enabled: true}},
	})
	require.NoError(t, err)
	_, err = svc.CreateNotificationThreshold(ctx, &notificationv1.CreateNotificationThresholdRequest{
		Name: "low", Metric: "balance_low",
		Operator: notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_LT, Value: 1000,
	})
	require.NoError(t, err)
	assert.Len(t, rec.events, 2)
	assert.Equal(t, "notification.preferences_updated", rec.events[0].Action)
	assert.Equal(t, "notification.threshold_created", rec.events[1].Action)
}

// AC1: GetNotification returns one notification with its full data.
func TestServiceGetNotification(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withUserPath(withOrg(context.Background(), "org-1"))
	id := seedNotification(ctx, t, svc, "billing.balance_low")

	got, err := svc.GetNotification(ctx, &notificationv1.GetNotificationRequest{NotificationId: id})
	require.NoError(t, err)
	assert.Equal(t, "billing.balance_low", got.GetNotification().GetEventType())

	_, err = svc.GetNotification(ctx, &notificationv1.GetNotificationRequest{NotificationId: "00000000-0000-0000-0000-000000000000"})
	assertCode(t, err, apierrors.CodeNotificationNotFound)
}

// AC4: GetNotificationPreferences reflects a persisted preference row.
func TestServiceGetPreferencesWithRow(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withUserPath(withOrg(context.Background(), "org-1"))
	_, err := svc.UpdateNotificationPreferences(ctx, &notificationv1.UpdateNotificationPreferencesRequest{
		Preferences: []*notificationv1.NotificationPreference{
			{EventType: "billing.balance_low", Enabled: false},
			{EventType: "billing.invoice_created", Enabled: true},
			{EventType: "billing.invoice_paid", Enabled: true},
			{EventType: "billing.spend_limit_breached", Enabled: true},
		},
	})
	require.NoError(t, err)
	got, err := svc.GetNotificationPreferences(ctx, &notificationv1.GetNotificationPreferencesRequest{})
	require.NoError(t, err)
	for _, p := range got.GetPreferences() {
		if p.GetEventType() == "billing.balance_low" {
			assert.False(t, p.GetEnabled())
		} else {
			assert.True(t, p.GetEnabled())
		}
	}
}

// AC5: UpdateNotificationThreshold with name/operator/value.
func TestServiceUpdateThresholdFields(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withUserPath(withOrg(context.Background(), "org-1"))
	created, err := svc.CreateNotificationThreshold(ctx, &notificationv1.CreateNotificationThresholdRequest{
		Name: "low", Metric: "balance_low",
		Operator: notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_LT, Value: 1000, Enabled: true,
	})
	require.NoError(t, err)
	id := created.GetThreshold().GetThresholdId()

	updated, err := svc.UpdateNotificationThreshold(ctx, &notificationv1.UpdateNotificationThresholdRequest{
		ThresholdId: id, Name: "renamed",
		Operator: notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_GT, Value: 2000, Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "renamed", updated.GetThreshold().GetName())
	assert.Equal(t, notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_GT, updated.GetThreshold().GetOperator())
	assert.Equal(t, 2000.0, updated.GetThreshold().GetValue())

	// Invalid value -> 11004.
	_, err = svc.UpdateNotificationThreshold(ctx, &notificationv1.UpdateNotificationThresholdRequest{
		ThresholdId: id, Value: -1,
	})
	assertCode(t, err, apierrors.CodeNotificationThresholdInvalid)
}

// AC5: DeleteNotificationThreshold audits the deletion.
func TestServiceDeleteThresholdAudit(t *testing.T) {
	svc := newServiceTestEnv(t)
	rec := &recordingRecorder{}
	svc.SetAuditRecorder(rec)
	ctx := withUserPath(withOrg(context.Background(), "org-1"))
	created, err := svc.CreateNotificationThreshold(ctx, &notificationv1.CreateNotificationThresholdRequest{
		Name: "low", Metric: "balance_low",
		Operator: notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_LT, Value: 1000,
	})
	require.NoError(t, err)
	_, err = svc.DeleteNotificationThreshold(ctx, &notificationv1.DeleteNotificationThresholdRequest{
		ThresholdId: created.GetThreshold().GetThresholdId(),
	})
	require.NoError(t, err)
	assert.Equal(t, "notification.threshold_deleted", rec.events[len(rec.events)-1].Action)
}

// AC5: CreateNotificationThreshold with an empty name -> 11004.
func TestServiceCreateThresholdInvalidName(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withUserPath(withOrg(context.Background(), "org-1"))
	_, err := svc.CreateNotificationThreshold(ctx, &notificationv1.CreateNotificationThresholdRequest{
		Name: "", Metric: "balance_low",
		Operator: notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_LT, Value: 1000,
	})
	assertCode(t, err, apierrors.CodeNotificationThresholdInvalid)
}

// AC5: CreateNotificationThreshold with an invalid operator -> 11004.
func TestServiceCreateThresholdInvalidOperator(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withUserPath(withOrg(context.Background(), "org-1"))
	_, err := svc.CreateNotificationThreshold(ctx, &notificationv1.CreateNotificationThresholdRequest{
		Name: "low", Metric: "balance_low",
		Operator: notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_UNSPECIFIED, Value: 1000,
	})
	assertCode(t, err, apierrors.CodeNotificationThresholdInvalid)
}

// AC2: MarkNotificationRead on an unknown id -> 11001.
func TestServiceMarkReadNotFound(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withUserPath(withOrg(context.Background(), "org-1"))
	_, err := svc.MarkNotificationRead(ctx, &notificationv1.MarkNotificationReadRequest{
		NotificationId: "00000000-0000-0000-0000-000000000000",
	})
	assertCode(t, err, apierrors.CodeNotificationNotFound)
}

// AC1: GetUnreadCount with no notifications returns 0.
func TestServiceUnreadCountEmpty(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withUserPath(withOrg(context.Background(), "org-1"))
	uc, err := svc.GetUnreadCount(ctx, &notificationv1.GetUnreadCountRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(0), uc.GetUnreadCount())
}

// A caller with no active organization - a platform administrator in the
// admin console - still gets an unread count: the badge counts the
// account's notifications across organizations instead of failing.
func TestServiceUnreadCountWithoutActiveOrg(t *testing.T) {
	svc := newServiceTestEnv(t)
	svc.SetSessionOrgResolver(&fakeOrgResolver{})

	// Two unread rows in two organizations for the same account.
	seedNotification(withUserPath(withOrg(context.Background(), "org-1")), t, svc, "billing.balance_low")
	seedNotification(withUserPath(withOrg(context.Background(), "org-2")), t, svc, "billing.invoice_created")

	// No organization in the session and none in the metadata.
	uc, err := svc.GetUnreadCount(withAdminPath(context.Background()), &notificationv1.GetUnreadCountRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(2), uc.GetUnreadCount())
}

// The fallback must not invent a caller: with no session and no
// organization there is nobody to count for.
func TestServiceUnreadCountWithoutOrgOrSession(t *testing.T) {
	svc := newServiceTestEnv(t)
	svc.SetSessionOrgResolver(&fakeOrgResolver{})
	svc.SetSessionUserResolver(&fakeUserResolver{})

	_, err := svc.GetUnreadCount(withAdminPath(context.Background()), &notificationv1.GetUnreadCountRequest{})
	assertCode(t, err, apierrors.CodeUnauthorized)
}

// A session without an active organization (an account that has not joined
// one yet) also gets a count instead of an error.
func TestServiceUnreadCountSessionWithoutActiveOrg(t *testing.T) {
	svc := newServiceTestEnv(t)
	svc.SetSessionOrgResolver(&fakeOrgResolver{orgID: ""})

	seedNotification(withUserPath(withOrg(context.Background(), "org-1")), t, svc, "billing.balance_low")

	uc, err := svc.GetUnreadCount(withAdminPath(context.Background()), &notificationv1.GetUnreadCountRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), uc.GetUnreadCount())
}

// A platform administrator administers every organization, so the
// notification gate cannot rely on the console session's organization
// roster: the caller is not a member of it.
func TestServiceUnreadCountPlatformAdmin(t *testing.T) {
	svc := newServiceTestEnv(t)
	svc.SetSessionOrgResolver(&fakeOrgResolver{orgID: "org-admin"})
	svc.SetRoleGuard(forbiddenRoleGuard{})
	svc.SetSessionRolesResolver(fakeRolesResolver{roles: []string{"platform-admin"}})

	uc, err := svc.GetUnreadCount(withAdminPath(context.Background()), &notificationv1.GetUnreadCountRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(0), uc.GetUnreadCount())
}

// A caller who is not a platform administrator is still gated by the
// organization's roster.
func TestServiceUnreadCountNonAdminForbidden(t *testing.T) {
	svc := newServiceTestEnv(t)
	svc.SetSessionOrgResolver(&fakeOrgResolver{orgID: "org-admin"})
	svc.SetRoleGuard(forbiddenRoleGuard{})
	svc.SetSessionRolesResolver(fakeRolesResolver{roles: []string{"user"}})

	_, err := svc.GetUnreadCount(withAdminPath(context.Background()), &notificationv1.GetUnreadCountRequest{})
	assertCode(t, err, apierrors.CodeForbidden)
}

type forbiddenRoleGuard struct{}

func (forbiddenRoleGuard) RequireRole(context.Context, string, string, string) error {
	return apierrors.New(apierrors.CodeForbidden)
}

type fakeRolesResolver struct{ roles []string }

func (f fakeRolesResolver) SessionRoles(context.Context) ([]string, error) { return f.roles, nil }
