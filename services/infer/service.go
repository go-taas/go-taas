// Package infer implements the inference service lifecycle API: creating,
// scaling and deleting inference services. Desired-state changes are
// published to the message queue; the controller applies them to the
// cluster.
package infer

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
	routingv1 "github.com/go-taas/go-taas/proto/taas/routing/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/image"
	"github.com/go-taas/go-taas/services/model"
	"github.com/go-taas/go-taas/services/tenancy"
)

// ServiceName is the unique name of this service.
const ServiceName = "infer"

// Pagination and replica bounds.
const (
	listDefaultLimit = 20
	listMaxLimit     = 100
	minReplicas      = 1
	maxReplicas      = 100
)

// organizationMetadataKey is the gRPC metadata key carrying the
// organization id (set by the gateway from the X-Organization-Id
// header).
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

// RoleGuard enforces the minimum org role on the admin infer RPCs
// (features #32/#33/#34/#35, AD). It is implemented by tenancy.RoleGuard.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// PlatformAdminGuard answers whether the caller is a platform
// administrator. The inference fleet is a platform-owned asset, so the admin infer
// RPCs answer to the platform, never to an organization administrator.
// It is implemented by tenancy.Service.
type PlatformAdminGuard interface {
	RequirePlatformAdmin(ctx context.Context) error
}

// roleAdmin is the minimum org role for the admin infer RPCs: the
// fleet/operator view is admin-scoped.
const roleAdmin = "admin"

// roleMember is the minimum org role for the user-realm infer RPCs
// (feature #35): the compare RPC requires the caller to be an org member.
const roleMember = "member"

// Surface constants derived from the request path (feature #29, §3.3).
const (
	// SurfaceAdmin is the admin console surface.
	SurfaceAdmin = "admin"
	// SurfaceUser is the end-user console surface.
	SurfaceUser = "user"
)

// serviceNamePattern matches DNS-safe names: lowercase alphanumerics
// and hyphens, 1-63 chars, not starting or ending with a hyphen.
var serviceNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Service implements the inference service lifecycle gRPC service.
type Service struct {
	inferv1.UnimplementedInferServiceServiceServer

	components server.Components
	repo       *InferenceServiceRepository
	modelRepo  *model.Repository
	mqClient   mq.Client

	// orgGuard validates the transitional organization context against
	// the organizations table (feature #6). Nil until wired: unit tests
	// skip validation; main.go and FVT always wire it.
	orgGuard *tenancy.OrgGuard

	// orgQuotaReader reads an org's resource quotas for the
	// CreateInferenceService service-count check (feature #51, AD13).
	// Nil until wired: the check reads as unlimited.
	orgQuotaReader tenancy.OrgQuotaReader

	// sessionOrgResolver resolves the session's active organization for
	// the user-realm playground (feature-17 AD6). Nil until wired: the
	// transitional X-Organization-Id header is used.
	sessionOrgResolver SessionOrgResolver

	// sessionUserResolver resolves the caller's user id for the admin
	// role check (feature #10). Nil until wired: no role check.
	sessionUserResolver SessionUserResolver

	// roleGuard gates the admin infer RPCs by the caller's role in the
	// resolved org context (features #32/#33/#34/#35). Nil until wired:
	// no role check (unit tests).
	roleGuard RoleGuard
	// platformAdminGuard gates the admin infer RPCs on the platform
	// administrator role: the inference fleet belongs to the platform.
	platformAdminGuard PlatformAdminGuard

	// gatewayResolver discovers the serving gateway's in-cluster base URL
	// so the quickstart endpoint works without manual configuration. Nil
	// until wired: only infer.endpointBaseURL is consulted.
	gatewayResolver GatewayResolver

	// platformSettings supplies the operator-configured data-plane
	// hostname. Nil until wired: the static configuration wins.
	platformSettings PlatformSettingsResolver

	// projectMembers lists a user's projects, so a project grant in an
	// access policy can be matched. Nil until wired.
	projectMembers ProjectMemberLister

	// subjectLabeller resolves access-policy subject ids to display
	// labels. Nil until wired: the raw id is reported.
	subjectLabeller SubjectLabeller

	// platformAdmin reports whether the caller is a platform
	// administrator, whose operator surface is not organization-isolated.
	// Nil until wired: the caller is treated as organization-scoped.
	platformAdmin PlatformAdminResolver

	// auditRecorder is the best-effort audit recorder (feature #15, AD3).
	// Nil until wired: no audit events are produced.
	auditRecorder AuditRecorder

	// compatibilityChecker resolves the compatibility status of a
	// (model, engine, card_type) combination for deploy-time enforcement
	// (feature #19, AD13). Nil until wired: no matrix check is applied.
	compatibilityChecker image.CompatibilityChecker

	// loadTestRepo persists load-test runs (feature #20). Wired lazily
	// from the shared components.
	loadTestRepo *LoadTestRepository
	// loadTestRunner executes load tests asynchronously (feature #20,
	// AD12). Nil until wired: CreateLoadTest fails closed (10311).
	loadTestRunner *LoadTestRunner
	// systemCredentialProvider resolves the synthetic platform
	// credential for the load-test runner (feature #20, AD11). Nil until
	// wired: CreateLoadTest fails closed (10311).
	systemCredentialProvider SystemCredentialProvider

	// logFetcher reads a service's container logs from the Kubernetes
	// API through the Controller (feature #33, AD2). Nil until wired:
	// the log RPCs fail closed (10301).
	logFetcher LogFetcher

	// deploymentEventRepo persists the deployment event trail (feature
	// #34, AD2). Wired lazily from the shared components.
	deploymentEventRepo *DeploymentEventRepository

	// routingPolicyRepo persists routing policies, revisions and the
	// publication outbox (feature #45, §4.1). Wired lazily from the
	// shared components.
	routingPolicyRepo *RoutingPolicyRepository

	// externalProxy proxies a published external model on the metered path
	// (feature #48, AD14). It is implemented by the provider module and
	// injected at wiring time. Nil until wired: an external playground
	// request fails closed.
	externalProxy ExternalModelProxy

	// routingResolver resolves a routing group name to an ordered
	// attempt list (feature #49, AD14). It is implemented by the routing
	// module and injected at wiring time. Nil until wired: a virtual
	// playground request fails closed.
	routingResolver RoutingResolver

	// sliceValidator validates a vGPU slice request at deploy time
	// (feature #52, AD13). It is implemented by the vgpu module and
	// injected at wiring time. Nil until wired: a slice request fails
	// closed (13403).
	sliceValidator SliceValidator

	// incidentReader reads a service's active incident for the tenant
	// status banner (feature #53, AD10). It is implemented by the fault
	// module and injected at wiring time. Nil until wired: the status
	// reports healthy.
	incidentReader IncidentReader

	// clusterResolver resolves the cluster a service is placed on at
	// deploy time (feature #40, AD7/AD8). Nil until wired: the service
	// is created without a cluster (single-cluster installs).
	clusterResolver ClusterResolver
}

// ClusterResolver resolves the cluster a new inference service is placed
// on (feature #40, AD7/AD8). It is implemented by the cluster module and
// injected at wiring time.
type ClusterResolver interface {
	// ResolveCluster returns the cluster id to place a service on. A
	// requested id is validated (must be an active cluster); an empty
	// request resolves to the platform default cluster (the configured
	// default, else the first active cluster). It returns 12401 when the
	// requested cluster is unknown and 12403 when it is disabled.
	ResolveCluster(ctx context.Context, requested string) (string, error)
}

// IncidentReader reads a service's active incident for the tenant
// status banner (feature #53, AD10). It is implemented by the fault
// module and injected at wiring time.
type IncidentReader interface {
	// ActiveIncidentForService returns the active incident for a
	// service, or nil when healthy.
	ActiveIncidentForService(ctx context.Context, serviceID string) (ServiceIncident, bool, error)
}

