// Package tracing implements the read-only inference request tracing
// (feature #27). It owns the traces and trace_spans tables, the
// best-effort capture alongside the request log, and the read-only query
// RPCs (ListTraces, GetTrace) dual-bound to the admin and user surfaces.
package tracing

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	tracingv1 "github.com/go-taas/go-taas/proto/taas/tracing/v1"

	"github.com/go-taas/go-taas/pkg/authctx"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// ServiceName is the unique name of this service.
const ServiceName = "tracing"

// organizationMetadataKey is the gRPC metadata key carrying the
// transitional caller organization, set by the gateway from the
// X-Organization-Id HTTP header (the auth module's pattern).
const organizationMetadataKey = "x-organization-id"

// defaultRangeHours is the default range when since/until are unset.
const defaultRangeHours = 24

// Pagination bounds (Section 5.1).
const (
	listMaxLimit = 100
)

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

// RoleGuard enforces the minimum org role on the admin tracing RPCs. It
// is implemented by tenancy.RoleGuard.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// roleAdmin is the minimum org role for the admin tracing RPCs (AD9):
// the fleet view is operator-scoped.
const roleAdmin = "admin"

// Surface constants derived from the request path (feature #27, §3.3).
const (
	// SurfaceAdmin is the admin console surface.
	SurfaceAdmin = "admin"
	// SurfaceUser is the end-user console surface.
	SurfaceUser = "user"
)

// Service implements the tracing gRPC service.
type Service struct {
	tracingv1.UnimplementedTracingServiceServer

	components server.Components

	// repo is the injection point used by tests and FVT; production
	// resolves it lazily from the shared components.
	repo *Repository

	// maxRangeSeconds caps the accepted range (AD2). It is read from
	// config at construction; tests may override it.
	maxRangeSeconds int64

	// sessionOrgResolver resolves the session's active organization for
	// the user-realm binding (feature-17 AD6). Nil until wired: the
	// transitional X-Organization-Id header is used.
	sessionOrgResolver SessionOrgResolver

	// sessionUserResolver resolves the caller's user id for the admin
	// role check (feature #10). Nil until wired: no role check.
	sessionUserResolver SessionUserResolver

	// roleGuard gates the admin tracing RPCs by the caller's role in
	// the resolved org context (AD9). Nil until wired: no role check
	// (unit tests).
	roleGuard RoleGuard
}

// New constructs the tracing service. The repository is wired lazily on
// first use from the shared components.
func New(components server.Components) *Service {
	return &Service{
		components:      components,
		maxRangeSeconds: 92 * 24 * 3600,
	}
}

// SetMaxRangeSeconds overrides the accepted range cap (AD2). It is used
// by tests and FVT to exercise the 10404 boundary without a 92-day
// window.
func (s *Service) SetMaxRangeSeconds(v int64) { s.maxRangeSeconds = v }

// SetSessionOrgResolver injects the session-organization resolver used
// by the user-realm binding (feature-17 AD6).
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver used by the
// admin role check (feature #10).
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard that gates the admin tracing
// RPCs by the caller's role (AD9).
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// NewForFVT constructs a tracing service bound to a caller-provided GORM
// database. It exists so full-verification tests can wire the real
// service stack against a disposable database.
func NewForFVT(db *gorm.DB) *Service {
	return &Service{
		repo:            NewRepository(db),
		maxRangeSeconds: 92 * 24 * 3600,
	}
}

// MigrateSchemaForFVT applies the tracing schema onto a caller-provided
// database for full-verification tests.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&Trace{}, &TraceSpan{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	tracingv1.RegisterTracingServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return tracingv1.RegisterTracingServiceHandler
}

// Migrate implements server.Migrator: it creates the traces and
// trace_spans tables via GORM AutoMigrate (Section 4.2). There is
// nothing to seed.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&Trace{}, &TraceSpan{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "tracing: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "tracing: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the tracing repository.
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
	org, err := resolveOrganizationID(ctx)
	if err != nil && authctx.IsPlatformAdmin(ctx) {
		// A platform administrator belongs to no organization: the platform
		// console is platform-wide, so an empty organization is a valid
		// context. Reads then span every organization (or return nothing);
		// a write still needs a real, active organization.
		return "", nil
	}
	return org, err
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

