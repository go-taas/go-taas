// Package observability implements the read-only model observability
// aggregation over request_logs (feature #24). It serves the admin fleet
// overview (GetObservabilityOverview) and the dual-bound single-model
// drill-down (GetModelObservability), deriving latency percentiles,
// throughput, error rate and token throughput from the request logs the
// metering module already writes.
package observability

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	observabilityv1 "github.com/go-taas/go-taas/proto/taas/observability/v1"

	"github.com/go-taas/go-taas/pkg/authctx"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// ServiceName is the unique name of this service.
const ServiceName = "observability"

// organizationMetadataKey is the gRPC metadata key carrying the
// transitional caller organization, set by the gateway from the
// X-Organization-Id HTTP header (the auth module's pattern).
const organizationMetadataKey = "x-organization-id"

// defaultRangeHours is the default range when since/until are unset.
const defaultRangeHours = 24

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

// RoleGuard enforces the minimum org role on the admin observability
// RPCs. It is implemented by tenancy.RoleGuard.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// roleAdmin is the minimum org role for the admin observability RPCs
// (AD4): the fleet view is operator-scoped.
const roleAdmin = "admin"

// Service implements the observability gRPC service.
type Service struct {
	observabilityv1.UnimplementedObservabilityServiceServer

	components server.Components

	// repo is the injection point used by tests and FVT; production
	// resolves it lazily from the shared components.
	repo *Repository

	// maxRangeSeconds caps the accepted range (AD7). It is read from
	// config at construction; tests may override it.
	maxRangeSeconds int64

	// sessionOrgResolver resolves the session's active organization for
	// the user-realm binding (feature-17 AD6). Nil until wired: the
	// transitional X-Organization-Id header is used.
	sessionOrgResolver SessionOrgResolver

	// sessionUserResolver resolves the caller's user id for the admin
	// role check (feature #10). Nil until wired: no role check.
	sessionUserResolver SessionUserResolver

	// roleGuard gates the admin observability RPCs by the caller's role
	// in the resolved org context (AD4). Nil until wired: no role check
	// (unit tests).
	roleGuard RoleGuard
}

// New constructs the observability service. The repository is wired
// lazily on first use from the shared components.
func New(components server.Components) *Service {
	return &Service{
		components:      components,
		maxRangeSeconds: 92 * 24 * 3600,
	}
}

// SetMaxRangeSeconds overrides the accepted range cap (AD7). It is used
// by tests and FVT to exercise the 10404 boundary without a 92-day
// window.
func (s *Service) SetMaxRangeSeconds(v int64) { s.maxRangeSeconds = v }

// SetSessionOrgResolver injects the session-organization resolver used
// by the user-realm binding (feature-17 AD6).
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver used by the
// admin role check (feature #10).
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard that gates the admin
// observability RPCs by the caller's role (AD4).
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// NewForFVT constructs an observability service bound to a
// caller-provided GORM database. It exists so full-verification tests
// can wire the real service stack against a disposable database.
func NewForFVT(db *gorm.DB) *Service {
	return &Service{
		repo:            NewRepository(db),
		maxRangeSeconds: 92 * 24 * 3600,
	}
}

// MigrateSchemaForFVT applies the observability schema onto a
// caller-provided database for full-verification tests. The feature is
// read-only over metering's request_logs and the model/auth tables, so
// there is no observability-owned table to migrate; the two additive
// request_logs indexes are created here (Section 4.2).
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&requestLogRow{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	observabilityv1.RegisterObservabilityServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return observabilityv1.RegisterObservabilityServiceHandler
}

// Migrate implements server.Migrator: it creates the two additive
// request_logs indexes (Section 4.2) via GORM AutoMigrate on the
// request-log model. There is nothing to seed.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&requestLogRow{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "observability: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "observability: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the observability repository.
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