// ServiceIncident is the masked incident projection for the tenant
// banner (feature #53, AD10).
type ServiceIncident struct {
	IncidentID string
	Severity   string
	Status     string
	RootCause  string
	Message    string
}

// SliceValidator validates a vGPU slice request at deploy time (feature
// #52, AD13). It is implemented by the vgpu module and injected at
// wiring time.
type SliceValidator interface {
	// ValidateSlice returns the resolved slice template (memory GiB,
	// compute %) when the pool exists, is granted to the org, and the
	// template exists in the pool. It returns 13403 otherwise.
	ValidateSlice(ctx context.Context, orgID, poolID, templateID string) (SliceTemplateInfo, error)
}

// SliceTemplateInfo is the resolved slice template for a deploy
// (feature #52, AD13).
type SliceTemplateInfo struct {
	MemoryGiB  int64
	ComputePct int64
}

// ExternalModelProxy proxies a published external model on the metered path
// (feature #48, AD14). It is implemented by the provider module and injected
// at wiring time — the same setter pattern as SetModelAuthorizer. The
// production data plane (Envoy + Wasm) consumes the frozen
// ResolveExternalModel contract instead.
type ExternalModelProxy interface {
	// ProxyExternalChat resolves the catalog model, proxies an
	// OpenAI-compatible chat completion to the provider with the decrypted
	// key, emits a metering event (success or failure) and returns the
	// upstream body. Upstream 401/403 -> 13012; upstream 5xx/timeout ->
	// 13013; a disabled provider -> 13011; an unpublished model -> 13015.
	ProxyExternalChat(ctx context.Context, modelID, orgID, apiKeyID string, body []byte) ([]byte, error)
}

// RoutingResolver resolves a routing group name to an ordered attempt
// list (feature #49, AD14). It is implemented by the routing module and
// injected at wiring time — the same setter pattern as
// SetExternalModelProxy.
type RoutingResolver interface {
	// ResolveRoutingGroup resolves the group name against the caller's
	// org/key context and returns the ordered attempt list with the
	// matched rule and skipped candidates. Unknown group -> 13101;
	// disabled -> 13110; no eligible candidate -> 13109.
	ResolveRoutingGroup(ctx context.Context, req *routingv1.ResolveRoutingGroupRequest) (*routingv1.ResolveRoutingGroupResponse, error)
	// RecordDecision persists the decision trail for the decisions page
	// (feature #49, AD12). Best-effort: the caller never fails on it.
	RecordDecision(ctx context.Context, res *routingv1.ResolveRoutingGroupResponse, requestID, groupName, selectedModelID, selectedModelName string, outcomes map[string]string) error
}

// AuditRecorder is the best-effort, non-fatal audit recorder seam
// (feature #15, AD3). It is implemented by the audit module and injected
// at wiring time.
type AuditRecorder interface {
	// Record writes one audit event best-effort; it never returns an
	// error.
	Record(ctx context.Context, ev *audit.AuditEvent)
}

// New constructs the inference service from the shared server
// components.
func New(components server.Components) *Service {
	return &Service{components: components}
}

// SetOrgGuard injects the tenancy read guard (the SetDeleteModelGuard
// pattern). Production and FVT wire it; unit tests leave it nil so
// checkOrg no-ops.
func (s *Service) SetOrgGuard(g *tenancy.OrgGuard) { s.orgGuard = g }

// SetOrgQuotaReader injects the tenancy org-quota reader (feature #51,
// AD13). Nil reads as unlimited (existing FVT setups unchanged).
func (s *Service) SetOrgQuotaReader(r tenancy.OrgQuotaReader) { s.orgQuotaReader = r }

// SetSessionOrgResolver injects the session-organization resolver used
// by the user-realm playground (feature-17 AD6). Production and FVT wire
// the auth service; unit tests may inject a fake.
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver used by the
// admin role check (feature #10).
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard that gates the admin infer
// RPCs by the caller's role (features #32/#33/#34/#35).
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// SetPlatformAdminGuard injects the platform-administrator gate. When it
// is wired (production and FVT) the admin RPCs answer to the platform
// alone; a unit test that leaves it nil keeps the org-role fallback below
// so the org guard can be exercised on its own.
func (s *Service) SetPlatformAdminGuard(g PlatformAdminGuard) { s.platformAdminGuard = g }

// GatewayResolver discovers the in-cluster base URL of the serving
// gateway (e.g. the AIBrix Envoy gateway), "http://host:port" or "" when
// none is discoverable. It is implemented in the server wiring layer,
// which owns the Kubernetes client.
type GatewayResolver interface {
	InferenceGatewayURL(ctx context.Context) string
}

// PlatformSettingsResolver supplies the operator-configured data-plane
// endpoint. hostname is what users are told to call (no scheme, no path);
// scheme is "http" or "https"; internalURL is the in-cluster origin the
// platform itself calls, and is empty when it should be discovered.
type PlatformSettingsResolver interface {
	InferenceGateway(ctx context.Context) (hostname, scheme, internalURL string, err error)
}

// SetGatewayResolver injects the serving-gateway discovery seam.
func (s *Service) SetGatewayResolver(r GatewayResolver) { s.gatewayResolver = r }

// SetPlatformSettings resolves the operator-configured data-plane
// endpoint (hostname, scheme and in-cluster URL). It is implemented by the
// runtime module and injected at wiring time. Nil until wired: only the
// static configuration and gateway discovery are consulted.
func (s *Service) SetPlatformSettings(r PlatformSettingsResolver) { s.platformSettings = r }

// PlatformAdminResolver reports whether the caller carries the
// platform-admin role. It is implemented by the tenancy module and injected
// at wiring time, so the operator surface can list every organization's
// inference services without importing tenancy's role model here.
type PlatformAdminResolver interface {
	IsPlatformAdmin(ctx context.Context) (bool, error)
}

// SetPlatformAdminResolver injects the platform-administrator check.
func (s *Service) SetPlatformAdminResolver(r PlatformAdminResolver) { s.platformAdmin = r }

// SetAuditRecorder injects the best-effort audit recorder (feature #15,
// AD3). Production wires the audit module; unit tests may inject a fake.
func (s *Service) SetAuditRecorder(r AuditRecorder) { s.auditRecorder = r }

// SetSliceValidator injects the vGPU slice validator (feature #52,
// AD13). Production wires the vgpu module; unit tests may inject a fake.
func (s *Service) SetSliceValidator(v SliceValidator) { s.sliceValidator = v }

// SetIncidentReader injects the fault incident reader (feature #53,
// AD10). Production wires the fault module; unit tests may inject a
// fake.
func (s *Service) SetIncidentReader(r IncidentReader) { s.incidentReader = r }

// SetCompatibilityChecker installs the compatibility matrix checker for
// deploy-time enforcement (feature #19, AD13). It must be called before
// serving.
func (s *Service) SetCompatibilityChecker(c image.CompatibilityChecker) {
	s.compatibilityChecker = c
}

// SetSystemCredentialProvider installs the narrow accessor for the
// synthetic platform credential the load-test runner presents to the
// inference endpoint (feature #20, AD11). Production wires the auth
// service; unit tests may inject a fake.
func (s *Service) SetSystemCredentialProvider(p SystemCredentialProvider) {
	s.systemCredentialProvider = p
}

// SetLoadTestRunner installs the async load-test runner (feature #20).
// It must be called before serving; without it a load test cannot be
// created (10311). The enable/disable kill switch is a startup decision
// made at wiring time (main.go), so this path never reads global config.
func (s *Service) SetLoadTestRunner(r *LoadTestRunner) { s.loadTestRunner = r }

// SetLogFetcher installs the read-only log-fetch seam (feature #33,
// AD2). Production wires the controller-backed implementation; unit
// tests may inject a fake. Without it the log RPCs fail closed.
func (s *Service) SetLogFetcher(f LogFetcher) { s.logFetcher = f }

