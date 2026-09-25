// Package audit implements the audit service: recording control-plane
// mutations as audit events, serving the admin audit trail and the
// end-user activity list, and enforcing retention (feature #15).
package audit

import (
	"context"
	"math"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	auditv1 "github.com/go-taas/go-taas/proto/taas/audit/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// ServiceName is the unique name of this service.
const ServiceName = "audit"

// Pagination and range bounds (architecture Sections 5.2).
const (
	listDefaultLimit  = 20
	listMaxLimit      = 100
	maxRangeSeconds   = 366 * 24 * 3600 // 366 days
	defaultRangeHours = 24
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
// injected at wiring time. Nil until wired: the end-user activity RPCs
// fall back to the transitional X-Organization-Id header.
type SessionUserResolver interface {
	// SessionUserID returns the authenticated caller's user id.
	SessionUserID(ctx context.Context) (string, error)
}

// Service implements the audit gRPC service.
type Service struct {
	auditv1.UnimplementedAuditServiceServer

	components server.Components

	// repo is the injection point used by tests and FVT; production
	// resolves it lazily from the shared components.
	repo *Repository

	// sessionOrgResolver resolves the session's active organization for
	// the admin-realm reads (feature-17 AD6). Nil until wired: the
	// transitional X-Organization-Id header is used.
	sessionOrgResolver SessionOrgResolver

	// sessionUserResolver resolves the caller's user id for the
	// end-user activity reads (feature #10). Nil until wired: the
	// transitional header is used.
	sessionUserResolver SessionUserResolver

	// roleGuard gates the admin audit RPCs by the caller's role in the
	// resolved org context (feature #10, AD7). Nil until wired: no role
	// check (unit tests). It is an interface (implemented by
	// tenancy.RoleGuard) so the audit module does not import tenancy,
	// which imports audit for its own recorder seam (feature #15).
	roleGuard RoleGuard

	// exportMaxRows is the export row cap (AD7). 0 means the configured
	// default (10000).
	exportMaxRows int
}

// New constructs the audit service. The repository is wired lazily on
// first use from the shared components.
func New(components server.Components) *Service {
	return &Service{components: components}
}

// SetSessionOrgResolver injects the session-organization resolver used
// by the admin-realm reads (feature-17 AD6).
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver used by the
// end-user activity reads (feature #10).
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// RoleGuard enforces the minimum org role on the admin audit RPCs. It
// is implemented by tenancy.RoleGuard.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// roleMember is the minimum org role for the admin audit reads.
const roleMember = "member"

// SetRoleGuard injects the org role guard that gates the admin audit
// RPCs by the caller's role (feature #10, AD7).
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// SetExportMaxRows overrides the export row cap (AD7). Tests use it to
// exercise the truncated path without a 10,000-row fixture.
func (s *Service) SetExportMaxRows(n int) { s.exportMaxRows = n }

// NewForFVT constructs an audit service bound to a caller-provided GORM
// database. It exists so full-verification tests can wire the real
// service stack against a disposable database.
func NewForFVT(db *gorm.DB) *Service {
	return &Service{repo: NewRepository(db)}
}

// MigrateSchemaForFVT applies the audit schema (audit_events) onto a
// caller-provided database for full-verification tests.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&AuditEvent{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	auditv1.RegisterAuditServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return auditv1.RegisterAuditServiceHandler
}

// Migrate implements server.Migrator: it creates/updates the
// audit_events table via GORM AutoMigrate. There is nothing to seed.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&AuditEvent{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "audit: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "audit: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the audit repository.
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

// resolveOrg returns the organization context for an admin-realm read
// (feature-17 AD6): the session's active org when a session is present,
// otherwise the transitional X-Organization-Id header.
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

// resolveActorUserID returns the caller's user id for the end-user
// activity reads and the admin role check. When a session resolver is
// wired and a session is present it is authoritative; in the
// transitional (session-less) path the X-Organization-Id header names
// the caller (the pattern the other services use for feature-17 AD6).
func (s *Service) resolveActorUserID(ctx context.Context) (string, error) {
	userID, hasSession, err := s.resolveSessionActor(ctx)
	if err != nil {
		return "", err
	}
	if hasSession {
		return userID, nil
	}
	return resolveOrganizationID(ctx)
}

// resolveSessionActor returns the session actor's user id and whether a
// session was present. In the transitional (session-less) path it
// returns ("", false, nil); a session-invalid answer is treated as "no
// session"; any other failure propagates.
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

// requireAdminRole enforces the minimum org role on the admin audit RPCs.
// The RoleGuard resolves the caller from a session (a real user
// identity); in the transitional (session-less) path there is no session
// user to check, so the check is skipped and the org header itself is
// the access boundary (feature-17 AD6).
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
	return s.roleGuard.RequireRole(ctx, orgID, userID, roleMember)
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

// normalizePagination clamps the page request: offset >= 0, limit
// defaults to 20 when unset or non-positive, capped at 100.
func normalizePagination(page *commonv1.PageRequest) (offset, limit int) {
	offset = 0
	if page != nil && page.GetOffset() > 0 {
		offset = int(page.GetOffset())
	}
	limit = listDefaultLimit
	if page != nil && page.GetLimit() > 0 {
		limit = int(page.GetLimit())
	}
	if limit > listMaxLimit {
		limit = listMaxLimit
	}
	return offset, limit
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

// validateRange checks and defaults the since/until pair: until
// defaults to now+1s (so events created in the current second are
// included), since to until-24h; since > until or a range > 366 days
// returns 10603 (FR3.1/FR4.1).
func validateRange(since, until int64) (int64, int64, error) {
	if until <= 0 {
		until = time.Now().Unix() + 1
	}
	if since <= 0 {
		since = until - defaultRangeHours*3600
	}
	if since > until {
		return 0, 0, apierrors.New(apierrors.CodeAuditRangeInvalid)
	}
	if until-since > maxRangeSeconds {
		return 0, 0, apierrors.New(apierrors.CodeAuditRangeInvalid)
	}
	return since, until, nil
}

// exportMaxRowsFor returns the export row cap, defaulting to 10000 when
// unset (AD7).
func (s *Service) exportMaxRowsFor() int {
	if s.exportMaxRows > 0 {
		return s.exportMaxRows
	}
	return 10000
}

// RecordAuditEvent is the best-effort, non-fatal recorder invoked by
// every mutating control-plane API after the mutation succeeds (AD3).
// It has no HTTP binding (cluster-internal surface).
func (s *Service) RecordAuditEvent(ctx context.Context, req *auditv1.RecordAuditEventRequest) (*auditv1.RecordAuditEventResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	// A stored event always has a concrete result; an unspecified enum
	// defaults to success (the common case).
	result := resultString(req.GetResult())
	if result == "" {
		result = "success"
	}
	ev := &AuditEvent{
		OrganizationID: req.GetOrganizationId(),
		ActorUserID:    req.GetActorUserId(),
		ActorType:      actorTypeString(req.GetActorType()),
		Action:         req.GetAction(),
		ResourceType:   req.GetResourceType(),
		ResourceID:     req.GetResourceId(),
		Result:         result,
		IPAddress:      req.GetIpAddress(),
		UserAgent:      req.GetUserAgent(),
		Metadata:       req.GetMetadata(),
	}
	id, err := repo.InsertAuditEvent(ctx, ev)
	if err != nil {
		return nil, err
	}
	return &auditv1.RecordAuditEventResponse{
		Response:     okResponse(),
		AuditEventId: id,
	}, nil
}

// ListAuditEvents returns audit events across tenants, filtered by
// org, actor, action, resource type, result and time range, paginated
// newest-first (FR3.1). Admin-surface API.
func (s *Service) ListAuditEvents(ctx context.Context, req *auditv1.ListAuditEventsRequest) (*auditv1.ListAuditEventsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	// Admin org scoping (AC8): the caller may only see events for orgs
	// they can access. When a session is present, the session's active
	// org is authoritative; the RoleGuard enforces the minimum role.
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	since, until, err := validateRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	// Org isolation (AC8): when the request does not name an org, scope
	// the list to the caller's resolved org so a caller never sees
	// events for orgs they cannot access.
	orgFilter := req.GetOrganizationId()
	if orgFilter == "" {
		orgFilter = orgID
	}

	rows, total, err := repo.ListAuditEvents(ctx, AuditEventFilter{
		OrganizationID: orgFilter,
		ActorUserID:    req.GetActorUserId(),
		Action:         req.GetAction(),
		ResourceType:   req.GetResourceType(),
		Result:         resultString(req.GetResult()),
		Since:          since,
		Until:          until,
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}
	events := make([]*auditv1.AuditEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, summarizeEvent(row))
	}
	return &auditv1.ListAuditEventsResponse{
		Response:    okResponse(),
		AuditEvents: events,
		PageMeta:    &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// GetAuditEvent returns one audit event's full metadata for drill-down
// (FR3.2). Admin-surface API. Unknown ids return 10601 (AC5).
func (s *Service) GetAuditEvent(ctx context.Context, req *auditv1.GetAuditEventRequest) (*auditv1.GetAuditEventResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.FindAuditEventByID(ctx, req.GetAuditEventId())
	if err != nil {
		return nil, err
	}
	return &auditv1.GetAuditEventResponse{
		Response:   okResponse(),
		AuditEvent: summarizeEvent(row),
	}, nil
}

// ExportAuditEvents returns the filtered set as CSV or JSON, capped at
// the export row cap (FR3.3, AD7). Admin-surface API. An invalid format
// returns 10602; a set wider than the cap returns the first cap rows
// with truncated: true (AC6).
func (s *Service) ExportAuditEvents(ctx context.Context, req *auditv1.ExportAuditEventsRequest) (*auditv1.ExportAuditEventsResponse, error) {
	// Validate the export format first so an invalid format is rejected
	// before any org/role resolution (AC6).
	if req.GetFormat() != auditv1.AuditExportFormat_AUDIT_EXPORT_FORMAT_CSV &&
		req.GetFormat() != auditv1.AuditExportFormat_AUDIT_EXPORT_FORMAT_JSON {
		return nil, apierrors.New(apierrors.CodeAuditExportInvalid)
	}
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	since, until, err := validateRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	// Fetch maxRows+1 so the cap can be detected (AD7).
	// Org isolation (AC8): default the org filter to the resolved org.
	orgFilter := req.GetOrganizationId()
	if orgFilter == "" {
		orgFilter = orgID
	}
	rows, _, err := repo.ListAuditEvents(ctx, AuditEventFilter{
		OrganizationID: orgFilter,
		ActorUserID:    req.GetActorUserId(),
		Action:         req.GetAction(),
		ResourceType:   req.GetResourceType(),
		Result:         resultString(req.GetResult()),
		Since:          since,
		Until:          until,
		Offset:         0,
		Limit:          s.exportMaxRowsFor() + 1,
	})
	if err != nil {
		return nil, err
	}
	capped, truncated := applyExportCap(rows, s.exportMaxRowsFor())
	content, err := renderExport(capped, req.GetFormat())
	if err != nil {
		return nil, err
	}
	return &auditv1.ExportAuditEventsResponse{
		Response:  okResponse(),
		Content:   content,
		Truncated: truncated,
	}, nil
}

// ListMyActivity returns the caller's own audit events, filtered by
// action, result and time range, paginated newest-first (FR4.1).
// User-surface API; hard-scoped to the caller (AC7).
func (s *Service) ListMyActivity(ctx context.Context, req *auditv1.ListMyActivityRequest) (*auditv1.ListMyActivityResponse, error) {
	actorUserID, err := s.resolveActorUserID(ctx)
	if err != nil {
		return nil, err
	}
	since, until, err := validateRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListMyActivity(ctx, actorUserID, AuditEventFilter{
		Action: req.GetAction(),
		Result: resultString(req.GetResult()),
		Since:  since,
		Until:  until,
		Offset: offset,
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}
	events := make([]*auditv1.AuditEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, summarizeEvent(row))
	}
	return &auditv1.ListMyActivityResponse{
		Response:    okResponse(),
		AuditEvents: events,
		PageMeta:    &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// ExportMyActivity returns the caller's own filtered events as CSV or
// JSON, capped at the export row cap (FR4.2, AD7). User-surface API;
// hard-scoped to the caller (AC7). An invalid format returns 10602.
func (s *Service) ExportMyActivity(ctx context.Context, req *auditv1.ExportMyActivityRequest) (*auditv1.ExportMyActivityResponse, error) {
	actorUserID, err := s.resolveActorUserID(ctx)
	if err != nil {
		return nil, err
	}
	since, until, err := validateRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	// Fetch maxRows+1 so the cap can be detected (AD7).
	rows, _, err := repo.ListMyActivity(ctx, actorUserID, AuditEventFilter{
		Action: req.GetAction(),
		Result: resultString(req.GetResult()),
		Since:  since,
		Until:  until,
		Offset: 0,
		Limit:  s.exportMaxRowsFor() + 1,
	})
	if err != nil {
		return nil, err
	}
	capped, truncated := applyExportCap(rows, s.exportMaxRowsFor())
	content, err := renderExport(capped, req.GetFormat())
	if err != nil {
		return nil, err
	}
	return &auditv1.ExportMyActivityResponse{
		Response:  okResponse(),
		Content:   content,
		Truncated: truncated,
	}, nil
}

// renderExport serializes the events in the requested format. An
// invalid format returns 10602 (AC6).
func renderExport(events []*AuditEvent, format auditv1.AuditExportFormat) (string, error) {
	switch format {
	case auditv1.AuditExportFormat_AUDIT_EXPORT_FORMAT_CSV:
		return renderCSV(events)
	case auditv1.AuditExportFormat_AUDIT_EXPORT_FORMAT_JSON:
		return renderJSON(events)
	default:
		return "", apierrors.New(apierrors.CodeAuditExportInvalid)
	}
}

// summarizeEvent maps an audit event row to the proto message.
func summarizeEvent(ev *AuditEvent) *auditv1.AuditEvent {
	return &auditv1.AuditEvent{
		AuditEventId:   ev.ID,
		OrganizationId: ev.OrganizationID,
		ActorUserId:    ev.ActorUserID,
		ActorType:      actorTypeEnum(ev.ActorType),
		Action:         ev.Action,
		ResourceType:   ev.ResourceType,
		ResourceId:     ev.ResourceID,
		Result:         resultEnum(ev.Result),
		IpAddress:      ev.IPAddress,
		UserAgent:      ev.UserAgent,
		Metadata:       ev.Metadata,
		CreatedAt:      ev.CreatedAt.Unix(),
	}
}

// actorTypeString maps the proto enum to the stored string.
func actorTypeString(t auditv1.AuditActorType) string {
	switch t {
	case auditv1.AuditActorType_AUDIT_ACTOR_TYPE_SYSTEM:
		return "system"
	case auditv1.AuditActorType_AUDIT_ACTOR_TYPE_API_KEY:
		return "api_key"
	default:
		return "user"
	}
}

// actorTypeEnum maps a stored actor-type string to the proto enum.
func actorTypeEnum(s string) auditv1.AuditActorType {
	switch s {
	case "system":
		return auditv1.AuditActorType_AUDIT_ACTOR_TYPE_SYSTEM
	case "api_key":
		return auditv1.AuditActorType_AUDIT_ACTOR_TYPE_API_KEY
	default:
		return auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER
	}
}

// resultString maps the proto enum to the stored string.
func resultString(r auditv1.AuditResult) string {
	switch r {
	case auditv1.AuditResult_AUDIT_RESULT_FAILURE:
		return "failure"
	case auditv1.AuditResult_AUDIT_RESULT_SUCCESS:
		return "success"
	default:
		// Unspecified: no result filter.
		return ""
	}
}

// resultEnum maps a stored result string to the proto enum.
func resultEnum(s string) auditv1.AuditResult {
	if s == "failure" {
		return auditv1.AuditResult_AUDIT_RESULT_FAILURE
	}
	return auditv1.AuditResult_AUDIT_RESULT_SUCCESS
}

// okResponse is the success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}