// requireAdminRole enforces the minimum org role on the admin tracing
// RPCs (AD9). The RoleGuard resolves the caller from a session; in the
// transitional (session-less) path there is no session user to check, so
// the check is skipped and the org header itself is the access boundary
// (feature-17 AD6).
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
	return s.roleGuard.RequireRole(ctx, orgID, userID, roleAdmin)
}

// surfaceFromContext derives the tracing surface from the request path.
// The surface is a property of the binding, never a request field
// (feature #27, §3.3). The gateway forwards the request path in the
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

// validateRange checks and defaults the since/until pair: until
// defaults to now, since to until-24h; since > until or a range > the
// configured cap returns 10404 (AD2).
func (s *Service) validateRange(since, until int64) (int64, int64, error) {
	if until <= 0 {
		until = time.Now().Unix()
	}
	if since <= 0 {
		since = until - defaultRangeHours*3600
	}
	if since > until {
		return 0, 0, apierrors.New(apierrors.CodeMeteringRangeInvalid)
	}
	if until-since > s.maxRangeSeconds {
		return 0, 0, apierrors.New(apierrors.CodeMeteringRangeInvalid)
	}
	return since, until, nil
}

// ListTraces returns the trace explorer list for a time range and
// optional filters (AC1, AC2). The admin binding is fleet-wide by
// default (AD9); the user binding is hard-scoped to the caller's org
// (AD8).
func (s *Service) ListTraces(ctx context.Context, req *tracingv1.ListTracesRequest) (*tracingv1.ListTracesResponse, error) {
	surface := surfaceFromContext(ctx)

	var orgFilter string
	if surface == SurfaceAdmin {
		// Admin binding: cross-org by default, optional org filter (AD9).
		orgFilter = strings.TrimSpace(req.GetOrganizationId())
		if orgFilter == "" {
			orgFilter, _ = s.resolveOrg(ctx)
		}
		if err := s.requireAdminRole(ctx, orgFilter); err != nil {
			return nil, err
		}
	} else {
		// User binding: hard-scoped to the caller's org (AD8).
		org, err := s.resolveOrg(ctx)
		if err != nil {
			return nil, err
		}
		orgFilter = org
	}

	since, until, err := s.validateRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPageSize())
	rows, total, err := repo.ListTraces(ctx, TraceFilter{
		OrganizationID: orgFilter,
		RequestID:      strings.TrimSpace(req.GetRequestId()),
		ModelID:        strings.TrimSpace(req.GetModelId()),
		APIKeyID:       strings.TrimSpace(req.GetApiKeyId()),
		Status:         strings.TrimSpace(req.GetStatus()),
		Since:          since,
		Until:          until,
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}
	traces := make([]*tracingv1.TraceSummary, 0, len(rows))
	for _, row := range rows {
		traces = append(traces, s.summarizeTrace(ctx, repo, row, surface))
	}
	return &tracingv1.ListTracesResponse{
		Response:      okResponse(),
		Traces:        traces,
		NextPageToken: nextPageToken(offset, limit, total),
	}, nil
}

// GetTrace returns one trace's full detail: the summary fields plus its
// spans (AC3, AC4). The admin binding covers any trace (AD9); the user
// binding is hard-scoped to the caller's org (AD8).
func (s *Service) GetTrace(ctx context.Context, req *tracingv1.GetTraceRequest) (*tracingv1.GetTraceResponse, error) {
	traceID := strings.TrimSpace(req.GetTraceId())
	if traceID == "" {
		return nil, apierrors.New(apierrors.CodeTraceNotFound)
	}
	surface := surfaceFromContext(ctx)

	var orgFilter string
	if surface == SurfaceAdmin {
		// Admin binding: cross-org by default, optional org filter (AD9).
		orgFilter = strings.TrimSpace(req.GetOrganizationId())
		if orgFilter == "" {
			orgFilter, _ = s.resolveOrg(ctx)
		}
		if err := s.requireAdminRole(ctx, orgFilter); err != nil {
			return nil, err
		}
	} else {
		// User binding: hard-scoped to the caller's org (AD8).
		org, err := s.resolveOrg(ctx)
		if err != nil {
			return nil, err
		}
		orgFilter = org
	}

	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	trace, spans, err := repo.FindTraceByID(ctx, orgFilter, traceID)
	if err != nil {
		return nil, err
	}
	detail := s.buildDetail(ctx, repo, trace, spans, surface)
	return &tracingv1.GetTraceResponse{
		Response: okResponse(),
		Trace:    detail,
	}, nil
}