// SetExternalModelProxy installs the external-model proxy seam (feature
// #48, AD14). Production wires the provider module; unit tests may inject
// a fake. Without it an external playground request fails closed.
func (s *Service) SetExternalModelProxy(p ExternalModelProxy) { s.externalProxy = p }

// SetRoutingResolver installs the routing-group resolver seam (feature
// #49, AD14). Production wires the routing module; unit tests may inject
// a fake. Without it a virtual playground request fails closed.
func (s *Service) SetRoutingResolver(r RoutingResolver) { s.routingResolver = r }

// SetClusterResolver installs the cluster-placement resolver seam
// (feature #40, AD7/AD8). Production wires the cluster module; unit
// tests may inject a fake. Without it a service is created without a
// cluster (single-cluster installs).
func (s *Service) SetClusterResolver(r ClusterResolver) { s.clusterResolver = r }

// recordAudit writes one audit event best-effort (feature #15, AD3). A
// recorder failure is logged and never fails or rolls back the mutation.
func (s *Service) recordAudit(ctx context.Context, ev *audit.AuditEvent) {
	if s.auditRecorder == nil {
		return
	}
	s.auditRecorder.Record(ctx, ev)
}

// resolveOrg returns the organization context for the user-realm
// playground (feature-17 AD6): the session's active org when a session
// is present, otherwise the transitional X-Organization-Id header.
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

// clientEndpoints returns the endpoints a caller should use for a
// service: the platform inference gateway's public base URL. It falls back
// to the stored (in-cluster) endpoints when the platform has no gateway
// configured, so the field is never empty for a running service.
func (s *Service) clientEndpoints(ctx context.Context, row *InferenceService) []string {
	if resolved := s.resolveGatewaySettings(ctx); resolved.publicBaseURL != "" {
		return []string{resolved.publicBaseURL + "/v1"}
	}
	return row.Endpoints
}

// findService returns a service by id. Inference services are platform
// assets (like images), so nothing is scoped by organization: every
// management RPC resolves the row directly. Who may *use* a service is a
// separate question, answered by its access policy.
func (s *Service) findService(ctx context.Context, repo *InferenceServiceRepository, serviceID string) (*InferenceService, error) {
	return repo.FindByID(ctx, serviceID)
}

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

// checkServiceQuota enforces the org's max_inference_services quota at
// create time (feature #51, FR3.1). A nil reader or an unlimited quota
// skips the check; at the limit the create is rejected with 13302
// naming the current count and the limit. Existing services are never
// deleted by a tightening (D6).
func (s *Service) checkServiceQuota(ctx context.Context, orgID string) error {
	if s.orgQuotaReader == nil {
		return nil
	}
	q, err := s.orgQuotaReader.OrgQuota(ctx, orgID)
	if err != nil {
		return err
	}
	if q.MaxInferenceServices == 0 {
		return nil
	}
	repo, err := s.repository()
	if err != nil {
		return err
	}
	count, err := repo.CountActiveServicesByOrg(ctx, orgID)
	if err != nil {
		return err
	}
	if count >= q.MaxInferenceServices {
		return apierrors.Newf(apierrors.CodeQuotaServiceCountExceeded,
			"your organization has reached its limit of %d inference services; request an increase or remove an unused service", q.MaxInferenceServices)
	}
	return nil
}

// requireAdminRole enforces the minimum org role on the admin infer RPCs
// (features #32/#33/#34/#35). The RoleGuard resolves the caller from a
// session; in the transitional (session-less) path there is no session
// user to check, so the check is skipped and the org header itself is
// the access boundary (feature-17 AD6).
func (s *Service) requireAdminRole(ctx context.Context, orgID string) error {
	if s.platformAdminGuard != nil {
		return s.platformAdminGuard.RequirePlatformAdmin(ctx)
	}
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

// requireMemberRole enforces the minimum org role on the user-realm
// infer RPCs (feature #35, AD). The compare RPC is end-user surface but
// still requires the caller to be an org member (10036 for a non-member
// session). The RoleGuard resolves the caller from a session; in the
// transitional (session-less) path there is no session user to check, so
// the check is skipped and the org header itself is the access boundary.
func (s *Service) requireMemberRole(ctx context.Context, orgID string) error {
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

// surfaceFromContext derives the infer surface from the request path.
// The surface is a property of the binding, never a request field
// (feature #29, §3.3). The gateway forwards the request path in the
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

// NewWithDependencies constructs a Service with explicit dependencies
// (tests, tools).
func NewWithDependencies(
	repo *InferenceServiceRepository,
	modelRepo *model.Repository,
	mqClient mq.Client,
	deploymentEventRepo *DeploymentEventRepository,
) *Service {
	return &Service{
		repo:                repo,
		modelRepo:           modelRepo,
		mqClient:            mqClient,
		deploymentEventRepo: deploymentEventRepo,
		routingPolicyRepo:   NewRoutingPolicyRepository(repo.DB(context.Background())),
	}
}

// NewForFVT constructs a Service bound to a test database and MQ
// client for FVT.
func NewForFVT(db *gorm.DB, mqClient mq.Client) *Service {
	return NewWithDependencies(
		NewInferenceServiceRepository(db),
		model.NewRepository(db),
		mqClient,
		NewDeploymentEventRepository(db),
	)
}

// MigrateSchemaForFVT applies the infer schema (inference_services,
// autoscaling_policy, load_tests, deployment_events, routing_policies,
// routing_policy_revisions, routing_policy_outbox) to the given
// database. FVT-only helper.
func MigrateSchemaForFVT(db *gorm.DB) error {
	if err := db.AutoMigrate(&InferenceService{}, &AutoscalingPolicy{}, &LoadTest{}, &DeploymentEvent{}, &RoutingPolicy{}, &RoutingPolicyRevision{}, &RoutingPolicyOutbox{}, &InferenceServiceAccess{}); err != nil {
		return err
	}
	return NewAutoscalingPolicyRepository(db).SeedDefault(context.Background())
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	inferv1.RegisterInferServiceServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return inferv1.RegisterInferServiceServiceHandler
}

// Migrate applies the infer schema through the components' database.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	if err := db.WithContext(ctx).AutoMigrate(&InferenceService{}, &AutoscalingPolicy{}, &LoadTest{}, &DeploymentEvent{}, &RoutingPolicy{}, &RoutingPolicyRevision{}, &RoutingPolicyOutbox{}, &InferenceServiceAccess{}); err != nil {
		return err
	}
	// Seed the singleton global-default policy (feature #16, §4.3): the
	// global policy always exists.
	policyRepo := NewAutoscalingPolicyRepository(db)
	return policyRepo.SeedDefault(ctx)
}

// gormDB resolves the *gorm.DB handle from the components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "infer: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "infer: unexpected db handle type %T", raw)
	}
	return db, nil
}

// repository lazily resolves the inference service repository.
func (s *Service) repository() (*InferenceServiceRepository, error) {
	if s.repo != nil {
		return s.repo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.repo = NewInferenceServiceRepository(db)
	return s.repo, nil
}

// modelRepository lazily resolves the model repository.
func (s *Service) modelRepository() (*model.Repository, error) {
	if s.modelRepo != nil {
		return s.modelRepo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.modelRepo = model.NewRepository(db)
	return s.modelRepo, nil
}

// servedModelName resolves the catalog name of the model an inference
// service runs. Change events carry it because clients address the served
// endpoint by that name and AIBrix routes on it. A lookup failure falls
// back to the service name, which is always a valid serving name.
func servedModelName(ctx context.Context, repo *model.Repository, svc *InferenceService) string {
	if repo == nil || svc.ModelID == "" {
		return svc.Name
	}
	row, err := repo.GetModel(ctx, svc.ModelID)
	if err != nil || row.Name == "" {
		return svc.Name
	}
	return row.Name
}

// mqClientFor lazily resolves the MQ client.
func (s *Service) mqClientFor() (mq.Client, error) {
	if s.mqClient != nil {
		return s.mqClient, nil
	}
	if s.components == nil || s.components.MQ() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "infer: mq component unavailable")
	}
	client, ok := s.components.MQ().Client().(mq.Client)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "infer: unexpected mq handle type %T", s.components.MQ().Client())
	}
	s.mqClient = client
	return client, nil
}

