// Package billing implements the billing service: the effective-dated
// model × card-type price matrix, tiered pricing, usage-line ingestion,
// the charging engine, and charge/bill queries.
package billing

import (
	"context"
	"math"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"

	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/tenancy"
)

// ServiceName is the unique name of this service.
const ServiceName = "billing"

// Pagination and range bounds (architecture Sections 4.2/4.3).
const (
	listDefaultLimit  = 20
	listMaxLimit      = 100
	maxRangeSeconds   = 366 * 24 * 3600 // 366 days
	defaultRangeHours = 24
)

// Field length limits (architecture Section 4.3 validation matrix).
const (
	maxModelIDLen  = 128
	maxCardTypeLen = 64
	maxCurrencyLen = 8
)

// organizationMetadataKey is the gRPC metadata key carrying the
// transitional caller organization, set by the gateway from the
// X-Organization-Id HTTP header (the auth module's pattern).
const organizationMetadataKey = "x-organization-id"

// Service implements the billing gRPC service.
type Service struct {
	billingv1.UnimplementedBillingServiceServer

	components server.Components

	// repo and publisher are the injection points used by tests and
	// FVT; production resolves them lazily from the shared components.
	repo      *Repository
	publisher mq.Client
	// inferRepo is the read-only infer repository used for card
	// resolution at ingestion time (Section 3.4).
	inferRepo *infer.InferenceServiceRepository

	// orgGuard validates the transitional organization context against
	// the organizations table (feature #6). Nil until wired: unit tests
	// skip validation; main.go and FVT always wire it.
	orgGuard *tenancy.OrgGuard
}

// New constructs the billing service. The repository is wired lazily
// on first use from the shared components (the database component is
// initialized by server Init, which runs after service construction).
func New(components server.Components) *Service {
	return &Service{components: components}
}

// SetOrgGuard injects the tenancy read guard (the SetDeleteModelGuard
// pattern). Production and FVT wire it; unit tests leave it nil so
// checkOrg no-ops.
func (s *Service) SetOrgGuard(g *tenancy.OrgGuard) { s.orgGuard = g }

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

// NewForFVT constructs a billing service bound to a caller-provided
// GORM database and MQ client. It exists so full-verification tests can
// wire the real service stack against a disposable database and bus.
func NewForFVT(db *gorm.DB, publisher mq.Client) *Service {
	return &Service{repo: NewRepository(db), publisher: publisher}
}

// MigrateSchemaForFVT applies the billing schema (price_entries,
// usage_lines, charge_records, accounts, transactions) onto a
// caller-provided database for full-verification tests.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&PriceEntry{}, &UsageLine{}, &ChargeRecord{}, &Account{}, &Transaction{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	billingv1.RegisterBillingServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return billingv1.RegisterBillingServiceHandler
}

// Migrate implements server.Migrator: it creates/updates the
// price_entries, usage_lines, charge_records, accounts and
// transactions tables via GORM AutoMigrate. There is nothing to seed.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&PriceEntry{}, &UsageLine{}, &ChargeRecord{}, &Account{}, &Transaction{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "billing: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "billing: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the billing repository.
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
// unauthorized (AC16).
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

// validateBillingRange checks and defaults the since/until pair: until
// defaults to now, since to until-24h; a negative since, since > until,
// an until in the future, or a range > 366 days returns 10508 (FR5.3,
// AC12).
func validateBillingRange(since, until int64) (int64, int64, error) {
	if since < 0 {
		return 0, 0, apierrors.New(apierrors.CodeBillingRangeInvalid)
	}
	if until <= 0 {
		until = time.Now().Unix()
	}
	if until > time.Now().Unix()+60 {
		// until at most one minute in the future (clock skew slack).
		return 0, 0, apierrors.New(apierrors.CodeBillingRangeInvalid)
	}
	if since <= 0 {
		since = until - defaultRangeHours*3600
	}
	if since > until {
		return 0, 0, apierrors.New(apierrors.CodeBillingRangeInvalid)
	}
	if until-since > maxRangeSeconds {
		return 0, 0, apierrors.New(apierrors.CodeBillingRangeInvalid)
	}
	return since, until, nil
}

