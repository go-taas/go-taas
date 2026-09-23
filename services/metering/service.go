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

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
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

// Service implements the metering gRPC service.
type Service struct {
	meteringv1.UnimplementedMeteringServiceServer

	components server.Components

	// repo and publisher are the injection points used by tests and
	// FVT; production resolves them lazily from the shared components.
	repo      *Repository
	publisher mq.Client
}

// New constructs the metering service. The repository is wired lazily
// on first use from the shared components (the database component is
// initialized by server Init, which runs after service construction).
func New(components server.Components) *Service {
	return &Service{components: components}
}

// NewForFVT constructs a metering service bound to a caller-provided
// GORM database and MQ client. It exists so full-verification tests can
// wire the real service stack against a disposable database and bus.
func NewForFVT(db *gorm.DB, publisher mq.Client) *Service {
	return &Service{repo: NewRepository(db), publisher: publisher}
}

// MigrateSchemaForFVT applies the metering schema (vouchers,
// usage_records) onto a caller-provided database for full-verification
// tests.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&Voucher{}, &UsageRecord{})
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

// Migrate implements server.Migrator: it creates/updates the vouchers
// and usage_records tables via GORM AutoMigrate. There is nothing to
// seed.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&Voucher{}, &UsageRecord{})
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
	return stored.ID, nil
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
	orgID, err := resolveOrganizationID(ctx)
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
	return &meteringv1.ListVouchersResponse{
		Response: okResponse(),
		Vouchers: vouchers,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// GetVoucher returns one voucher; unknown ids return 10403 (FR4.3).
func (s *Service) GetVoucher(ctx context.Context, req *meteringv1.GetVoucherRequest) (*meteringv1.GetVoucherResponse, error) {
	if _, err := resolveOrganizationID(ctx); err != nil {
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
	return &meteringv1.GetVoucherResponse{
		Response: okResponse(),
		Voucher:  summarizeVoucher(row),
	}, nil
}

// GetUsageSummary returns per-key (default) or per-model aggregates
// with the settled/pending hour split (FR4.1).
func (s *Service) GetUsageSummary(ctx context.Context, req *meteringv1.GetUsageSummaryRequest) (*meteringv1.GetUsageSummaryResponse, error) {
	orgID, err := resolveOrganizationID(ctx)
	if err != nil {
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
	orgID, err := resolveOrganizationID(ctx)
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