// deploymentEventRepository lazily resolves the deployment event
// repository (feature #34, AD2).
func (s *Service) deploymentEventRepository() (*DeploymentEventRepository, error) {
	if s.deploymentEventRepo != nil {
		return s.deploymentEventRepo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.deploymentEventRepo = NewDeploymentEventRepository(db)
	return s.deploymentEventRepo, nil
}

// CreateInferenceService deploys a model with the given image and
// replicas. The change is published to the message queue and applied by
// the controller.
func (s *Service) CreateInferenceService(ctx context.Context, req *inferv1.CreateInferenceServiceRequest) (*inferv1.CreateInferenceServiceResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	// A disabled organization cannot deploy new services (FR3.2,
	// 10017).
	if err := s.checkOrg(ctx, orgID, true); err != nil {
		return nil, err
	}

	// Feature #51 (FR3.1): the org's inference-service quota is checked
	// as soon as the org context is validated, before any payload work.
	if err := s.checkServiceQuota(ctx, orgID); err != nil {
		return nil, err
	}

	// Validation order per architecture 4.3: name, replicas,
	// accelerator, model, version, image, compatibility. Nothing is
	// published unless every check passes (AC5).
	name := strings.TrimSpace(req.GetName())
	if !serviceNamePattern.MatchString(name) {
		return nil, apierrors.New(apierrors.CodeInferServiceStateInvalid)
	}
	// Service names are platform-unique: a service is a platform asset, so
	// two organizations must not be able to claim the same name — they
	// would share one Kubernetes Deployment and one serving-gateway route.
	{
		nameRepo, repoErr := s.repository()
		if repoErr != nil {
			return nil, repoErr
		}
		taken, nameErr := nameRepo.NameTaken(ctx, name)
		if nameErr != nil {
			return nil, nameErr
		}
		if taken {
			return nil, apierrors.Newf(apierrors.CodeInferServiceExists,
				"infer: a service named %q already exists; service names are platform-wide", name)
		}
	}
	replicas := int(req.GetReplicas())
	if replicas < minReplicas || replicas > maxReplicas {
		return nil, apierrors.New(apierrors.CodeInferReplicasInvalid)
	}
	accelerator := strings.ToLower(strings.TrimSpace(req.GetAccelerator()))
	if !image.IsValidAccelerator(accelerator) {
		return nil, apierrors.New(apierrors.CodeInferEngineUnsupported)
	}

	modelRepo, err := s.modelRepository()
	if err != nil {
		return nil, err
	}
	// The catalog name is the name clients pass as `model`; the change
	// event carries it so the controller advertises it to AIBrix and the
	// engine serves under it.
	modelRow, err := modelRepo.GetModel(ctx, req.GetModelId())
	if err != nil {
		return nil, err
	}
	modelVersion, err := modelRepo.FindVersion(ctx, req.GetModelId(), req.GetModelVersion())
	if err != nil {
		return nil, err
	}
	// Feature-13 control-plane gate (AC5): a restricted model may only be
	// deployed by a granted organization. The check runs before any
	// desired-state write, so a blocked deploy publishes nothing.
	authorized, err := modelRepo.IsModelAuthorized(ctx, req.GetModelId(), orgID)
	if err != nil {
		return nil, err
	}
	if !authorized {
		return nil, apierrors.New(apierrors.CodeModelUnauthorized)
	}
	img, err := image.Lookup(req.GetImageId())
	if err != nil {
		return nil, err
	}
	if img.Accelerator != accelerator {
		return nil, apierrors.New(apierrors.CodeImageIncompatible)
	}
	// Feature #19 (AD13): the deploy form consults the compatibility
	// matrix. An unsupported combination is blocked at deploy time; an
	// experimental combination deploys with a warning flag on the
	// response. The check runs before any desired-state write, so a
	// blocked deploy publishes nothing.
	//
	// A card type is optional (empty means "any card of this accelerator
	// family"), and the matrix is keyed by a concrete (model, engine,
	// card_type) triple. Without a card type there is no triple to look
	// up, so the gate is skipped rather than failed: a missing optional
	// field must never reject an otherwise valid deploy (10211).
	cardType := strings.TrimSpace(req.GetAcceleratorType())
	compatStatus := ""
	if s.compatibilityChecker != nil && cardType != "" {
		compatStatus, err = s.compatibilityChecker(ctx, req.GetModelId(), img.Engine, cardType)
		if err != nil {
			return nil, err
		}
		if compatStatus == image.StatusUnsupported {
			return nil, apierrors.Newf(apierrors.CodeCompatibilityUnsupported,
				"model %s is not supported on engine %s with card type %s; see the compatibility matrix",
				req.GetModelId(), img.Engine, cardType)
		}
	}
	// Feature #16: validate the explicit autoscaling policy synchronously
	// (AD10) before any write or publish.
	if err := validateAutoscalingPolicy(req.GetAutoscaling()); err != nil {
		return nil, err
	}

	// Feature #52 (AD13): the vGPU slice request is validated before any
	// write or publish. Both pool_id and slice_template_id must be
	// present together; absent = whole card. A slice request with a nil
	// validator fails closed (13403).
	poolID := strings.TrimSpace(req.GetPoolId())
	sliceTemplateID := strings.TrimSpace(req.GetSliceTemplateId())
	var sliceInfo SliceTemplateInfo
	if (poolID == "") != (sliceTemplateID == "") {
		return nil, apierrors.New(apierrors.CodeVGPUSliceUnavailable)
	}
	if poolID != "" {
		if s.sliceValidator == nil {
			return nil, apierrors.New(apierrors.CodeVGPUSliceUnavailable)
		}
		info, err := s.sliceValidator.ValidateSlice(ctx, orgID, poolID, sliceTemplateID)
		if err != nil {
			return nil, err
		}
		sliceInfo = info
	}

	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	client, err := s.mqClientFor()
	if err != nil {
		return nil, err
	}

	// Feature #16: store the explicit policy, or {} to inherit the global
	// default (FR1.2).
	var autoscalingJSON datatypes.JSON
	if req.GetAutoscaling() != nil {
		autoscalingJSON, err = policyToJSON(policyFromProto(req.GetAutoscaling()))
		if err != nil {
			return nil, err
		}
	} else {
		autoscalingJSON = datatypes.JSON("{}")
	}

	// Feature #40 (AD7/AD8): resolve the cluster the service is placed
	// on. A requested cluster_id is validated; an empty request resolves
	// to the platform default cluster. Without a resolver (single-cluster
	// installs) the service is created without a cluster.
	clusterID := ""
	if s.clusterResolver != nil {
		clusterID, err = s.clusterResolver.ResolveCluster(ctx, strings.TrimSpace(req.GetClusterId()))
		if err != nil {
			return nil, err
		}
	}

	svc := &InferenceService{
		ID:              NewServiceID(),
		OrganizationID:  orgID,
		Name:            name,
		ModelID:         req.GetModelId(),
		ModelVersion:    modelVersion.Version,
		ImageID:         req.GetImageId(),
		Replicas:        replicas,
		Accelerator:     accelerator,
		AcceleratorType: strings.TrimSpace(req.GetAcceleratorType()),
		State:           StatePending,
		Endpoints:       []string{},
		Autoscaling:     autoscalingJSON,
		PoolID:          poolID,
		SliceTemplateID: sliceTemplateID,
		SliceGPUIndex:   -1,
		SliceMemoryGiB:  sliceInfo.MemoryGiB,
		SliceComputePct: sliceInfo.ComputePct,
		ClusterID:       clusterID,
	}
	if err := repo.Create(ctx, svc); err != nil {
		return nil, err
	}

	evt := buildChangeEvent(EventTypeUpsert, svc, modelRow.Name, modelVersion.WeightPath, img.Reference(), img.Engine)
	// Feature #16: the change event carries the effective policy (the
	// global default when the service stores {}).
	effective, err := repo.ResolveEffectivePolicy(ctx, svc)
	if err != nil {
		return nil, err
	}
	evt.Autoscaling = effective
	if err := publishChangeWithCompensation(ctx, client, repo, evt, svc.ID); err != nil {
		logger.S().Errorw("infer: publish create change failed",
			"service_id", svc.ID, "err", err)
		return nil, apierrors.Newf(apierrors.CodeInternal, "infer: publish change failed")
	}

	// Feature #15: record the successful deploy best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         "inference_service.create",
		ResourceType:   "inference_service",
		ResourceID:     svc.ID,
		Result:         "success",
	})
	// Feature #34: record the create event in the deployment trail. The
	// before side is empty (nothing existed before the create).
	s.recordLifecycleEvent(ctx, nil, svc, DeploymentEventCreate, s.actorFor(ctx, orgID))

	return &inferv1.CreateInferenceServiceResponse{
		Response:  okResponse(),
		ServiceId: svc.ID,
		Warning:   experimentalWarning(compatStatus),
	}, nil
}

