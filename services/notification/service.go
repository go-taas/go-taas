// Package notification implements the in-console notification center:
// notification persistence with read/unread state, per-user event-type
// preferences, configurable threshold alerts, and the event consumer
// that fans out the platform event catalog to enabled users (feature
// #26).
package notification

import (
	"context"
	"math"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	notificationv1 "github.com/go-taas/go-taas/proto/taas/notification/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/tenancy"
)

// ServiceName is the unique name of this service.
const ServiceName = "notification"

// Pagination bounds (architecture §5.2).
const (
	listDefaultLimit = 20
	listMaxLimit     = 100
)

// Field length limits (architecture §5.2).
const (
	maxNameLen = 64
)

// organizationMetadataKey is the gRPC metadata key carrying the
// transitional caller organization, set by the gateway from the
// X-Organization-Id HTTP header (the auth module's pattern).
const organizationMetadataKey = "x-organization-id"

// SessionOrgResolver resolves the session's active organization
// (feature-17 AD6). It is implemented by the auth module and injected at
// wiring time. Nil until wired: the transitional X-Organization-Id
// header is used.
type SessionOrgResolver interface {
	// SessionActiveOrg returns the session's active organization, or
	// ("", nil) when no session is present (transitional access).
	SessionActiveOrg(ctx context.Context) (string, error)
}

// SessionUserResolver resolves the authenticated caller's user id from
// the session (feature #10). It is implemented by the auth module and
// injected at wiring time.
type SessionUserResolver interface {
	// SessionUserID returns the authenticated caller's user id.
	SessionUserID(ctx context.Context) (string, error)
}

// RoleGuard enforces the minimum org role on the admin notification
// RPCs. It is implemented by tenancy.RoleGuard.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// roleMember is the minimum org role for the admin notification RPCs.
const roleMember = "member"

// AuditRecorder is the best-effort, non-fatal audit recorder seam
// (feature #15, AD3). It is implemented by the audit module and injected
// at wiring time.
type AuditRecorder interface {
	// Record writes one audit event best-effort; it never returns an
	// error.
	Record(ctx context.Context, ev *audit.AuditEvent)
}

// Service implements the notification gRPC service.
type Service struct {
	notificationv1.UnimplementedNotificationServiceServer

	components server.Components

	// repo is the injection point used by tests and FVT; production
	// resolves it lazily from the shared components.
	repo *Repository

	// sessionRolesResolver resolves the caller's session roles; a platform
	// administrator reaches the notification surface of every organization
	// (feature #54).
	sessionRolesResolver SessionRolesResolver
	// sessionOrgResolver resolves the session's active organization for
	// the admin-realm reads (feature-17 AD6). Nil until wired: the
	// transitional X-Organization-Id header is used.
	sessionOrgResolver SessionOrgResolver

	// sessionUserResolver resolves the caller's user id for the admin
	// role check (feature #10). Nil until wired: no role check.
	sessionUserResolver SessionUserResolver

	// roleGuard gates the admin notification RPCs by the caller's role in
	// the resolved org context (feature #10, AD7). Nil until wired: no
	// role check (unit tests).
	roleGuard RoleGuard

	// auditRecorder is the best-effort audit recorder (feature #15, AD3).
	// Nil until wired: no audit events are produced.
	auditRecorder AuditRecorder
}

// New constructs the notification service. The repository is wired
// lazily on first use from the shared components.
func New(components server.Components) *Service {
	return &Service{components: components}
}

// SessionRolesResolver resolves the authenticated caller's session roles.
// The notification service needs only the platform-administrator flag.
type SessionRolesResolver interface {
	SessionRoles(ctx context.Context) ([]string, error)
}

// SetSessionRolesResolver injects the session-roles resolver used to
// recognise a platform administrator.
func (s *Service) SetSessionRolesResolver(r SessionRolesResolver) { s.sessionRolesResolver = r }

// SetSessionOrgResolver injects the session-organization resolver used
// by the admin-realm reads (feature-17 AD6).
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver used by the
// admin role check (feature #10).
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard that gates the admin
// notification RPCs by the caller's role (feature #10, AD7).
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// SetAuditRecorder injects the best-effort audit recorder (feature #15,
// AD3).
func (s *Service) SetAuditRecorder(r AuditRecorder) { s.auditRecorder = r }