// SetPrice creates or updates one matrix cell version (FR1). The
// validation matrix runs first; the first failure returns 10507 and
// nothing is written (AC2).
func (s *Service) SetPrice(_ context.Context, req *billingv1.SetPriceRequest) (*billingv1.SetPriceResponse, error) {
	if err := validatePrice(req); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}

	currency := strings.TrimSpace(req.GetCurrency())
	if currency == "" {
		currency = config.GetConfig().Billing.Currency
	}
	effectiveFrom := req.GetEffectiveFrom()
	if effectiveFrom <= 0 {
		effectiveFrom = time.Now().UTC().Unix()
	}

	tiers := make([]PriceTier, 0, len(req.GetTiers()))
	for _, t := range req.GetTiers() {
		tiers = append(tiers, PriceTier{
			UpToTokens:            t.GetUpToTokens(),
			InputPricePerMillion:  t.GetInputPricePerMillion(),
			OutputPricePerMillion: t.GetOutputPricePerMillion(),
		})
	}
	tiersJSON, err := encodeTiers(tiers)
	if err != nil {
		return nil, apierrors.New(apierrors.CodePriceInvalid)
	}

	entry := &PriceEntry{
		ModelID:               strings.TrimSpace(req.GetModelId()),
		AcceleratorType:       strings.TrimSpace(req.GetAcceleratorType()),
		InputPricePerMillion:  req.GetInputPricePerMillion(),
		OutputPricePerMillion: req.GetOutputPricePerMillion(),
		CachedPricePerMillion: req.GetCachedPricePerMillion(),
		Currency:              currency,
		EffectiveFrom:         effectiveFrom,
		TiersJSON:             tiersJSON,
	}
	stored, err := repo.UpsertPrice(context.Background(), entry)
	if err != nil {
		return nil, err
	}
	return &billingv1.SetPriceResponse{
		Response: okResponse(),
		PriceId:  stored.ID,
	}, nil
}

// validatePrice applies the SetPrice validation matrix (architecture
// Section 4.3): the first failure returns 10507.
func validatePrice(req *billingv1.SetPriceRequest) error {
	invalid := apierrors.New(apierrors.CodePriceInvalid)
	modelID := strings.TrimSpace(req.GetModelId())
	if modelID == "" || len(modelID) > maxModelIDLen {
		return invalid
	}
	card := strings.TrimSpace(req.GetAcceleratorType())
	if card == "" || len(card) > maxCardTypeLen {
		return invalid
	}
	if req.GetInputPricePerMillion() < 0 || req.GetOutputPricePerMillion() < 0 ||
		req.GetCachedPricePerMillion() < 0 {
		return invalid
	}
	currency := strings.TrimSpace(req.GetCurrency())
	if currency != "" && len(currency) > maxCurrencyLen {
		return invalid
	}
	if currency != "" && currency != config.GetConfig().Billing.Currency {
		return invalid
	}
	if req.GetEffectiveFrom() < 0 {
		return invalid
	}
	// Tiers: up_to_tokens > 0 for all but the last; the last has
	// up_to_tokens = 0; strictly ascending bounds; rates >= 0. The
	// ascending check skips the unbounded last tier (its 0 would
	// always compare less).
	tiers := req.GetTiers()
	for i, t := range tiers {
		last := i == len(tiers)-1
		if last && t.GetUpToTokens() != 0 {
			return invalid
		}
		if !last && t.GetUpToTokens() <= 0 {
			return invalid
		}
		if !last && i > 0 && t.GetUpToTokens() <= tiers[i-1].GetUpToTokens() {
			return invalid
		}
		if t.GetInputPricePerMillion() < 0 || t.GetOutputPricePerMillion() < 0 {
			return invalid
		}
	}
	return nil
}