// requireAdminRole enforces the minimum org role on the admin
// observability RPCs (AD4). The RoleGuard resolves the caller from a
// session; in the transitional (session-less) path there is no session
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
	return s.roleGuard.RequireRole(ctx, orgID, userID, roleAdmin)
}

// surfaceFromContext derives the observability surface from the request
// path. The surface is a property of the binding, never a request field
// (feature #24, §3.3). The gateway forwards the request path in the
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
// configured cap returns 10404 (AD7).
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

// GetObservabilityOverview returns the fleet-wide observability: summary
// cards, a per-model table, and a model-filtered time series (AC1, AC2).
// It is admin-only (AD4): the fleet view is cross-org by default, with
// an optional org filter, gated by the caller's role.
func (s *Service) GetObservabilityOverview(ctx context.Context, req *observabilityv1.GetObservabilityOverviewRequest) (*observabilityv1.GetObservabilityOverviewResponse, error) {
	// The admin fleet view is cross-org by default (AD4). The org filter
	// is optional: read from the session active org or the transitional
	// header when the caller wants to scope to their own org.
	orgFilter := strings.TrimSpace(req.GetOrganizationId())
	if orgFilter == "" {
		orgFilter, _ = s.resolveOrg(ctx)
	}
	if err := s.requireAdminRole(ctx, orgFilter); err != nil {
		return nil, err
	}
	since, until, err := s.validateRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	modelFilter := strings.TrimSpace(req.GetModelId())
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	bucketSize := bucketSizeForRange(since, until)
	buckets, models, err := repo.AggregateOverview(ctx, orgFilter, modelFilter, since, until, bucketSize)
	if err != nil {
		return nil, err
	}
	watermark, err := repo.DataThrough(ctx, orgFilter, modelFilter, since, until, bucketSize)
	if err != nil {
		return nil, err
	}

	cards := buildCards(buckets, watermark)
	modelRows := make([]*observabilityv1.ModelObservabilityRow, 0, len(models))
	for _, m := range models {
		name, _ := repo.ModelName(ctx, m.ModelID)
		modelRows = append(modelRows, &observabilityv1.ModelObservabilityRow{
			ModelId:            m.ModelID,
			ModelName:          name,
			RequestCount:       m.RequestCount,
			ErrorCount:         m.ErrorCount,
			AvgLatencyMs:       m.AvgLatencyMs,
			P95LatencyMs:       m.P95LatencyMs,
			OutputTokensPerSec: tokensPerSec(m.OutputTokens, m.BucketSeconds),
			DataThrough:        m.DataThrough,
		})
	}
	series := buildSeries(buckets)

	return &observabilityv1.GetObservabilityOverviewResponse{
		Response: okResponse(),
		Cards:    cards,
		Models:   modelRows,
		Series:   series,
	}, nil
}