// NewForFVT constructs a notification service bound to a caller-provided
// GORM database. It exists so full-verification tests can wire the real
// service stack against a disposable database.
func NewForFVT(db *gorm.DB) *Service {
	return &Service{repo: NewRepository(db)}
}

// MigrateSchemaForFVT applies the notification schema (notifications,
// notification_preferences, notification_thresholds) onto a
// caller-provided database for full-verification tests.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&Notification{}, &NotificationPreference{}, &NotificationThreshold{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	notificationv1.RegisterNotificationServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return notificationv1.RegisterNotificationServiceHandler
}

// Migrate implements server.Migrator: it creates/updates the
// notifications, notification_preferences and notification_thresholds
// tables via GORM AutoMigrate. There is nothing to seed.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&Notification{}, &NotificationPreference{}, &NotificationThreshold{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "notification: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "notification: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the notification repository.
func (s *Service) repository() (*Repository, error) {
	if s.repo != nil {
		return s.repo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.repo = NewRepository(db)
	return s.repo, nil
}

// resolveOrg returns the organization context for a read (feature-17
// AD6): the session's active org when a session is present, otherwise
// the transitional X-Organization-Id header.
func (s *Service) resolveOrg(ctx context.Context) (string, error) {
	if s.sessionOrgResolver != nil {
		if org, err := s.sessionOrgResolver.SessionActiveOrg(ctx); err != nil {
			return "", err
		} else if org != "" {
			return org, nil
		}
	}
	return resolveOrganizationID(ctx)
}

// resolveOrganizationID reads the transitional caller organization from
// the x-organization-id gRPC metadata (set by the gateway from the
// X-Organization-Id HTTP header). Missing or empty values are
// unauthorized (the auth module's pattern).
func resolveOrganizationID(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	values := md.Get(organizationMetadataKey)
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	return strings.TrimSpace(values[0]), nil
}

// resolveSessionActor returns the caller's user id and whether a session
// is present.
func (s *Service) resolveSessionActor(ctx context.Context) (string, bool, error) {
	if s.sessionUserResolver == nil {
		return "", false, nil
	}
	userID, err := s.sessionUserResolver.SessionUserID(ctx)
	if err == nil && userID != "" {
		return userID, true, nil
	}
	if err != nil && apierrors.CodeOf(err) != apierrors.CodeSessionInvalid {
		return "", false, err
	}
	return "", false, nil
}

// requireAdminRole enforces the minimum org role on the admin
// notification RPCs. The RoleGuard resolves the caller from a session;
// in the transitional (session-less) path there is no session user to
// check, so the check is skipped and the org header itself is the access
// boundary (feature-17 AD6).
func (s *Service) requireAdminRole(ctx context.Context, orgID string) error {
	if s.roleGuard == nil || orgID == "" {
		return nil
	}
	userID, hasSession, err := s.resolveSessionActor(ctx)
	if err != nil {
		return err
	}
	if !hasSession {
		return nil
	}
	// A platform administrator administers every organization, so the
	// organization's roster cannot be the gate for their own notifications
	// either: the console session may carry an organization they are not a
	// member of.
	if s.isPlatformAdmin(ctx) {
		return nil
	}
	return s.roleGuard.RequireRole(ctx, orgID, userID, roleMember)
}

// isPlatformAdmin reports whether the caller's session carries the
// platform-administrator role. Without a roles resolver it is false, so a
// unit test is never accidentally treated as an operator.
func (s *Service) isPlatformAdmin(ctx context.Context) bool {
	if s.sessionRolesResolver == nil {
		return false
	}
	roles, err := s.sessionRolesResolver.SessionRoles(ctx)
	if err != nil {
		return false
	}
	for _, r := range roles {
		if r == tenancy.RolePlatformAdmin {
			return true
		}
	}
	return false
}

// surfaceFromContext derives the notification surface from the request
// path. The surface is a property of the binding, never a request field
// (feature #26, §3.3). The gateway forwards the request path in the
// x-request-path metadata; the admin prefix maps to "admin", everything
// else to "user".
func surfaceFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return SurfaceUser
	}
	paths := md.Get("x-request-path")
	if len(paths) > 0 && strings.HasPrefix(paths[0], "/api/v1/admin/") {
		return SurfaceAdmin
	}
	return SurfaceUser
}