// experimentalWarning returns the warning banner text when the deployed
// combination is experimental, or "" otherwise (feature #19, AD13).
func experimentalWarning(compatStatus string) string {
	if compatStatus == image.StatusExperimental {
		return "This model/engine/card-type combination is experimental and not fully validated."
	}
	return ""
}

// ListInferenceServices returns the inference services of the caller's
// organization.
func (s *Service) ListInferenceServices(ctx context.Context, req *inferv1.ListInferenceServicesRequest) (*inferv1.ListInferenceServicesResponse, error) {
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

	offset, limit := normalizePagination(req.GetPage())
	// Inference services are platform assets, so the list is platform-wide
	// for everyone: there is no organization to scope it by. The legacy
	// organization_id request field is still honoured as a filter, which is
	// how an operator narrows the list by the creating organization.
	rows, total, err := repo.ListAll(ctx, strings.TrimSpace(req.GetOrganizationId()), offset, limit, false)
	if err != nil {
		return nil, err
	}

	// Feature #16: resolve the global default once so the list summary
	// can merge it for services that inherit ({}).
	globalDefault, err := s.globalAutoscalingDefault(ctx)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	restricted := s.restrictedServiceIDs(ctx, repo, ids)
	services := make([]*inferv1.InferenceServiceSummary, 0, len(rows))
	for _, row := range rows {
		effective := effectivePolicyForRow(row, globalDefault)
		summary := summarizeService(row, effective)
		summary.AccessRestricted = restricted[row.ID]
		services = append(services, summary)
	}
	return &inferv1.ListInferenceServicesResponse{
		Response: okResponse(),
		Services: services,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampToInt32(limit)},
	}, nil
}

// GetInferenceService returns one inference service with its status.
func (s *Service) GetInferenceService(ctx context.Context, req *inferv1.GetInferenceServiceRequest) (*inferv1.GetInferenceServiceResponse, error) {
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

	row, err := s.findService(ctx, repo, req.GetServiceId())
	if err != nil {
		return nil, err
	}

	// Feature #16: resolve the effective policy and the autoscaling
	// status block.
	effective, err := repo.ResolveEffectivePolicy(ctx, row)
	if err != nil {
		return nil, err
	}

	// Endpoints are only exposed while running, and they name the platform
	// inference gateway rather than this service's in-cluster address: the
	// gateway is what a caller actually talks to, and it routes to the
	// service by model name. The controller still stores the in-cluster
	// address, which stays useful for debugging.
	var endpoints []string
	if row.State == StateRunning {
		endpoints = s.clientEndpoints(ctx, row)
	}
	summary := summarizeService(row, effective)
	summary.AccessRestricted = s.restrictedServiceIDs(ctx, repo, []string{row.ID})[row.ID]
	return &inferv1.GetInferenceServiceResponse{
		Response:          okResponse(),
		Service:           summary,
		Endpoints:         endpoints,
		Autoscaling:       policyToProto(effective),
		AutoscalingStatus: autoscalingStatusFromRow(row),
		AssignedSlice:     assignedSliceFromRow(row),
	}, nil
}

// assignedSliceFromRow returns the assigned-slice projection for a
// shared-GPU service, or nil for a whole-card service or before
// scheduling (feature #52, AD14).
func assignedSliceFromRow(row *InferenceService) *inferv1.AssignedSlice {
	if row.PoolID == "" {
		return nil
	}
	return &inferv1.AssignedSlice{
		PoolId:       row.PoolID,
		TemplateName: row.SliceTemplateID,
		MemoryGib:    row.SliceMemoryGiB,
		ComputePct:   row.SliceComputePct,
		NodeId:       row.SliceNodeID,
		GpuIndex:     int64(row.SliceGPUIndex),
	}
}

// globalAutoscalingDefault resolves the global default policy, falling
// back to the shipped defaults when the singleton is not seeded.
func (s *Service) globalAutoscalingDefault(ctx context.Context) (*AutoscalingPolicy, error) {
	policyRepo, err := s.autoscalingPolicyRepository()
	if err != nil {
		return nil, err
	}
	policy, err := policyRepo.GetDefault(ctx)
	if err != nil {
		// The row is seeded at migration; a miss is an infrastructure
		// invariant violation, but fall back to defaults so reads still
		// work.
		return defaultAutoscalingPolicy(), nil
	}
	return policy, nil
}

// effectivePolicyForRow returns the effective policy for a row: the
// stored policy when present, otherwise the global default.
func effectivePolicyForRow(row *InferenceService, globalDefault *AutoscalingPolicy) *AutoscalingPolicy {
	if len(row.Autoscaling) > 0 && string(row.Autoscaling) != "{}" && string(row.Autoscaling) != "null" {
		var p AutoscalingPolicy
		if err := json.Unmarshal(row.Autoscaling, &p); err == nil {
			return &p
		}
	}
	return globalDefault
}

// autoscalingStatusFromRow maps a row's autoscaling status columns to the
// proto status block.
func autoscalingStatusFromRow(row *InferenceService) *inferv1.AutoscalingStatus {
	status := &inferv1.AutoscalingStatus{
		State:              autoscalingStateProto(row.AutoscalingState),
		CurrentReplicas:    clampToInt32(row.AutoscalingCurrentReplicas),
		DesiredReplicas:    clampToInt32(row.AutoscalingDesiredReplicas),
		CurrentConcurrency: clampToInt32(row.AutoscalingCurrentConcurrency),
		TargetConcurrency:  clampToInt32(row.AutoscalingTargetConcurrency),
		ErrorReason:        row.AutoscalingErrorReason,
	}
	if row.AutoscalingLastScalingEventAt != nil {
		status.LastScalingEventAt = row.AutoscalingLastScalingEventAt.Unix()
	}
	return status
}