// summarizeTrace maps a trace row to the wire summary, resolving names
// in-process and masking service_id on the user surface (AD8).
func (s *Service) summarizeTrace(ctx context.Context, repo *Repository, row *Trace, surface string) *tracingv1.TraceSummary {
	modelName, _ := repo.ModelName(ctx, row.ModelID)
	keyName, _ := repo.APIKeyName(ctx, row.APIKeyID)
	serviceID := ""
	if row.ServiceID != nil {
		serviceID = *row.ServiceID
	}
	if surface == SurfaceUser {
		// Mask the operator service id to a phase label (AD8).
		serviceID = maskServiceID(serviceID)
	}
	return &tracingv1.TraceSummary{
		TraceId:        row.TraceID,
		OrganizationId: row.OrganizationID,
		ApiKeyId:       row.APIKeyID,
		ApiKeyName:     keyName,
		ModelId:        row.ModelID,
		ModelName:      modelName,
		ServiceId:      serviceID,
		Status:         row.Status,
		Error:          row.Error,
		TotalLatencyMs: row.TotalLatencyMs,
		TtftMs:         row.TTFTMs,
		GenerationMs:   row.GenerationMs,
		CreatedAt:      row.CreatedAt.Unix(),
	}
}

// buildDetail maps a trace row and its spans to the wire detail,
// resolving names in-process and masking service_id on the user surface
// (AD8).
func (s *Service) buildDetail(ctx context.Context, repo *Repository, trace *Trace, spans []*TraceSpan, surface string) *tracingv1.TraceDetail {
	modelName, _ := repo.ModelName(ctx, trace.ModelID)
	keyName, _ := repo.APIKeyName(ctx, trace.APIKeyID)
	serviceID := ""
	if trace.ServiceID != nil {
		serviceID = *trace.ServiceID
	}
	if surface == SurfaceUser {
		serviceID = maskServiceID(serviceID)
	}
	spanOut := make([]*tracingv1.TraceSpan, 0, len(spans))
	for _, span := range spans {
		spanOut = append(spanOut, &tracingv1.TraceSpan{
			SpanId:        span.ID,
			TraceId:       span.TraceID,
			ParentSpanId:  deref(span.ParentSpanID),
			Name:          span.Name,
			Kind:          span.Kind,
			StartOffsetMs: span.StartOffsetMs,
			DurationMs:    span.DurationMs,
			Status:        span.Status,
			Error:         span.Error,
			Attributes:    span.Attributes,
		})
	}
	return &tracingv1.TraceDetail{
		TraceId:          trace.TraceID,
		OrganizationId:   trace.OrganizationID,
		ApiKeyId:         trace.APIKeyID,
		ApiKeyName:       keyName,
		ModelId:          trace.ModelID,
		ModelName:        modelName,
		ServiceId:        serviceID,
		Status:           trace.Status,
		Error:            trace.Error,
		TotalLatencyMs:   trace.TotalLatencyMs,
		TtftMs:           trace.TTFTMs,
		GenerationMs:     trace.GenerationMs,
		PromptTokens:     trace.PromptTokens,
		CompletionTokens: trace.CompletionTokens,
		CachedTokens:     trace.CachedTokens,
		ReasoningTokens:  trace.ReasoningTokens,
		CreatedAt:        trace.CreatedAt.Unix(),
		Spans:            spanOut,
	}
}

// maskServiceID masks an operator service id to a phase label on the
// user surface (AD8). The gateway span is "gateway"; the inference span
// is "inference"; anything else is masked to "inference".
func maskServiceID(serviceID string) string {
	if serviceID == "" {
		return ""
	}
	if serviceID == "gateway" {
		return "gateway"
	}
	return "inference"
}

// deref returns the string value of a pointer, or "" when nil.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// normalizePagination clamps the page request: offset >= 0, limit
// defaults to 20 when unset or non-positive, capped at 100.
func normalizePagination(pageSize int32) (offset, limit int) {
	offset = 0
	limit = listDefaultLimit
	if pageSize > 0 {
		limit = int(pageSize)
	}
	if limit > listMaxLimit {
		limit = listMaxLimit
	}
	return offset, limit
}

// nextPageToken returns a dotted page token for the next page, or ""
// when there is no next page.
func nextPageToken(offset, limit int, total int64) string {
	next := int64(offset) + int64(limit)
	if next >= total {
		return ""
	}
	return itoa(next)
}

// itoa converts an int64 to a decimal string.
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// okResponse returns the success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}