// recordAudit writes one audit event best-effort (feature #15, AD3).
func (s *Service) recordAudit(ctx context.Context, ev *audit.AuditEvent) {
	if s.auditRecorder == nil {
		return
	}
	s.auditRecorder.Record(ctx, ev)
}

// okResponse is the success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}

// clampInt32 clamps v into the int32 range so proto fields never
// overflow on 32-bit hosts.
func clampInt32(v int) int32 {
	if v < math.MinInt32 {
		return math.MinInt32
	}
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v)
}

// pageBounds returns the offset/limit from a PageRequest with defaults.
func pageBounds(page *commonv1.PageRequest) (int, int) {
	offset := 0
	limit := listDefaultLimit
	if page != nil {
		if page.GetOffset() > 0 {
			offset = int(page.GetOffset())
		}
		if page.GetLimit() > 0 {
			limit = int(page.GetLimit())
		}
	}
	if limit > listMaxLimit {
		limit = listMaxLimit
	}
	return offset, limit
}

// summarizeNotification maps a Notification row to the proto message.
func summarizeNotification(n *Notification) *notificationv1.Notification {
	severity := notificationv1.NotificationSeverity_NOTIFICATION_SEVERITY_INFO
	switch n.Severity {
	case SeverityWarning:
		severity = notificationv1.NotificationSeverity_NOTIFICATION_SEVERITY_WARNING
	case SeverityCritical:
		severity = notificationv1.NotificationSeverity_NOTIFICATION_SEVERITY_CRITICAL
	}
	return &notificationv1.Notification{
		NotificationId: n.NotificationID,
		OrganizationId: n.OrganizationID,
		UserId:         n.UserID,
		Surface:        n.Surface,
		EventType:      n.EventType,
		Title:          n.Title,
		Body:           n.Body,
		Severity:       severity,
		Read:           n.Read,
		Data:           n.Data,
		Link:           n.Link,
		CreatedAt:      n.CreatedAt.Unix(),
	}
}

// summarizeThreshold maps a NotificationThreshold row to the proto
// message.
func summarizeThreshold(t *NotificationThreshold) *notificationv1.NotificationThreshold {
	op := notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_GT
	if t.Operator == OperatorLT {
		op = notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_LT
	}
	return &notificationv1.NotificationThreshold{
		ThresholdId:    t.ThresholdID,
		OrganizationId: t.OrganizationID,
		UserId:         t.UserID,
		Surface:        t.Surface,
		Name:           t.Name,
		Metric:         t.Metric,
		Operator:       op,
		Value:          t.Value,
		Enabled:        t.Enabled,
		CreatedAt:      t.CreatedAt.Unix(),
		UpdatedAt:      t.UpdatedAt.Unix(),
	}
}