// ScaleInferenceService changes the replica count of an inference
// service.
func (s *Service) ScaleInferenceService(ctx context.Context, req *inferv1.ScaleInferenceServiceRequest) (*inferv1.ScaleInferenceServiceResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	replicas := int(req.GetReplicas())
	if replicas < minReplicas || replicas > maxReplicas {
		return nil, apierrors.New(apierrors.CodeInferReplicasInvalid)
	}

	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	client, err := s.mqClientFor()
	if err != nil {
		return nil, err
	}

	row, err := s.findService(ctx, repo, req.GetServiceId())
	if err != nil {
		return nil, err
	}
	if row.State == StateTerminated {
		return nil, apierrors.New(apierrors.CodeInferServiceStateInvalid)
	}

	// Spec-only update: the state is untouched (AC8).
	if err := repo.UpdateReplicas(ctx, orgID, req.GetServiceId(), replicas); err != nil {
		return nil, err
	}

	// Re-read the model and image to compose the resolved spec.
	modelRepo, err := s.modelRepository()
	if err != nil {
		return nil, err
	}
	modelVersion, err := modelRepo.FindVersion(ctx, row.ModelID, row.ModelVersion)
	if err != nil {
		return nil, err
	}
	img, err := image.Lookup(row.ImageID)
	if err != nil {
		return nil, err
	}

	scaled := *row
	scaled.Replicas = replicas
	evt := buildChangeEvent(EventTypeUpsert, &scaled, servedModelName(ctx, modelRepo, row),
		modelVersion.WeightPath, img.Reference(), img.Engine)
	if err := publishChange(ctx, client, evt); err != nil {
		logger.S().Errorw("infer: publish scale change failed",
			"service_id", req.GetServiceId(), "err", err)
		return nil, apierrors.Newf(apierrors.CodeInternal, "infer: publish change failed")
	}

	// Feature #15: record the successful scale best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         "inference_service.scale",
		ResourceType:   "inference_service",
		ResourceID:     req.GetServiceId(),
		Result:         "success",
	})
	// Feature #34: record the scale event in the deployment trail with
	// the before (old replicas) and after (new replicas) states.
	scaledRow, _ := s.findService(ctx, repo, req.GetServiceId())
	if scaledRow != nil {
		s.recordLifecycleEvent(ctx, row, scaledRow, DeploymentEventScale, s.actorFor(ctx, orgID))
	}

	return &inferv1.ScaleInferenceServiceResponse{Response: okResponse()}, nil
}

// DeleteInferenceService removes an inference service.
func (s *Service) DeleteInferenceService(ctx context.Context, req *inferv1.DeleteInferenceServiceRequest) (*inferv1.DeleteInferenceServiceResponse, error) {
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
	client, err := s.mqClientFor()
	if err != nil {
		return nil, err
	}

	row, err := s.findService(ctx, repo, req.GetServiceId())
	if err != nil {
		return nil, err
	}

	// Idempotent delete: an already-terminated service succeeds without
	// publishing a second event (AC9).
	if row.State == StateTerminated {
		return &inferv1.DeleteInferenceServiceResponse{Response: okResponse()}, nil
	}

	if err := repo.MarkTerminated(ctx, orgID, req.GetServiceId()); err != nil {
		return nil, err
	}

	// A publish failure only logs: the row is already terminated, so the
	// service is hidden from lists and the controller's periodic resync
	// (feature #4) eventually tears the resources down.
	if err := publishChange(ctx, client, buildChangeEvent(EventTypeDelete, row, "", "", "", "")); err != nil {
		logger.S().Warnw("infer: publish delete change failed",
			"service_id", req.GetServiceId(), "err", err)
	}

	// Feature #15: record the successful delete best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         "inference_service.delete",
		ResourceType:   "inference_service",
		ResourceID:     req.GetServiceId(),
		Result:         "success",
	})
	// Feature #34: record the delete event in the deployment trail. The
	// after side is empty (nothing remains after the delete).
	s.recordLifecycleEvent(ctx, row, nil, DeploymentEventDelete, s.actorFor(ctx, orgID))

	return &inferv1.DeleteInferenceServiceResponse{Response: okResponse()}, nil
}

// UpdateInferenceServiceVersion changes a service's model_version in
// place, keeping the same service_id and endpoints (feature #32, AC3).
// The service transitions running → deploying → running. Validation
// order: service exists (10301), state updatable (10303), target version
// registered for the service's model (10103).
func (s *Service) UpdateInferenceServiceVersion(ctx context.Context, req *inferv1.UpdateInferenceServiceVersionRequest) (*inferv1.UpdateInferenceServiceVersionResponse, error) {
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
	client, err := s.mqClientFor()
	if err != nil {
		return nil, err
	}

	row, err := s.findService(ctx, repo, req.GetServiceId())
	if err != nil {
		return nil, err
	}
	if row.State == StateTerminated {
		return nil, apierrors.New(apierrors.CodeInferServiceStateInvalid)
	}
	targetVersion := strings.TrimSpace(req.GetModelVersion())
	if targetVersion == "" {
		return nil, apierrors.New(apierrors.CodeModelVersionNotFound)
	}
	modelRepo, err := s.modelRepository()
	if err != nil {
		return nil, err
	}
	// The target version must be a registered version of the service's
	// model (10103).
	target, err := modelRepo.FindVersion(ctx, row.ModelID, targetVersion)
	if err != nil {
		return nil, err
	}

	// Spec-only update: change model_version and transition to deploying.
	if err := repo.UpdateModelVersion(ctx, orgID, req.GetServiceId(), target.Version); err != nil {
		return nil, err
	}

	// Re-read the image to compose the resolved spec.
	img, err := image.Lookup(row.ImageID)
	if err != nil {
		return nil, err
	}

	updated := *row
	updated.ModelVersion = target.Version
	updated.State = StateDeploying
	evt := buildChangeEvent(EventTypeUpdateVersion, &updated, servedModelName(ctx, modelRepo, row),
		target.WeightPath, img.Reference(), img.Engine)
	if err := publishChange(ctx, client, evt); err != nil {
		logger.S().Errorw("infer: publish version-change failed",
			"service_id", req.GetServiceId(), "err", err)
		return nil, apierrors.Newf(apierrors.CodeInternal, "infer: publish change failed")
	}

	// Feature #15: record the successful version change best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         "inference_service.update_version",
		ResourceType:   "inference_service",
		ResourceID:     req.GetServiceId(),
		Result:         "success",
	})

	// Feature #34: record the update event in the deployment trail with
	// the before (old version) and after (new version) states.
	s.recordLifecycleEvent(ctx, row, &updated, DeploymentEventUpdate, s.actorFor(ctx, orgID))

	return &inferv1.UpdateInferenceServiceVersionResponse{
		Response:  okResponse(),
		ServiceId: req.GetServiceId(),
		State:     StateDeploying,
	}, nil
}

// ListServiceLogPods returns the service's pods/containers masked as
// replica indices (feature #33, AC1). Unknown service → 10301.
func (s *Service) ListServiceLogPods(ctx context.Context, req *inferv1.ListServiceLogPodsRequest) (*inferv1.ListServiceLogPodsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	// The admin log RPCs are gated by the caller's org role (feature
	// #33, §3.3): a non-member admin session receives 10036.
	if surfaceFromContext(ctx) == SurfaceAdmin {
		if err := s.requireAdminRole(ctx, orgID); err != nil {
			return nil, err
		}
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if _, err := s.findService(ctx, repo, req.GetServiceId()); err != nil {
		return nil, err
	}
	if s.logFetcher == nil {
		return nil, apierrors.Newf(apierrors.CodeInferLogsUnavailable,
			"infer: service logs are unavailable; the server has no Kubernetes client")
	}
	pods, err := s.logFetcher.ListServiceLogPods(ctx, req.GetServiceId())
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodeInferLogsUnavailable, "infer: %v", err)
	}
	out := make([]*inferv1.ServiceLogPod, 0, len(pods))
	for _, p := range pods {
		out = append(out, &inferv1.ServiceLogPod{
			ReplicaIndex: p.ReplicaIndex,
			Container:    p.Container,
			State:        p.State,
		})
	}
	return &inferv1.ListServiceLogPodsResponse{
		Response: okResponse(),
		Pods:     out,
	}, nil
}