// GetModelObservability returns the single-model observability: summary
// cards, a time series, and a per-API-key breakdown (AC3, AC4). The
// admin binding covers all orgs (AD4); the user binding is hard-scoped
// to the caller's organization (AD4, D7).
func (s *Service) GetModelObservability(ctx context.Context, req *observabilityv1.GetModelObservabilityRequest) (*observabilityv1.GetModelObservabilityResponse, error) {
	modelID := strings.TrimSpace(req.GetModelId())
	if modelID == "" {
		return nil, apierrors.New(apierrors.CodeObservabilityModelNotFound)
	}
	surface := surfaceFromContext(ctx)

	var orgFilter string
	if surface == SurfaceAdmin {
		// Admin binding: cross-org by default, optional org filter (AD4).
		orgFilter = strings.TrimSpace(req.GetOrganizationId())
		if orgFilter == "" {
			orgFilter, _ = s.resolveOrg(ctx)
		}
		if err := s.requireAdminRole(ctx, orgFilter); err != nil {
			return nil, err
		}
	} else {
		// User binding: hard-scoped to the caller's org (AD4, D7).
		// X-Organization-Id is ignored; the session active org is
		// authoritative.
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
	// An unknown model returns 10801 (AD2).
	exists, err := repo.ModelExists(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, apierrors.New(apierrors.CodeObservabilityModelNotFound)
	}

	bucketSize := bucketSizeForRange(since, until)
	buckets, keys, err := repo.AggregateModel(ctx, orgFilter, modelID, since, until, bucketSize)
	if err != nil {
		return nil, err
	}
	watermark, err := repo.DataThrough(ctx, orgFilter, modelID, since, until, bucketSize)
	if err != nil {
		return nil, err
	}

	cards := buildCards(buckets, watermark)
	keyRows := make([]*observabilityv1.ObservabilityKeyRow, 0, len(keys))
	for _, k := range keys {
		name, _ := repo.APIKeyName(ctx, k.APIKeyID)
		keyRows = append(keyRows, &observabilityv1.ObservabilityKeyRow{
			ApiKeyId:           k.APIKeyID,
			ApiKeyName:         name,
			RequestCount:       k.RequestCount,
			ErrorCount:         k.ErrorCount,
			AvgLatencyMs:       k.AvgLatencyMs,
			P95LatencyMs:       k.P95LatencyMs,
			OutputTokensPerSec: tokensPerSec(k.OutputTokens, k.BucketSeconds),
		})
	}
	series := buildSeries(buckets)

	return &observabilityv1.GetModelObservabilityResponse{
		Response: okResponse(),
		Cards:    cards,
		Series:   series,
		Keys:     keyRows,
	}, nil
}

// buildCards derives the headline summary cards from the bucket rows.
// error_rate is derived client-side as error_count / request_count; the
// wire carries integer counts and integer milliseconds only (AD6).
func buildCards(buckets []BucketRow, watermark int64) *observabilityv1.ObservabilityCard {
	card := &observabilityv1.ObservabilityCard{DataThrough: watermark}
	var latencySum, latencyCount int64
	var latencies []int64
	for _, b := range buckets {
		card.RequestCount += b.RequestCount
		card.ErrorCount += b.ErrorCount
		card.OutputTokensPerSec += tokensPerSec(b.OutputTokens, b.BucketSeconds)
		card.InputTokensPerSec += tokensPerSec(b.InputTokens, b.BucketSeconds)
		latencySum += b.AvgLatencyMs * b.RequestCount
		latencyCount += b.RequestCount
		for i := int64(0); i < b.RequestCount; i++ {
			latencies = append(latencies, b.AvgLatencyMs)
		}
	}
	card.AvgLatencyMs = avg(latencySum, latencyCount)
	card.P95LatencyMs = percentile(latencies, 0.95)
	return card
}

// buildSeries maps the bucket rows to the wire series points.
func buildSeries(buckets []BucketRow) []*observabilityv1.ObservabilitySeriesPoint {
	out := make([]*observabilityv1.ObservabilitySeriesPoint, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, &observabilityv1.ObservabilitySeriesPoint{
			Bucket:             b.Bucket,
			RequestCount:       b.RequestCount,
			ErrorCount:         b.ErrorCount,
			AvgLatencyMs:       b.AvgLatencyMs,
			P95LatencyMs:       b.P95LatencyMs,
			OutputTokensPerSec: tokensPerSec(b.OutputTokens, b.BucketSeconds),
			InputTokensPerSec:  tokensPerSec(b.InputTokens, b.BucketSeconds),
		})
	}
	return out
}

// tokensPerSec derives tokens/sec from a token sum and the bucket size.
func tokensPerSec(tokens, bucketSeconds int64) int64 {
	if bucketSeconds <= 0 {
		return 0
	}
	return tokens / bucketSeconds
}

// okResponse returns the success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}

// Surface constants derived from the request path (feature #24, §3.3).
const (
	// SurfaceAdmin is the admin console surface.
	SurfaceAdmin = "admin"
	// SurfaceUser is the end-user console surface.
	SurfaceUser = "user"
)