// ListNotifications returns the surface's notifications, newest first,
// filterable by read state and event type, and paginated (AC1).
func (s *Service) ListNotifications(ctx context.Context, req *notificationv1.ListNotificationsRequest) (*notificationv1.ListNotificationsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	surface := surfaceFromContext(ctx)
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	userID, _, err := s.resolveSessionActor(ctx)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		// Transitional (session-less) path: no per-user inbox. Use the
		// org as the user scope so the transitional CLI/FVT can list.
		userID = orgID
	}
	eventType := strings.TrimSpace(req.GetEventType())
	if eventType != "" && !containsString(eventTypesForSurface(surface), eventType) {
		return nil, apierrors.New(apierrors.CodeNotificationEventTypeInvalid)
	}
	offset, limit := pageBounds(req.GetPage())
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	rows, total, err := repo.ListNotifications(ctx, NotificationFilter{
		OrganizationID: orgID,
		UserID:         userID,
		ReadFilter:     req.GetReadFilter(),
		Read:           req.GetRead(),
		EventType:      eventType,
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}
	notifications := make([]*notificationv1.Notification, 0, len(rows))
	for _, n := range rows {
		notifications = append(notifications, summarizeNotification(n))
	}
	return &notificationv1.ListNotificationsResponse{
		Response:      okResponse(),
		Notifications: notifications,
		PageMeta:      &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// GetNotification returns one notification with its full data payload
// (AC1).
func (s *Service) GetNotification(ctx context.Context, req *notificationv1.GetNotificationRequest) (*notificationv1.GetNotificationResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	userID, _, err := s.resolveSessionActor(ctx)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		userID = orgID
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	n, err := repo.FindNotificationByID(ctx, orgID, userID, req.GetNotificationId())
	if err != nil {
		return nil, err
	}
	return &notificationv1.GetNotificationResponse{
		Response:     okResponse(),
		Notification: summarizeNotification(n),
	}, nil
}

// MarkNotificationRead marks one notification read (AC2).
func (s *Service) MarkNotificationRead(ctx context.Context, req *notificationv1.MarkNotificationReadRequest) (*notificationv1.MarkNotificationReadResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	userID, _, err := s.resolveSessionActor(ctx)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		userID = orgID
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	n, err := repo.MarkNotificationRead(ctx, orgID, userID, req.GetNotificationId())
	if err != nil {
		return nil, err
	}
	return &notificationv1.MarkNotificationReadResponse{
		Response:     okResponse(),
		Notification: summarizeNotification(n),
	}, nil
}

// MarkAllNotificationsRead marks all of the caller's notifications read
// (AC2).
func (s *Service) MarkAllNotificationsRead(ctx context.Context, _ *notificationv1.MarkAllNotificationsReadRequest) (*notificationv1.MarkAllNotificationsReadResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	userID, _, err := s.resolveSessionActor(ctx)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		userID = orgID
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	count, err := repo.MarkAllNotificationsRead(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	return &notificationv1.MarkAllNotificationsReadResponse{
		Response:    okResponse(),
		MarkedCount: count,
	}, nil
}

// DeleteNotification deletes one notification (AC3).
func (s *Service) DeleteNotification(ctx context.Context, req *notificationv1.DeleteNotificationRequest) (*notificationv1.DeleteNotificationResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	userID, _, err := s.resolveSessionActor(ctx)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		userID = orgID
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if err := repo.DeleteNotification(ctx, orgID, userID, req.GetNotificationId()); err != nil {
		return nil, err
	}
	return &notificationv1.DeleteNotificationResponse{Response: okResponse()}, nil
}

// GetUnreadCount returns the caller's unread count for the bell badge
// (AC1).
func (s *Service) GetUnreadCount(ctx context.Context, _ *notificationv1.GetUnreadCountRequest) (*notificationv1.GetUnreadCountResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	var unscoped bool
	// The badge is a shell element, so it must not fail the page for an
	// account that has not joined an organization yet: the count then covers
	// the caller's own notifications across organizations. A caller with no
	// session and no organization header still has no identity to count for.
	switch apierrors.CodeOf(err) {
	case apierrors.CodeUnauthorized, apierrors.CodeOrganizationNotFound:
		unscoped = true
	case apierrors.Code(0):
	default:
		return nil, err
	}
	if !unscoped {
		if err := s.requireAdminRole(ctx, orgID); err != nil {
			return nil, err
		}
	}
	userID, _, err := s.resolveSessionActor(ctx)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		if unscoped {
			return nil, apierrors.New(apierrors.CodeUnauthorized)
		}
		userID = orgID
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	var count int64
	if unscoped {
		count, err = repo.CountUnreadForUser(ctx, userID)
	} else {
		count, err = repo.CountUnread(ctx, orgID, userID)
	}
	if err != nil {
		return nil, err
	}
	return &notificationv1.GetUnreadCountResponse{
		Response:    okResponse(),
		UnreadCount: count,
	}, nil
}

// GetNotificationPreferences returns the caller's preferences: for each
// event type in the surface's catalog, an enabled boolean (default all
// enabled) (AC4).
func (s *Service) GetNotificationPreferences(ctx context.Context, _ *notificationv1.GetNotificationPreferencesRequest) (*notificationv1.GetNotificationPreferencesResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	surface := surfaceFromContext(ctx)
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	userID, _, err := s.resolveSessionActor(ctx)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		userID = orgID
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetPreferences(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	enabled := make(map[string]bool)
	if row != nil {
		types, err := row.EventTypes()
		if err == nil {
			for _, t := range types {
				enabled[t] = true
			}
		}
	}
	catalog := eventTypesForSurface(surface)
	prefs := make([]*notificationv1.NotificationPreference, 0, len(catalog))
	for _, t := range catalog {
		// A user with no preference row defaults to all enabled (AD4).
		prefs = append(prefs, &notificationv1.NotificationPreference{
			EventType: t,
			Enabled:   enabled[t] || row == nil,
		})
	}
	return &notificationv1.GetNotificationPreferencesResponse{
		Response:    okResponse(),
		Preferences: prefs,
	}, nil
}

// UpdateNotificationPreferences updates the caller's preferences (AC4).
func (s *Service) UpdateNotificationPreferences(ctx context.Context, req *notificationv1.UpdateNotificationPreferencesRequest) (*notificationv1.UpdateNotificationPreferencesResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	surface := surfaceFromContext(ctx)
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	userID, _, err := s.resolveSessionActor(ctx)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		userID = orgID
	}
	catalog := eventTypesForSurface(surface)
	enabled := make([]string, 0, len(req.GetPreferences()))
	for _, p := range req.GetPreferences() {
		t := strings.TrimSpace(p.GetEventType())
		if t == "" {
			return nil, apierrors.New(apierrors.CodeNotificationPreferencesInvalid)
		}
		if !containsString(catalog, t) {
			return nil, apierrors.New(apierrors.CodeNotificationEventTypeInvalid)
		}
		if p.GetEnabled() {
			enabled = append(enabled, t)
		}
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if err := repo.UpsertPreferences(ctx, orgID, userID, enabled); err != nil {
		return nil, err
	}
	// Feature #15 (AD12): preference changes are audited.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    userID,
		ActorType:      "user",
		Action:         "notification.preferences_updated",
		ResourceType:   "notification_preferences",
		ResourceID:     userID,
		Result:         "success",
	})
	// Return the full catalog with the updated enabled state.
	enabledSet := make(map[string]bool)
	for _, t := range enabled {
		enabledSet[t] = true
	}
	prefs := make([]*notificationv1.NotificationPreference, 0, len(catalog))
	for _, t := range catalog {
		prefs = append(prefs, &notificationv1.NotificationPreference{
			EventType: t,
			Enabled:   enabledSet[t],
		})
	}
	return &notificationv1.UpdateNotificationPreferencesResponse{
		Response:    okResponse(),
		Preferences: prefs,
	}, nil
}

// CreateNotificationThreshold creates a threshold from name, metric,
// operator, and value (AC5).
func (s *Service) CreateNotificationThreshold(ctx context.Context, req *notificationv1.CreateNotificationThresholdRequest) (*notificationv1.CreateNotificationThresholdResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	surface := surfaceFromContext(ctx)
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	userID, _, err := s.resolveSessionActor(ctx)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		userID = orgID
	}
	th, err := validateThreshold(surface, req.GetName(), req.GetMetric(), req.GetOperator(), req.GetValue())
	if err != nil {
		return nil, err
	}
	enabled := req.GetEnabled()
	if !req.GetEnabled() && req.GetValue() == 0 && req.GetName() == "" {
		enabled = true
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row := &NotificationThreshold{
		OrganizationID: orgID,
		UserID:         userID,
		Surface:        surface,
		Name:           th.name,
		Metric:         th.metric,
		Operator:       th.operator,
		Value:          th.value,
		Enabled:        enabled,
	}
	if _, err := repo.InsertThreshold(ctx, row); err != nil {
		return nil, err
	}
	// Feature #15 (AD12): threshold creation is audited.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    userID,
		ActorType:      "user",
		Action:         "notification.threshold_created",
		ResourceType:   "notification_threshold",
		ResourceID:     row.ThresholdID,
		Result:         "success",
	})
	return &notificationv1.CreateNotificationThresholdResponse{
		Response:  okResponse(),
		Threshold: summarizeThreshold(row),
	}, nil
}