// GetServiceLogs returns a bounded window of log lines for a chosen
// pod/container (feature #33, AC2/AC3). Validation order: service exists
// (10301), tail/since valid (10404), pod/container known (10304).
func (s *Service) GetServiceLogs(ctx context.Context, req *inferv1.GetServiceLogsRequest) (*inferv1.GetServiceLogsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	// The admin log RPCs are gated by the caller's org role (feature
	// #33, §3.3): a non-member admin session receives 10036.
	if surfaceFromContext(ctx) == SurfaceAdmin {
		if err := s.requireAdminRole(ctx, orgID); err != nil {
			return nil, err
		}
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if _, err := s.findService(ctx, repo, req.GetServiceId()); err != nil {
		return nil, err
	}
	tail := int(req.GetTail())
	if tail == 0 {
		tail = 500
	}
	if tail < 1 || tail > 5000 {
		return nil, apierrors.New(apierrors.CodeMeteringRangeInvalid)
	}
	if since := strings.TrimSpace(req.GetSince()); since != "" {
		if _, err := time.Parse(time.RFC3339, since); err != nil {
			return nil, apierrors.New(apierrors.CodeMeteringRangeInvalid)
		}
	}
	if s.logFetcher == nil {
		return nil, apierrors.Newf(apierrors.CodeInferLogsUnavailable,
			"infer: service logs are unavailable; the server has no Kubernetes client")
	}
	window, err := s.logFetcher.GetServiceLogs(
		ctx, req.GetServiceId(), strings.TrimSpace(req.GetPod()),
		strings.TrimSpace(req.GetContainer()), tail, strings.TrimSpace(req.GetSince()),
		strings.TrimSpace(req.GetNextOffset()),
	)
	if err != nil {
		// The fetcher's errors are actionable ("unknown replica", "no
		// pods") and a bare "internal error" leaves the operator with
		// nothing to act on.
		return nil, apierrors.Newf(apierrors.CodeInferLogsUnavailable,
			"infer: %v", err)
	}
	lines := make([]*inferv1.ServiceLogLine, 0, len(window.Lines))
	for _, l := range window.Lines {
		lines = append(lines, &inferv1.ServiceLogLine{
			Timestamp: l.Timestamp,
			Level:     l.Level,
			Message:   l.Message,
		})
	}
	return &inferv1.GetServiceLogsResponse{
		Response:   okResponse(),
		Lines:      lines,
		NextOffset: window.NextOffset,
		HasMore:    window.HasMore,
	}, nil
}

// ListDeploymentEvents returns the deployment event trail, newest first,
// with field-level diffs, filtered by service/event type/actor/time range
// (feature #34, AC1). Unknown service_id → 10301; invalid range → 10404.
func (s *Service) ListDeploymentEvents(ctx context.Context, req *inferv1.ListDeploymentEventsRequest) (*inferv1.ListDeploymentEventsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	// The admin deployment RPCs are gated by the caller's org role
	// (feature #34, §3.3): a non-member admin session receives 10036.
	if surfaceFromContext(ctx) == SurfaceAdmin {
		if err := s.requireAdminRole(ctx, orgID); err != nil {
			return nil, err
		}
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	evtRepo, err := s.deploymentEventRepository()
	if err != nil {
		return nil, err
	}
	serviceID := strings.TrimSpace(req.GetServiceId())
	if serviceID != "" {
		if _, err := s.findService(ctx, repo, serviceID); err != nil {
			return nil, err
		}
	}
	since, until := req.GetSince(), req.GetUntil()
	if since > 0 && until > 0 && since > until {
		return nil, apierrors.New(apierrors.CodeMeteringRangeInvalid)
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := evtRepo.ListEvents(ctx, DeploymentEventFilter{
		ServiceID: serviceID,
		EventType: strings.TrimSpace(req.GetEventType()),
		Actor:     strings.TrimSpace(req.GetActor()),
		Since:     since,
		Until:     until,
	}, offset, limit)
	if err != nil {
		return nil, err
	}
	events := make([]*inferv1.DeploymentEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, &inferv1.DeploymentEvent{
			EventId:     row.ID,
			ServiceId:   row.ServiceID,
			ServiceName: row.ServiceName,
			EventType:   row.EventType,
			Actor:       row.Actor,
			Before:      string(row.Before),
			After:       string(row.After),
			CreatedAt:   row.CreatedAt.Unix(),
		})
	}
	return &inferv1.ListDeploymentEventsResponse{
		Response: okResponse(),
		Events:   events,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampToInt32(limit)},
	}, nil
}

// RollbackDeployment reverts a service to a selected historical state
// (the before of a chosen event), keeping the same service_id and
// endpoints (feature #34, AC3). Validation order: service exists (10301),
// state updatable (10303), event exists and belongs to the service
// (10304).
func (s *Service) RollbackDeployment(ctx context.Context, req *inferv1.RollbackDeploymentRequest) (*inferv1.RollbackDeploymentResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	// The admin deployment RPCs are gated by the caller's org role
	// (feature #34, §3.3): a non-member admin session receives 10036.
	if surfaceFromContext(ctx) == SurfaceAdmin {
		if err := s.requireAdminRole(ctx, orgID); err != nil {
			return nil, err
		}
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	evtRepo, err := s.deploymentEventRepository()
	if err != nil {
		return nil, err
	}
	client, err := s.mqClientFor()
	if err != nil {
		return nil, err
	}

	row, err := s.findService(ctx, repo, req.GetServiceId())
	if err != nil {
		return nil, err
	}
	if row.State == StateTerminated {
		return nil, apierrors.New(apierrors.CodeInferServiceStateInvalid)
	}
	evt, err := evtRepo.GetEvent(ctx, req.GetEventId())
	if err != nil {
		return nil, err
	}
	if evt.ServiceID != row.ID {
		return nil, apierrors.New(apierrors.CodeInferEndpointNotFound)
	}

	// The rollback target is the event's before state. The before diff
	// omits unchanged fields, so the target is merged onto the current
	// state: only the fields the target event changed are restored.
	target, err := specFromJSON(evt.Before)
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "infer: malformed rollback target")
	}
	if target.isEmpty() {
		return nil, apierrors.New(apierrors.CodeInferServiceStateInvalid)
	}
	modelRepo, err := s.modelRepository()
	if err != nil {
		return nil, err
	}
	// The target version, when present, must be a registered version of
	// the service's model.
	if target.ModelVersion != "" {
		if _, err := modelRepo.FindVersion(ctx, row.ModelID, target.ModelVersion); err != nil {
			return nil, err
		}
	}

	// Apply the target as the new desired state.
	if err := repo.ApplyRollbackState(ctx, orgID, row.ID, target); err != nil {
		return nil, err
	}

	// Compose the rolled-back desired state by merging the target onto
	// the current row.
	updated := *row
	if target.ModelVersion != "" {
		updated.ModelVersion = target.ModelVersion
	}
	if target.ImageID != "" {
		updated.ImageID = target.ImageID
	}
	if target.Replicas != 0 {
		updated.Replicas = target.Replicas
	}
	if target.Accelerator != "" {
		updated.Accelerator = target.Accelerator
	}
	if target.AcceleratorType != "" {
		updated.AcceleratorType = target.AcceleratorType
	}
	updated.State = StateDeploying
	img, err := image.Lookup(updated.ImageID)
	if err != nil {
		return nil, err
	}
	evtChange := buildChangeEvent(EventTypeRollback, &updated, servedModelName(ctx, modelRepo, row),
		target.WeightPath, img.Reference(), img.Engine)
	if err := publishChange(ctx, client, evtChange); err != nil {
		logger.S().Errorw("infer: publish rollback change failed",
			"service_id", req.GetServiceId(), "err", err)
		return nil, apierrors.Newf(apierrors.CodeInternal, "infer: publish change failed")
	}

	// Record the rollback event (FR2.3): the diff is from the current
	// state to the rolled-back state.
	s.recordLifecycleEvent(ctx, row, &updated, DeploymentEventRollback, s.actorFor(ctx, orgID))

	// Feature #15: record the successful rollback best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         "inference_service.rollback",
		ResourceType:   "inference_service",
		ResourceID:     req.GetServiceId(),
		Result:         "success",
	})

	return &inferv1.RollbackDeploymentResponse{
		Response:  okResponse(),
		ServiceId: req.GetServiceId(),
		State:     StateDeploying,
	}, nil
}

