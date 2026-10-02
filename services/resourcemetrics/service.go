// Package resourcemetrics implements the read-only per-service resource
// utilization aggregation over the service_resource_metrics table
// (feature #37). It serves the admin Service Metrics page
// (GetServiceResourceMetrics), deriving per-service CPU/memory/GPU
// utilization over time from the sample rows the Controller writes.
package resourcemetrics

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	resourcemetricsv1 "github.com/go-taas/go-taas/proto/taas/resourcemetrics/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// ServiceName is the unique name of this service.
const ServiceName = "resourcemetrics"

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
	SessionActiveOrg(ctx context.Context) (string, error)
}

// SessionUserResolver resolves the authenticated caller's user id from
// the session (feature #10). It is implemented by the auth module and
// injected at wiring time.
type SessionUserResolver interface {
	SessionUserID(ctx context.Context) (string, error)
}

// RoleGuard enforces the minimum org role on the admin resource-metrics
// RPC. It is implemented by tenancy.RoleGuard.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// roleAdmin is the minimum org role for the admin resource-metrics RPC
// (AD1): the resource surface is operator-scoped.
const roleAdmin = "admin"

// ServiceExists reports whether an inference service id exists (the
// infer service contract, 10301). It is implemented by the infer module
// and injected at wiring time.
type ServiceExists interface {
	ServiceExists(ctx context.Context, serviceID string) (bool, error)
}

// Service implements the resourcemetrics gRPC service.
type Service struct {
	resourcemetricsv1.UnimplementedResourceMetricsServiceServer

	components server.Components

	// repo is the injection point used by tests and FVT; production
	// resolves it lazily from the shared components.
	repo *Repository

	// maxRangeSeconds caps the accepted range (AD5). It is read from
	// config at construction; tests may override it.
	maxRangeSeconds int64

	// sessionOrgResolver resolves the session's active organization
	// (feature-17 AD6). Nil until wired: the transitional
	// X-Organization-Id header is used.
	sessionOrgResolver SessionOrgResolver

	// sessionUserResolver resolves the caller's user id for the admin
	// role check (feature #10). Nil until wired: no role check.
	sessionUserResolver SessionUserResolver

	// roleGuard gates the admin resource-metrics RPC by the caller's
	// role (AD1). Nil until wired: no role check (unit tests).
	roleGuard RoleGuard

	// serviceExists validates the service_id against the infer service
	// contract (10301). Nil until wired: existence is not checked (unit
	// tests).
	serviceExists ServiceExists
}

// New constructs the resourcemetrics service. The repository is wired
// lazily on first use from the shared components.
func New(components server.Components) *Service {
	return &Service{
		components:      components,
		maxRangeSeconds: 92 * 24 * 3600,
	}
}

// SetMaxRangeSeconds overrides the accepted range cap (AD5). It is used
// by tests and FVT to exercise the 10404 boundary without a 92-day
// window.
func (s *Service) SetMaxRangeSeconds(v int64) { s.maxRangeSeconds = v }

// SetSessionOrgResolver injects the session-organization resolver used
// by the admin binding (feature-17 AD6).
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver used by the
// admin role check (feature #10).
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard that gates the admin
// resource-metrics RPC by the caller's role (AD1).
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// SetServiceExists injects the infer service-existence check (10301).
func (s *Service) SetServiceExists(e ServiceExists) { s.serviceExists = e }

// NewForFVT constructs a resourcemetrics service bound to a
// caller-provided GORM database. It exists so full-verification tests
// can wire the real service stack against a disposable database.
func NewForFVT(db *gorm.DB) *Service {
	return &Service{
		repo:            NewRepository(db),
		maxRangeSeconds: 92 * 24 * 3600,
	}
}

// MigrateSchemaForFVT applies the resourcemetrics schema onto a
// caller-provided database for full-verification tests.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&serviceResourceMetricRow{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	resourcemetricsv1.RegisterResourceMetricsServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return resourcemetricsv1.RegisterResourceMetricsServiceHandler
}

// Migrate implements server.Migrator: it creates the
// service_resource_metrics table via GORM AutoMigrate.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&serviceResourceMetricRow{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "resourcemetrics: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "resourcemetrics: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the resourcemetrics repository.
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
// resource-metrics RPC (AD1). The RoleGuard resolves the caller from a
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

// validateRange checks and defaults the since/until pair: until
// defaults to now, since to until-24h; since > until or a range > the
// configured cap returns 10404 (AD5).
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