// ListNotificationThresholds returns the caller's thresholds, filterable
// by enabled state (AC5).
func (s *Service) ListNotificationThresholds(ctx context.Context, req *notificationv1.ListNotificationThresholdsRequest) (*notificationv1.ListNotificationThresholdsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	userID, _, err := s.resolveSessionActor(ctx)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		userID = orgID
	}
	offset, limit := pageBounds(req.GetPage())
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	rows, total, err := repo.ListThresholds(ctx, ThresholdFilter{
		OrganizationID: orgID,
		UserID:         userID,
		EnabledFilter:  req.GetEnabledFilter(),
		Enabled:        req.GetEnabled(),
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}
	thresholds := make([]*notificationv1.NotificationThreshold, 0, len(rows))
	for _, t := range rows {
		thresholds = append(thresholds, summarizeThreshold(t))
	}
	return &notificationv1.ListNotificationThresholdsResponse{
		Response:   okResponse(),
		Thresholds: thresholds,
		PageMeta:   &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// UpdateNotificationThreshold updates the threshold's name, operator,
// value, or enabled state (AC5).
func (s *Service) UpdateNotificationThreshold(ctx context.Context, req *notificationv1.UpdateNotificationThresholdRequest) (*notificationv1.UpdateNotificationThresholdResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	surface := surfaceFromContext(ctx)
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	userID, _, err := s.resolveSessionActor(ctx)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		userID = orgID
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.FindThresholdByID(ctx, orgID, userID, req.GetThresholdId())
	if err != nil {
		return nil, err
	}
	// Validate the mutable fields (name/operator/value) when provided.
	if req.GetName() != "" || req.GetOperator() != notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_UNSPECIFIED || req.GetValue() != 0 {
		th, verr := validateThreshold(surface, req.GetName(), row.Metric, req.GetOperator(), req.GetValue())
		if verr != nil {
			return nil, verr
		}
		if req.GetName() != "" {
			row.Name = th.name
		}
		if req.GetOperator() != notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_UNSPECIFIED {
			row.Operator = th.operator
		}
		if req.GetValue() != 0 {
			row.Value = th.value
		}
	}
	row.Enabled = req.GetEnabled()
	if err := repo.UpdateThreshold(ctx, row); err != nil {
		return nil, err
	}
	// Feature #15 (AD12): threshold updates are audited.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    userID,
		ActorType:      "user",
		Action:         "notification.threshold_updated",
		ResourceType:   "notification_threshold",
		ResourceID:     row.ThresholdID,
		Result:         "success",
	})
	return &notificationv1.UpdateNotificationThresholdResponse{
		Response:  okResponse(),
		Threshold: summarizeThreshold(row),
	}, nil
}