// recordLifecycleEvent records a deployment event in the trail
// (feature #34, AD2). before/after are the service states on either side
// of the mutation; a nil side renders as an empty diff (the "before" of
// a create or the "after" of a delete). Best-effort: a recorder failure
// is logged and never fails the mutation.
func (s *Service) recordLifecycleEvent(ctx context.Context, before, after *InferenceService, eventType, actor string) {
	evtRepo, err := s.deploymentEventRepository()
	if err != nil {
		logger.S().Warnw("infer: deployment event repo unavailable", "err", err)
		return
	}
	svc := after
	if svc == nil {
		svc = before
	}
	if svc == nil {
		return
	}
	beforeJSON, afterJSON := specDiff(before, after)
	_ = evtRepo.RecordEvent(ctx, &DeploymentEvent{
		ServiceID:   svc.ID,
		ServiceName: svc.Name,
		EventType:   eventType,
		Actor:       actor,
		Before:      beforeJSON,
		After:       afterJSON,
	})
}

// actorFor resolves the actor for a deployment event: the caller's
// organization id (transitional) or "system".
func (s *Service) actorFor(_ context.Context, orgID string) string {
	if orgID != "" {
		return orgID
	}
	return "system"
}

// RecordDeploymentEvent implements the fault module's DeploymentTrail
// seam (feature #53, AD16). It records a recovery action in the
// deployment-history trail. Best-effort: a failure is logged and never
// fails the recovery.
func (s *Service) RecordDeploymentEvent(ctx context.Context, serviceID, eventType, actor, _ string) error {
	repo, err := s.repository()
	if err != nil {
		return err
	}
	row, err := repo.FindByID(ctx, serviceID)
	if err != nil {
		return err
	}
	evtRepo, err := s.deploymentEventRepository()
	if err != nil {
		return err
	}
	beforeJSON, afterJSON := specDiff(row, row)
	_ = evtRepo.RecordEvent(ctx, &DeploymentEvent{
		ServiceID:   row.ID,
		ServiceName: row.Name,
		EventType:   eventType,
		Actor:       actor,
		Before:      beforeJSON,
		After:       afterJSON,
	})
	return nil
}

// resolveOrganizationID reads the organization id from the incoming
// gRPC metadata (set by the gateway from X-Organization-Id).
func resolveOrganizationID(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	values := md.Get(organizationMetadataKey)
	if len(values) == 0 || values[0] == "" {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	return values[0], nil
}

// summarizeService maps a row to the API summary. effective is the
// resolved effective autoscaling policy (the global default when the
// service stores {}).
func summarizeService(row *InferenceService, effective *AutoscalingPolicy) *inferv1.InferenceServiceSummary {
	enabled := false
	min, max := 0, 0
	if effective != nil {
		enabled = effective.Enabled
		min = effective.MinReplicas
		max = effective.MaxReplicas
	}
	return &inferv1.InferenceServiceSummary{
		ServiceId:          row.ID,
		Name:               row.Name,
		ModelId:            row.ModelID,
		ModelVersion:       row.ModelVersion,
		ImageId:            row.ImageID,
		Replicas:           clampToInt32(row.Replicas),
		State:              row.State,
		UpdatedAt:          row.UpdatedAt.Unix(),
		AutoscalingEnabled: enabled,
		CurrentReplicas:    clampToInt32(row.AutoscalingCurrentReplicas),
		MinReplicas:        clampToInt32(min),
		MaxReplicas:        clampToInt32(max),
		AutoscalingState:   autoscalingStateProto(row.AutoscalingState),
		OrganizationId:     row.OrganizationID,
		// The accelerator the service was created for. It is what the console
		// shows as "accelerator", and it was missing from the summary
		// entirely — the detail page read a field the API never sent, so the
		// row rendered empty even though the column was populated.
		Accelerator:     row.Accelerator,
		AcceleratorType: row.AcceleratorType,
	}
}

// restrictedServiceIDs returns which of the given services carry an access
// policy. One query, so a page of 20 services does not cost 20 round trips.
func (s *Service) restrictedServiceIDs(ctx context.Context, repo *InferenceServiceRepository, serviceIDs []string) map[string]bool {
	out := make(map[string]bool, len(serviceIDs))
	if len(serviceIDs) == 0 {
		return out
	}
	restricted, err := repo.RestrictedServiceIDs(ctx, serviceIDs)
	if err != nil {
		// A policy read failure must not hide the list; the console shows
		// the row as unrestricted, which is the safe default for display
		// only (the enforcement path reads the policy directly).
		return out
	}
	for _, id := range restricted {
		out[id] = true
	}
	return out
}

// autoscalingStateProto maps the stored autoscaling state string to the
// proto enum.
func autoscalingStateProto(state string) inferv1.AutoscalingState {
	switch state {
	case "disabled":
		return inferv1.AutoscalingState_AUTOSCALING_STATE_DISABLED
	case "steady":
		return inferv1.AutoscalingState_AUTOSCALING_STATE_STEADY
	case "scaling-up":
		return inferv1.AutoscalingState_AUTOSCALING_STATE_SCALING_UP
	case "scaling-down":
		return inferv1.AutoscalingState_AUTOSCALING_STATE_SCALING_DOWN
	case "scaled-to-zero":
		return inferv1.AutoscalingState_AUTOSCALING_STATE_SCALED_TO_ZERO
	case "cold-starting":
		return inferv1.AutoscalingState_AUTOSCALING_STATE_COLD_STARTING
	case "error":
		return inferv1.AutoscalingState_AUTOSCALING_STATE_ERROR
	default:
		return inferv1.AutoscalingState_AUTOSCALING_STATE_UNSPECIFIED
	}
}

// clampToInt32 bounds v to the int32 range proto fields accept.
func clampToInt32(v int) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < 0 {
		return 0
	}
	return int32(v)
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

// okResponse is the shared success response envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "ok"}
}

// ClusterPlacementBackfiller assigns a default cluster to services that
// predate multi-cluster placement (feature #40, AD8). It is a one-time
// startup migration so existing single-cluster deployments keep their
// placement view.
type ClusterPlacementBackfiller struct {
	svc *Service
}

// NewClusterPlacementBackfiller builds a ClusterPlacementBackfiller.
func NewClusterPlacementBackfiller(svc *Service) *ClusterPlacementBackfiller {
	return &ClusterPlacementBackfiller{svc: svc}
}

// Run implements server.Runner: it resolves the default cluster and
// backfills services with an empty cluster_id once, then returns.
func (b *ClusterPlacementBackfiller) Run(ctx context.Context) error {
	if b.svc == nil || b.svc.clusterResolver == nil {
		return nil
	}
	clusterID, err := b.svc.clusterResolver.ResolveCluster(ctx, "")
	if err != nil {
		logger.S().Warnw("infer: cluster placement backfill skipped, resolve failed", "err", err)
		return nil
	}
	if clusterID == "" {
		return nil
	}
	repo, err := b.svc.repository()
	if err != nil {
		logger.S().Warnw("infer: cluster placement backfill skipped, repository unavailable", "err", err)
		return nil
	}
	n, err := repo.BackfillClusterPlacement(ctx, clusterID)
	if err != nil {
		logger.S().Warnw("infer: cluster placement backfill failed", "err", err)
		return nil
	}
	if n > 0 {
		logger.S().Infow("infer: backfilled cluster placement", "cluster_id", clusterID, "services", n)
	}
	return nil
}
