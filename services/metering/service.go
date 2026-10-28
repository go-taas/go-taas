// Package metering implements the metering service: ingesting token
// usage events from the data plane, settling them into hourly usage
// records and serving voucher/usage queries.
package metering

import (
	"context"
	"math"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	meteringv1 "github.com/go-taas/go-taas/proto/taas/metering/v1"

	"github.com/go-taas/go-taas/pkg/authctx"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/tenancy"
)

// ServiceName is the unique name of this service.
const ServiceName = "metering"

// Pagination and range bounds (architecture Sections 4.2/4.3).
const (
	listDefaultLimit  = 20
	listMaxLimit      = 100
	maxRangeSeconds   = 92 * 24 * 3600 // 92 days
	defaultRangeHours = 24
)

// Field length limits (architecture Section 4.3 validation matrix).
const (
	maxRequestIDLen = 128
	maxOrgIDLen     = 64
	maxAPIKeyIDLen  = 64
	maxModelIDLen   = 128
	maxServiceIDLen = 64
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

// RoleGuard enforces the minimum org role on the admin metering RPCs
// (feature #28 AD9, feature #31 AD9). It is implemented by
// tenancy.RoleGuard.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// roleAdmin is the minimum org role for the admin metering RPCs (AD9):
// the fleet view is operator-scoped.
const roleAdmin = "admin"

// Surface constants derived from the request path (feature #28, §3.3).
const (
	// SurfaceAdmin is the admin console surface.
	SurfaceAdmin = "admin"
	// SurfaceUser is the end-user console surface.
	SurfaceUser = "user"
)

// TraceCapturer captures an inference request's trace best-effort
// (feature #27, AD3). It is implemented by the tracing module and
// injected at wiring time. Nil until wired: no trace is captured.
type TraceCapturer interface {
	// CaptureTrace writes the trace and its spans best-effort; a failure
	// is logged and never fails or retries the voucher or the request
	// log.
	CaptureTrace(ctx context.Context, ev *TraceCaptureEvent)
}

// TraceCaptureEvent is the metering event projection the trace capture
// reads (feature #27, AD3). It carries the request identity, token
// counts, latency, status, error, and the per-phase timings.
type TraceCaptureEvent struct {
	RequestID        string
	OrganizationID   string
	APIKeyID         string
	ModelID          string
	ServiceID        string
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	ReasoningTokens  int64
	LatencyMs        int64
	TTFTMs           int64
	GenerationMs     int64
	Status           string
	Error            string
	CompletedAt      int64
}

// Service implements the metering gRPC service.
type Service struct {
	meteringv1.UnimplementedMeteringServiceServer

	components server.Components

	// repo and publisher are the injection points used by tests and
	// FVT; production resolves them lazily from the shared components.
	repo      *Repository
	publisher mq.Client

	// serviceLookup maps a catalog model id to the inference service
	// running it, so a gateway-originated voucher can be attributed to
	// the service that served the request. Nil until wired: the voucher
	// then only carries the model, which is still priced correctly.
	serviceLookup func(ctx context.Context, modelID string) string

	// costAttributor computes per-request estimated cost on read
	// (feature #9, AD3). Nil until wired: unit tests skip attribution;
	// main.go and FVT always wire it.
	costAttributor *CostAttributor

	// orgGuard validates the transitional organization context against
	// the organizations table (feature #6). Nil until wired: unit tests
	// skip validation; main.go and FVT always wire it.
	orgGuard *tenancy.OrgGuard

	// sessionOrgResolver resolves the session's active organization for
	// the user-realm reads (feature-17 AD6). Nil until wired: the
	// transitional X-Organization-Id header is used.
	sessionOrgResolver SessionOrgResolver

	// traceCapturer captures an inference request's trace best-effort
	// (feature #27, AD3). Nil until wired: no trace is captured.
	traceCapturer TraceCapturer

	// sessionUserResolver resolves the caller's user id for the admin
	// role check (feature #10). Nil until wired: no role check.
	sessionUserResolver SessionUserResolver

	// roleGuard gates the admin metering RPCs by the caller's role in
	// the resolved org context (AD9). Nil until wired: no role check
	// (unit tests).
	roleGuard RoleGuard
}

// New constructs the metering service. The repository is wired lazily
// on first use from the shared components (the database component is
// initialized by server Init, which runs after service construction).
func New(components server.Components) *Service {
	return &Service{components: components}
}

// SetOrgGuard injects the tenancy read guard (the SetDeleteModelGuard
// pattern). Production and FVT wire it; unit tests leave it nil so
// checkOrg no-ops.
func (s *Service) SetOrgGuard(g *tenancy.OrgGuard) { s.orgGuard = g }

// SetSessionOrgResolver injects the session-organization resolver used
// by the user-realm reads (feature-17 AD6). Production and FVT wire the
// auth service; unit tests may inject a fake.
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetTraceCapturer injects the best-effort trace capturer (feature #27,
// AD3). Production and FVT wire the tracing service; unit tests leave it
// nil so no trace is captured.
func (s *Service) SetTraceCapturer(c TraceCapturer) { s.traceCapturer = c }

// SetSessionUserResolver injects the session-user resolver used by the
// admin role check (feature #10).
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard that gates the admin metering
// RPCs by the caller's role (AD9).
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// resolveOrg returns the organization context for a user-realm read
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

// surfaceFromContext derives the metering surface from the request path.
// The surface is a property of the binding, never a request field
// (feature #28, §3.3). The gateway forwards the request path in the
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

// requireAdminRole enforces the minimum org role on the admin metering
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

// SetCostAttributor injects the per-request cost attributor (feature
// #9). Production and FVT wire it; unit tests leave it nil so voucher
// attribution no-ops.
func (s *Service) SetCostAttributor(c *CostAttributor) { s.costAttributor = c }

// checkOrg validates the org context: existence on reads, active
// state on gated writes. No-op when the guard is not wired.
func (s *Service) checkOrg(ctx context.Context, orgID string, requireActive bool) error {
	if s.orgGuard == nil {
		return nil
	}
	if requireActive {
		return s.orgGuard.RequireActive(ctx, orgID)
	}
	return s.orgGuard.RequireExists(ctx, orgID)
}

// NewForFVT constructs a metering service bound to a caller-provided
// GORM database and MQ client. It exists so full-verification tests can
// wire the real service stack against a disposable database and bus.
func NewForFVT(db *gorm.DB, publisher mq.Client) *Service {
	return &Service{repo: NewRepository(db), publisher: publisher}
}

// MigrateSchemaForFVT applies the metering schema (vouchers,
// usage_records, request_logs) onto a caller-provided database for
// full-verification tests.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&Voucher{}, &UsageRecord{}, &RequestLog{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	meteringv1.RegisterMeteringServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return meteringv1.RegisterMeteringServiceHandler
}

// Migrate implements server.Migrator: it creates/updates the vouchers,
// usage_records and request_logs tables via GORM AutoMigrate. There is
// nothing to seed.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&Voucher{}, &UsageRecord{}, &RequestLog{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "metering: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "metering: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the metering repository.
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

// resolveOrganizationID reads the transitional caller organization from
// the x-organization-id gRPC metadata (set by the gateway from the
// X-Organization-Id HTTP header). Missing or empty values are
// unauthorized (the auth module's pattern, AC14).
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
// defaults to now, since to until-24h; since > until or a range > 92
// days returns 10404 (FR4.6, AC10).
func validateRange(since, until int64) (int64, int64, error) {
	if until <= 0 {
		until = time.Now().Unix()
	}
	if since <= 0 {
		since = until - defaultRangeHours*3600
	}
	if since > until {
		return 0, 0, apierrors.New(apierrors.CodeMeteringRangeInvalid)
	}
	if until-since > maxRangeSeconds {
		return 0, 0, apierrors.New(apierrors.CodeMeteringRangeInvalid)
	}
	return since, until, nil
}

// handleEvent is the shared ingestion core used by both the RPC and
// the MQ consumer (D2, AC3): validation matrix → voucher build →
// idempotent ingest.
func (s *Service) handleEvent(ctx context.Context, ev *meteringEvent) (string, error) {
	if err := validateEvent(ev); err != nil {
		return "", err
	}
	repo, err := s.repository()
	if err != nil {
		return "", err
	}
	voucher := &Voucher{
		RequestID:        ev.RequestID,
		OrganizationID:   ev.OrganizationID,
		APIKeyID:         ev.APIKeyID,
		ModelID:          ev.ModelID,
		PromptTokens:     ev.Usage.PromptTokens,
		CompletionTokens: ev.Usage.CompletionTokens,
		CachedTokens:     ev.Usage.CachedTokens,
		ReasoningTokens:  ev.Usage.ReasoningTokens,
		CompletedAt:      time.Unix(ev.CompletedAt, 0).UTC(),
	}
	if ev.ServiceID != "" {
		sid := ev.ServiceID
		voucher.ServiceID = &sid
	}
	stored, err := repo.IngestVoucher(ctx, voucher)
	if err != nil {
		return "", err
	}
	// Feature #12: write the request log best-effort and non-fatal
	// (AD2). A failure is logged and never fails or retries the voucher.
	s.writeRequestLog(ctx, repo, ev)
	// Feature #27: capture the trace best-effort and non-fatal (AD3). A
	// trace-write failure is logged and never fails or retries the
	// voucher or the request log.
	s.captureTrace(ctx, ev)
	return stored.ID, nil
}

// captureTrace forwards the metering event to the tracing module's
// best-effort trace capture (feature #27, AD3). No-op when the capturer
// is not wired.
func (s *Service) captureTrace(ctx context.Context, ev *meteringEvent) {
	if s.traceCapturer == nil {
		return
	}
	s.traceCapturer.CaptureTrace(ctx, &TraceCaptureEvent{
		RequestID:        ev.RequestID,
		OrganizationID:   ev.OrganizationID,
		APIKeyID:         ev.APIKeyID,
		ModelID:          ev.ModelID,
		ServiceID:        ev.ServiceID,
		PromptTokens:     ev.Usage.PromptTokens,
		CompletionTokens: ev.Usage.CompletionTokens,
		CachedTokens:     ev.Usage.CachedTokens,
		ReasoningTokens:  ev.Usage.ReasoningTokens,
		LatencyMs:        ev.LatencyMs,
		TTFTMs:           ev.TTFTMs,
		GenerationMs:     ev.GenerationMs,
		Status:           ev.Status,
		Error:            ev.Error,
		CompletedAt:      ev.CompletedAt,
	})
}

// writeRequestLog builds and writes a request-log row best-effort
// (feature #12, AD2). A write failure is logged and skipped; the
// voucher is the authoritative accounting record.
func (s *Service) writeRequestLog(ctx context.Context, repo *Repository, ev *meteringEvent) {
	status := "success"
	if ev.Status != "" {
		status = ev.Status
	}
	log := &RequestLog{
		RequestID:        ev.RequestID,
		OrganizationID:   ev.OrganizationID,
		APIKeyID:         ev.APIKeyID,
		ModelID:          ev.ModelID,
		PromptTokens:     ev.Usage.PromptTokens,
		CompletionTokens: ev.Usage.CompletionTokens,
		CachedTokens:     ev.Usage.CachedTokens,
		ReasoningTokens:  ev.Usage.ReasoningTokens,
		LatencyMs:        ev.LatencyMs,
		Status:           status,
		Error:            ev.Error,
		CreatedAt:        time.Unix(ev.CompletedAt, 0).UTC(),
	}
	if ev.ServiceID != "" {
		sid := ev.ServiceID
		log.ServiceID = &sid
	}
	if err := repo.IngestRequestLog(ctx, log); err != nil {
		logger.S().Warnw("metering: request log write failed (best-effort, voucher already written)",
			"request_id", ev.RequestID, "err", err)
	}
}

// validateEvent applies the ingestion validation matrix (architecture
// Section 4.3): the first failure returns 10401 and nothing is written.
func validateEvent(ev *meteringEvent) error {
	invalid := apierrors.New(apierrors.CodeMeteringEventInvalid)
	if strings.TrimSpace(ev.RequestID) == "" || len(ev.RequestID) > maxRequestIDLen {
		return invalid
	}
	if strings.TrimSpace(ev.OrganizationID) == "" || len(ev.OrganizationID) > maxOrgIDLen {
		return invalid
	}
	if strings.TrimSpace(ev.APIKeyID) == "" || len(ev.APIKeyID) > maxAPIKeyIDLen {
		return invalid
	}
	if strings.TrimSpace(ev.ModelID) == "" || len(ev.ModelID) > maxModelIDLen {
		return invalid
	}
	if len(ev.ServiceID) > maxServiceIDLen {
		return invalid
	}
	if ev.Usage.PromptTokens < 0 || ev.Usage.CompletionTokens < 0 ||
		ev.Usage.CachedTokens < 0 || ev.Usage.ReasoningTokens < 0 {
		return invalid
	}
	if ev.CompletedAt <= 0 {
		return invalid
	}
	return nil
}

// IngestMeteringEvent records one token-usage event (the direct path
// for tests and future non-Envoy gateways; it shares the handler with
// the MQ consumer, FR1.4).
func (s *Service) IngestMeteringEvent(ctx context.Context, req *meteringv1.IngestMeteringEventRequest) (*meteringv1.IngestMeteringEventResponse, error) {
	ev := &meteringEvent{
		RequestID:      req.GetRequestId(),
		OrganizationID: req.GetOrganizationId(),
		APIKeyID:       req.GetApiKeyId(),
		ModelID:        req.GetModelId(),
		ServiceID:      req.GetServiceId(),
		CompletedAt:    req.GetCompletedAt(),
		LatencyMs:      req.GetLatencyMs(),
		Status:         requestLogStatusString(req.GetStatus()),
		Error:          req.GetError(),
		TTFTMs:         req.GetTtftMs(),
		GenerationMs:   req.GetGenerationMs(),
	}
	if usage := req.GetUsage(); usage != nil {
		ev.Usage = tokenUsage{
			PromptTokens:     usage.GetPromptTokens(),
			CompletionTokens: usage.GetCompletionTokens(),
			CachedTokens:     usage.GetCachedTokens(),
			ReasoningTokens:  usage.GetReasoningTokens(),
		}
	}
	voucherID, err := s.handleEvent(ctx, ev)
	if err != nil {
		return nil, err
	}
	return &meteringv1.IngestMeteringEventResponse{
		Response:  okResponse(),
		VoucherId: voucherID,
	}, nil
}

// ListVouchers returns vouchers filtered by api key, model and time
// range, paginated newest-first (FR4.2).
func (s *Service) ListVouchers(ctx context.Context, req *meteringv1.ListVouchersRequest) (*meteringv1.ListVouchersResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
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
	rows, total, err := repo.ListVouchers(ctx, VoucherFilter{
		OrganizationID: orgID,
		APIKeyID:       req.GetApiKeyId(),
		ModelID:        req.GetModelId(),
		Since:          since,
		Until:          until,
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}
	vouchers := make([]*meteringv1.VoucherSummary, 0, len(rows))
	for _, row := range rows {
		vouchers = append(vouchers, summarizeVoucher(row))
	}
	// Feature #9: attach per-request estimated cost on read (AD3).
	if s.costAttributor != nil {
		costs, priced, err := s.costAttributor.attributePage(ctx, rows)
		if err != nil {
			return nil, err
		}
		for _, v := range vouchers {
			v.EstimatedCostCents = costs[v.GetVoucherId()]
			v.Priced = priced[v.GetVoucherId()]
		}
	}
	return &meteringv1.ListVouchersResponse{
		Response: okResponse(),
		Vouchers: vouchers,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// GetVoucher returns one voucher; unknown ids return 10403 (FR4.3).
func (s *Service) GetVoucher(ctx context.Context, req *meteringv1.GetVoucherRequest) (*meteringv1.GetVoucherResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.FindByID(ctx, req.GetVoucherId())
	if err != nil {
		return nil, err
	}
	out := summarizeVoucher(row)
	// Feature #9: attach per-request estimated cost on read (AD3).
	if s.costAttributor != nil {
		cost, priced, err := s.costAttributor.estimateCost(ctx, row)
		if err != nil {
			return nil, err
		}
		out.EstimatedCostCents = cost
		out.Priced = priced
	}
	return &meteringv1.GetVoucherResponse{
		Response: okResponse(),
		Voucher:  out,
	}, nil
}

// GetUsageSummary returns per-key (default) or per-model aggregates
// with the settled/pending hour split (FR4.1).
func (s *Service) GetUsageSummary(ctx context.Context, req *meteringv1.GetUsageSummaryRequest) (*meteringv1.GetUsageSummaryResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	since, until, err := validateRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	groupBy := req.GetGroupBy()
	if groupBy == "" {
		groupBy = "api_key"
	}
	if groupBy != "api_key" && groupBy != "model" {
		return nil, apierrors.New(apierrors.CodeMeteringRangeInvalid)
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	rows, err := repo.UsageSummary(ctx, orgID, since, until, groupBy == "model")
	if err != nil {
		return nil, err
	}
	out := make([]*meteringv1.UsageSummaryRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, &meteringv1.UsageSummaryRow{
			GroupKey:         row.GroupKey,
			PromptTokens:     row.PromptTokens,
			CompletionTokens: row.CompletionTokens,
			CachedTokens:     row.CachedTokens,
			ReasoningTokens:  row.ReasoningTokens,
			RequestCount:     row.RequestCount,
			SettledHours:     row.SettledHours,
			PendingHours:     row.PendingHours,
		})
	}
	return &meteringv1.GetUsageSummaryResponse{Response: okResponse(), Rows: out}, nil
}

// ListUsageRecords returns settled usage records filtered by api key
// and range, paginated newest-period-first (FR4.4).
func (s *Service) ListUsageRecords(ctx context.Context, req *meteringv1.ListUsageRecordsRequest) (*meteringv1.ListUsageRecordsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
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
	rows, total, err := repo.ListUsageRecords(ctx, UsageRecordFilter{
		OrganizationID: orgID,
		APIKeyID:       req.GetApiKeyId(),
		Since:          since,
		Until:          until,
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}
	records := make([]*meteringv1.UsageRecordSummary, 0, len(rows))
	for _, row := range rows {
		records = append(records, &meteringv1.UsageRecordSummary{
			UsageRecordId:    row.ID,
			OrganizationId:   row.OrganizationID,
			ApiKeyId:         row.APIKeyID,
			PeriodStart:      row.PeriodStart,
			PeriodEnd:        row.PeriodEnd,
			PromptTokens:     row.PromptTokens,
			CompletionTokens: row.CompletionTokens,
			CachedTokens:     row.CachedTokens,
			ReasoningTokens:  row.ReasoningTokens,
			RequestCount:     row.RequestCount,
			SettledAt:        row.SettledAt.Unix(),
		})
	}
	return &meteringv1.ListUsageRecordsResponse{
		Response: okResponse(),
		Records:  records,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// summarizeVoucher maps a voucher row to the proto summary.
func summarizeVoucher(v *Voucher) *meteringv1.VoucherSummary {
	serviceID := ""
	if v.ServiceID != nil {
		serviceID = *v.ServiceID
	}
	return &meteringv1.VoucherSummary{
		VoucherId:      v.ID,
		RequestId:      v.RequestID,
		OrganizationId: v.OrganizationID,
		ModelId:        v.ModelID,
		ApiKeyId:       v.APIKeyID,
		ServiceId:      serviceID,
		Settled:        v.SettledUsageRecordID != nil,
		CompletedAt:    v.CompletedAt.Unix(),
		Usage: &meteringv1.TokenUsage{
			PromptTokens:     v.PromptTokens,
			CompletionTokens: v.CompletionTokens,
			CachedTokens:     v.CachedTokens,
			ReasoningTokens:  v.ReasoningTokens,
		},
	}
}

// okResponse is the success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}

// requestLogStatusString maps the proto enum to the stored string.
func requestLogStatusString(s meteringv1.RequestLogStatus) string {
	switch s {
	case meteringv1.RequestLogStatus_REQUEST_LOG_STATUS_ERROR:
		return "error"
	case meteringv1.RequestLogStatus_REQUEST_LOG_STATUS_STREAMING:
		return "streaming"
	default:
		return "success"
	}
}

// requestLogStatusEnum maps a stored status string to the proto enum.
func requestLogStatusEnum(s string) meteringv1.RequestLogStatus {
	switch s {
	case "error":
		return meteringv1.RequestLogStatus_REQUEST_LOG_STATUS_ERROR
	case "streaming":
		return meteringv1.RequestLogStatus_REQUEST_LOG_STATUS_STREAMING
	default:
		return meteringv1.RequestLogStatus_REQUEST_LOG_STATUS_SUCCESS
	}
}