// DeleteNotificationThreshold deletes the threshold (AC5).
func (s *Service) DeleteNotificationThreshold(ctx context.Context, req *notificationv1.DeleteNotificationThresholdRequest) (*notificationv1.DeleteNotificationThresholdResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	userID, _, err := s.resolveSessionActor(ctx)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		userID = orgID
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if err := repo.DeleteThreshold(ctx, orgID, userID, req.GetThresholdId()); err != nil {
		return nil, err
	}
	// Feature #15 (AD12): threshold deletion is audited.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    userID,
		ActorType:      "user",
		Action:         "notification.threshold_deleted",
		ResourceType:   "notification_threshold",
		ResourceID:     req.GetThresholdId(),
		Result:         "success",
	})
	return &notificationv1.DeleteNotificationThresholdResponse{Response: okResponse()}, nil
}

// thresholdSpec is the validated threshold fields.
type thresholdSpec struct {
	name     string
	metric   string
	operator string
	value    float64
}

// validateThreshold validates the threshold fields against the surface's
// metric set (AC5). An invalid metric/operator/value returns 11004.
func validateThreshold(surface, name, metric string, op notificationv1.ThresholdOperator, value float64) (*thresholdSpec, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxNameLen {
		return nil, apierrors.New(apierrors.CodeNotificationThresholdInvalid)
	}
	if !containsString(metricsForSurface(surface), metric) {
		return nil, apierrors.New(apierrors.CodeNotificationThresholdInvalid)
	}
	var operator string
	switch op {
	case notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_LT:
		operator = OperatorLT
	case notificationv1.ThresholdOperator_THRESHOLD_OPERATOR_GT:
		operator = OperatorGT
	default:
		return nil, apierrors.New(apierrors.CodeNotificationThresholdInvalid)
	}
	if value <= 0 {
		return nil, apierrors.New(apierrors.CodeNotificationThresholdInvalid)
	}
	return &thresholdSpec{name: name, metric: metric, operator: operator, value: value}, nil
}