// validMetric reports whether a metric dimension is one of the supported
// values (cpu / memory / gpu / empty).
func validMetric(metric string) bool {
	switch metric {
	case "", "cpu", "memory", "gpu":
		return true
	default:
		return false
	}
}

// GetServiceResourceMetrics returns per-service CPU/memory/GPU
// utilization over time: summary cards, a time-series of buckets, and a
// per-replica breakdown (AC1-AC3). It is admin-only (AD1).
func (s *Service) GetServiceResourceMetrics(ctx context.Context, req *resourcemetricsv1.GetServiceResourceMetricsRequest) (*resourcemetricsv1.GetServiceResourceMetricsResponse, error) {
	serviceID := strings.TrimSpace(req.GetServiceId())
	if serviceID == "" {
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	// An unknown service returns 10301 (AD9).
	if s.serviceExists != nil {
		exists, err := s.serviceExists.ServiceExists(ctx, serviceID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
		}
	}
	// An invalid metric dimension returns 12101 (AD9).
	metric := strings.TrimSpace(req.GetMetric())
	if !validMetric(metric) {
		return nil, apierrors.New(apierrors.CodeServiceMetricsInvalid)
	}
	replica := strings.TrimSpace(req.GetReplica())
	since, until, err := s.validateRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	bucketSize := bucketSizeForRange(since, until)
	buckets, replicas, err := repo.AggregateServiceMetrics(ctx, serviceID, since, until, bucketSize, replica)
	if err != nil {
		return nil, err
	}
	watermark, err := repo.ReadDataThrough(ctx, serviceID, since, until, bucketSize, replica)
	if err != nil {
		return nil, err
	}

	cards := buildCards(buckets, replicas, watermark)
	series := buildSeries(buckets)
	replicaRows := buildReplicaRows(replicas)

	return &resourcemetricsv1.GetServiceResourceMetricsResponse{
		Response: okResponse(),
		Cards:    cards,
		Series:   series,
		Replicas: replicaRows,
	}, nil
}

// buildCards derives the headline summary cards from the bucket rows and
// the per-replica rows.
func buildCards(buckets []BucketRow, replicas []ReplicaRow, watermark int64) *resourcemetricsv1.ResourceMetricsCard {
	card := &resourcemetricsv1.ResourceMetricsCard{DataThrough: watermark}
	if len(buckets) > 0 {
		last := buckets[len(buckets)-1]
		card.CurrentCpuPercent = last.CPUPercent
		card.CurrentMemoryBytes = last.MemoryBytes
		if last.HasGPU {
			card.CurrentGpuPercent = last.GPUPercent
		}
	}
	card.ReplicaCount = int64(len(replicas))
	return card
}

// buildSeries converts the bucket rows into the wire series. The metric
// switcher toggles the plotted dimension client-side with no refetch
// (AD8), so the full series is always returned.
func buildSeries(buckets []BucketRow) []*resourcemetricsv1.ResourceMetricsSeriesPoint {
	out := make([]*resourcemetricsv1.ResourceMetricsSeriesPoint, 0, len(buckets))
	for _, b := range buckets {
		p := &resourcemetricsv1.ResourceMetricsSeriesPoint{
			Bucket:       b.Bucket,
			CpuPercent:   b.CPUPercent,
			MemoryBytes:  b.MemoryBytes,
			GpuPercent:   b.GPUPercent,
		}
		if !b.HasGPU {
			p.GpuPercent = 0
		}
		out = append(out, p)
	}
	return out
}

// buildReplicaRows converts the replica rows into the wire rows.
func buildReplicaRows(replicas []ReplicaRow) []*resourcemetricsv1.ResourceMetricsReplicaRow {
	out := make([]*resourcemetricsv1.ResourceMetricsReplicaRow, 0, len(replicas))
	for _, r := range replicas {
		row := &resourcemetricsv1.ResourceMetricsReplicaRow{
			ReplicaIndex:       r.ReplicaIndex,
			CurrentCpuPercent:  r.CurrentCPUPercent,
			CurrentMemoryBytes: r.CurrentMemoryBytes,
			CurrentGpuPercent:  r.CurrentGPUPercent,
			DataThrough:        r.DataThrough,
		}
		if !r.HasGPU {
			row.CurrentGpuPercent = 0
		}
		out = append(out, row)
	}
	return out
}

// okResponse returns the standard success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}