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

// SetAuditRecorder injects the best-effort audit recorder (feature #15,
// AD3). Production wires the audit module; unit tests may inject a fake.
func (s *Service) SetAuditRecorder(r AuditRecorder) { s.auditRecorder = r }

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

// requireAdminRole enforces the minimum org role on the admin infer RPCs
// (features #32/#33/#34/#35). The RoleGuard resolves the caller from a
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
	if err := db.AutoMigrate(&InferenceService{}, &AutoscalingPolicy{}, &LoadTest{}, &DeploymentEvent{}, &RoutingPolicy{}, &RoutingPolicyRevision{}, &RoutingPolicyOutbox{}); err != nil {
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
	if err := db.WithContext(ctx).AutoMigrate(&InferenceService{}, &AutoscalingPolicy{}, &LoadTest{}, &DeploymentEvent{}, &RoutingPolicy{}, &RoutingPolicyRevision{}, &RoutingPolicyOutbox{}); err != nil {
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

	// Validation order per architecture 4.3: name, replicas,
	// accelerator, model, version, image, compatibility. Nothing is
	// published unless every check passes (AC5).
	name := strings.TrimSpace(req.GetName())
	if !serviceNamePattern.MatchString(name) {
		return nil, apierrors.New(apierrors.CodeInferServiceStateInvalid)
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
	if _, err := modelRepo.GetModel(ctx, req.GetModelId()); err != nil {
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
	compatStatus := ""
	if s.compatibilityChecker != nil {
		compatStatus, err = s.compatibilityChecker(ctx, req.GetModelId(), img.Engine, strings.TrimSpace(req.GetAcceleratorType()))
		if err != nil {
			return nil, err
		}
		if compatStatus == image.StatusUnsupported {
			return nil, apierrors.Newf(apierrors.CodeCompatibilityUnsupported,
				"model %s is not supported on engine %s with card type %s; see the compatibility matrix",
				req.GetModelId(), img.Engine, strings.TrimSpace(req.GetAcceleratorType()))
		}
	}
	// Feature #16: validate the explicit autoscaling policy synchronously
	// (AD10) before any write or publish.
	if err := validateAutoscalingPolicy(req.GetAutoscaling()); err != nil {
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
	}
	if err := repo.Create(ctx, svc); err != nil {
		return nil, err
	}

	evt := buildChangeEvent(EventTypeUpsert, svc, modelVersion.WeightPath, img.Reference(), img.Engine)
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
	rows, total, err := repo.ListByOrganization(ctx, orgID, offset, limit, false)
	if err != nil {
		return nil, err
	}

	// Feature #16: resolve the global default once so the list summary
	// can merge it for services that inherit ({}).
	globalDefault, err := s.globalAutoscalingDefault(ctx)
	if err != nil {
		return nil, err
	}

	services := make([]*inferv1.InferenceServiceSummary, 0, len(rows))
	for _, row := range rows {
		effective := effectivePolicyForRow(row, globalDefault)
		services = append(services, summarizeService(row, effective))
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

	row, err := repo.FindByIDAndOrganization(ctx, orgID, req.GetServiceId())
	if err != nil {
		return nil, err
	}

	// Feature #16: resolve the effective policy and the autoscaling
	// status block.
	effective, err := repo.ResolveEffectivePolicy(ctx, row)
	if err != nil {
		return nil, err
	}

	// Endpoints are only exposed while running.
	var endpoints []string
	if row.State == StateRunning {
		endpoints = row.Endpoints
	}
	return &inferv1.GetInferenceServiceResponse{
		Response:          okResponse(),
		Service:           summarizeService(row, effective),
		Endpoints:         endpoints,
		Autoscaling:       policyToProto(effective),
		AutoscalingStatus: autoscalingStatusFromRow(row),
	}, nil
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

	row, err := repo.FindByIDAndOrganization(ctx, orgID, req.GetServiceId())
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
	evt := buildChangeEvent(EventTypeUpsert, &scaled, modelVersion.WeightPath, img.Reference(), img.Engine)
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
	scaledRow, _ := repo.FindByIDAndOrganization(ctx, orgID, req.GetServiceId())
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

	row, err := repo.FindByIDAndOrganization(ctx, orgID, req.GetServiceId())
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
	if err := publishChange(ctx, client, buildChangeEvent(EventTypeDelete, row, "", "", "")); err != nil {
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

	row, err := repo.FindByIDAndOrganization(ctx, orgID, req.GetServiceId())
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
	evt := buildChangeEvent(EventTypeUpdateVersion, &updated, target.WeightPath, img.Reference(), img.Engine)
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
	if _, err := repo.FindByIDAndOrganization(ctx, orgID, req.GetServiceId()); err != nil {
		return nil, err
	}
	if s.logFetcher == nil {
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	pods, err := s.logFetcher.ListServiceLogPods(ctx, req.GetServiceId())
	if err != nil {
		return nil, err
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
	if _, err := repo.FindByIDAndOrganization(ctx, orgID, req.GetServiceId()); err != nil {
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
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	window, err := s.logFetcher.GetServiceLogs(
		ctx, req.GetServiceId(), strings.TrimSpace(req.GetPod()),
		strings.TrimSpace(req.GetContainer()), tail, strings.TrimSpace(req.GetSince()),
		strings.TrimSpace(req.GetNextOffset()),
	)
	if err != nil {
		return nil, err
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
		if _, err := repo.FindByIDAndOrganization(ctx, orgID, serviceID); err != nil {
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

	row, err := repo.FindByIDAndOrganization(ctx, orgID, req.GetServiceId())
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
	// The target version, when present, must be a registered version of
	// the service's model.
	if target.ModelVersion != "" {
		modelRepo, err := s.modelRepository()
		if err != nil {
			return nil, err
		}
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
	evtChange := buildChangeEvent(EventTypeRollback, &updated, target.WeightPath, img.Reference(), img.Engine)
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
	}
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