// ListPrices returns the price matrix — the current view by default,
// every version with include_history (FR2).
func (s *Service) ListPrices(ctx context.Context, req *billingv1.ListPricesRequest) (*billingv1.ListPricesResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListPrices(ctx, PriceFilter{
		ModelID:         strings.TrimSpace(req.GetModelId()),
		AcceleratorType: strings.TrimSpace(req.GetAcceleratorType()),
		IncludeHistory:  req.GetIncludeHistory(),
		Now:             time.Now().UTC().Unix(),
		Offset:          offset,
		Limit:           limit,
	})
	if err != nil {
		return nil, err
	}
	prices := make([]*billingv1.PriceEntry, 0, len(rows))
	for _, row := range rows {
		prices = append(prices, summarizePrice(row))
	}
	return &billingv1.ListPricesResponse{
		Response: okResponse(),
		Prices:   prices,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// summarizePrice maps a price row to the proto entry (tiers decoded
// from JSON).
func summarizePrice(p *PriceEntry) *billingv1.PriceEntry {
	tiers, _ := decodeTiers(p.TiersJSON)
	protoTiers := make([]*billingv1.PriceTier, 0, len(tiers))
	for _, t := range tiers {
		protoTiers = append(protoTiers, &billingv1.PriceTier{
			UpToTokens:            t.UpToTokens,
			InputPricePerMillion:  t.InputPricePerMillion,
			OutputPricePerMillion: t.OutputPricePerMillion,
		})
	}
	return &billingv1.PriceEntry{
		PriceId:               p.ID,
		ModelId:               p.ModelID,
		AcceleratorType:       p.AcceleratorType,
		InputPricePerMillion:  p.InputPricePerMillion,
		OutputPricePerMillion: p.OutputPricePerMillion,
		CachedPricePerMillion: p.CachedPricePerMillion,
		Currency:              p.Currency,
		EffectiveFrom:         p.EffectiveFrom,
		Tiers:                 protoTiers,
		UpdatedAt:             p.UpdatedAt.Unix(),
	}
}

// ListCharges returns charge records filtered by api key, model and
// time range, paginated newest-first (FR5.1, AC12).
func (s *Service) ListCharges(ctx context.Context, req *billingv1.ListChargesRequest) (*billingv1.ListChargesResponse, error) {
	orgID, err := resolveOrganizationID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	since, until, err := validateBillingRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListCharges(ctx, ChargeFilter{
		OrganizationID: orgID,
		APIKeyID:       strings.TrimSpace(req.GetApiKeyId()),
		ModelID:        strings.TrimSpace(req.GetModelId()),
		Since:          since,
		Until:          until,
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}
	charges := make([]*billingv1.ChargeRecordSummary, 0, len(rows))
	for _, row := range rows {
		charges = append(charges, summarizeCharge(row))
	}
	return &billingv1.ListChargesResponse{
		Response: okResponse(),
		Charges:  charges,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// summarizeCharge maps a charge row to the proto summary.
func summarizeCharge(c *ChargeRecord) *billingv1.ChargeRecordSummary {
	priceID := ""
	if c.PriceID != nil {
		priceID = *c.PriceID
	}
	return &billingv1.ChargeRecordSummary{
		ChargeId:         c.ID,
		OrganizationId:   c.OrganizationID,
		ApiKeyId:         c.APIKeyID,
		ModelId:          c.ModelID,
		AcceleratorType:  c.AcceleratorType,
		PeriodStart:      c.PeriodStart,
		PeriodEnd:        c.PeriodEnd,
		PromptTokens:     c.PromptTokens,
		CompletionTokens: c.CompletionTokens,
		CachedTokens:     c.CachedTokens,
		ReasoningTokens:  c.ReasoningTokens,
		RequestCount:     c.RequestCount,
		Amount:           c.Amount,
		Currency:         c.Currency,
		PriceId:          priceID,
		TierIndex:        clampTierIndex(c.TierIndex),
		Priced:           c.Priced,
		ChargedAt:        c.ChargedAt.Unix(),
	}
}

// ListBills returns monthly bill summaries computed on read (D9, FR5.2,
// AC12/AC16).
func (s *Service) ListBills(ctx context.Context, req *billingv1.ListBillsRequest) (*billingv1.ListBillsResponse, error) {
	orgID, err := resolveOrganizationID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	since, until, err := validateBillingRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.BillSummaries(ctx, orgID, since, until, offset, limit)
	if err != nil {
		return nil, err
	}
	bills := make([]*billingv1.BillSummary, 0, len(rows))
	for _, row := range rows {
		monthStart := time.Unix(row.MonthStart, 0).UTC()
		bills = append(bills, &billingv1.BillSummary{
			BillId:         billIDOf(orgID, monthStart),
			OrganizationId: orgID,
			Amount:         row.Amount,
			Currency:       row.Currency,
			PeriodStart:    row.MonthStart,
			PeriodEnd:      monthStart.AddDate(0, 1, 0).Unix(),
			ChargeCount:    row.ChargeCount,
			UnpricedCount:  row.UnpricedCount,
		})
	}
	return &billingv1.ListBillsResponse{
		Response: okResponse(),
		Bills:    bills,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// clampTierIndex clamps the tier index into the int32 range for the
// proto field.
func clampTierIndex(v int) int32 {
	if v < math.MinInt32 {
		return math.MinInt32
	}
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v)
}

// billIDOf composes the deterministic bill id "{org}-{YYYYMM}".
func billIDOf(orgID string, monthStart time.Time) string {
	return orgID + "-" + monthStart.Format("200601")
}

// okResponse is the success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}
