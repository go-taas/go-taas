package runtime

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"google.golang.org/grpc"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	runtimev1 "github.com/go-taas/go-taas/proto/taas/runtime/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"
)

// ServiceName is the unique name of this service.
const ServiceName = "runtime"

const roleAdmin = "admin"

// Engine values (AD8).
const (
	EngineVLLM   = "vllm"
	EngineSGLang = "sglang"
)

// Validation bounds (AD8).
const (
	maxModelNameLen = 128
	maxArtifactLen  = 512
	maxReplicas     = 32
)

// modelNamePattern is the accepted model-name shape (AD8).
var modelNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// podSelectorKeyPattern is the accepted pod-selector key shape (AD8).
var podSelectorKeyPattern = regexp.MustCompile(`^[a-z0-9A-Z._/-]+$`)

// SessionOrgResolver resolves the session's active organization.
type SessionOrgResolver interface {
	SessionActiveOrg(ctx context.Context) (string, error)
}

// SessionUserResolver resolves the authenticated caller's user id.
type SessionUserResolver interface {
	SessionUserID(ctx context.Context) (string, error)
}

// RoleGuard enforces the minimum org role on the runtime RPCs.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// PlatformAdminGuard answers whether the caller is a platform
// administrator. The runtime is a platform-owned asset, so the admin runtime
// RPCs answer to the platform, never to an organization administrator.
// It is implemented by tenancy.Service.
type PlatformAdminGuard interface {
	RequirePlatformAdmin(ctx context.Context) error
}

// AuditRecorder records runtime mutations best-effort.
type AuditRecorder interface {
	Record(ctx context.Context, ev *audit.AuditEvent)
}

// Service implements the runtime gRPC service (feature #47). It is
// admin-surface, admin-role, platform-scoped (AD1, AD10).
type Service struct {
	runtimev1.UnimplementedRuntimeServiceServer

	components server.Components

	// repo is the singleton serving-scheme repository.
	repo *RuntimeRepository
	// cache is the in-memory runtime projection cache (AD11).
	cache *ProjectionCache
	// broadcaster re-publishes the scheme to the controller on change.
	broadcaster *SchemeBroadcaster
	// publisher publishes model desired-state changes to the controller.
	publisher mq.Client

	sessionOrgResolver  SessionOrgResolver
	sessionUserResolver SessionUserResolver
	roleGuard           RoleGuard
	// platformAdminGuard gates the runtime RPCs on the platform
	// administrator role: the serving runtime belongs to the platform.
	platformAdminGuard PlatformAdminGuard
	auditRecorder       AuditRecorder

	// userLabelResolver resolves the acting admin's user id to a display
	// label, so the settings page shows a person rather than a UUID. Nil
	// until wired: the raw id is reported.
	userLabelResolver UserLabelResolver

	// dataPlaneRouter applies the saved inference hostname to the
	// data-plane HTTPRoute, so the setting actually publishes the gateway
	// instead of only changing what the console advertises. Nil until
	// wired: the setting is stored without touching the cluster.
	dataPlaneRouter DataPlaneRouter
}

// New constructs the runtime service over a fresh projection cache.
func New(components server.Components) *Service {
	return &Service{components: components, cache: NewProjectionCache()}
}

// NewWithCache constructs the runtime service over the shared server
// components and a caller-provided cache. Unlike the accelerator
// service (a pure in-memory projection whose Migrate is a no-op), the
// runtime service owns the runtime_settings table, so it must carry the
// components to resolve the database handle in Migrate/repository.
func NewWithCache(components server.Components, cache *ProjectionCache) *Service {
	return &Service{components: components, cache: cache}
}

// NewForFVT constructs a runtime service bound to a caller-provided GORM
// database and projection cache.
func NewForFVT(db *gorm.DB, cache *ProjectionCache) *Service {
	if cache == nil {
		cache = NewProjectionCache()
	}
	return &Service{repo: NewRuntimeRepository(db), cache: cache}
}

// MigrateSchemaForFVT applies the runtime schema onto a caller-provided
// database.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&RuntimeSetting{}, &PlatformSetting{})
}

// SetAuditRecorder injects the best-effort audit recorder (AD7).
func (s *Service) SetAuditRecorder(r AuditRecorder) { s.auditRecorder = r }

// SetSchemeBroadcaster injects the scheme broadcaster.
func (s *Service) SetSchemeBroadcaster(b *SchemeBroadcaster) { s.broadcaster = b }

// SetPublisher injects the model-change publisher.
func (s *Service) SetPublisher(c mq.Client) { s.publisher = c }

// SetSessionOrgResolver injects the session-organization resolver.
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver.
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard.
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// SetPlatformAdminGuard injects the platform-administrator gate. When it
// is wired (production and FVT) the admin RPCs answer to the platform
// alone; a unit test that leaves it nil keeps the org-role fallback below
// so the org guard can be exercised on its own.
func (s *Service) SetPlatformAdminGuard(g PlatformAdminGuard) { s.platformAdminGuard = g }

// DataPlaneRouter owns the data-plane HTTPRoute: it makes the route serve a
// hostname and reports what the route currently serves. It is implemented
// in pkg/k8s and injected at wiring time.
type DataPlaneRouter interface {
	// Ensure creates or updates the route so it serves exactly hostname.
	// cfg carries the install-time parts (parent Gateway, backend).
	Ensure(ctx context.Context, hostname string, cfg config.RuntimeDataPlaneConfig) error
	// ServedHostnames reports the hostnames the route serves.
	ServedHostnames(ctx context.Context, routeNamespace string) ([]string, error)
}

// SetDataPlaneRouter injects the data-plane route reconciler.
func (s *Service) SetDataPlaneRouter(r DataPlaneRouter) { s.dataPlaneRouter = r }

// UserLabelResolver resolves user ids to display labels (auth's
// ResolveUserLabels). Rosters and audit surfaces show people, not UUIDs.
type UserLabelResolver interface {
	ResolveUserLabels(ctx context.Context, userIDs []string) (map[string]string, error)
}

// SetUserLabelResolver injects the display-name resolver.
func (s *Service) SetUserLabelResolver(r UserLabelResolver) { s.userLabelResolver = r }

// Cache returns the shared projection cache (the wiring passes the same
// instance to the snapshot consumer).
func (s *Service) Cache() *ProjectionCache { return s.cache }

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	runtimev1.RegisterRuntimeServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return runtimev1.RegisterRuntimeServiceHandler
}

// Migrate implements server.Migrator: it creates the singleton
// runtime_settings table (AD6).
func (s *Service) Migrate(ctx context.Context) error {
	if s.repo != nil {
		return nil
	}
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&RuntimeSetting{}, &PlatformSetting{})
}

// gormDB resolves the *gorm.DB from the shared components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "runtime: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "runtime: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the runtime repository.
func (s *Service) repository() (*RuntimeRepository, error) {
	if s.repo != nil {
		return s.repo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.repo = NewRuntimeRepository(db)
	return s.repo, nil
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

// requireAdminRole enforces the admin role on the runtime RPCs (AD1,
// AD10). The RoleGuard resolves the caller from a session; in the
// transitional (session-less) path there is no session user to check, so
// the check is skipped. The runtime is platform-scoped: when the session
// carries no active org there is nothing to resolve a role in, so the
// check is skipped (the admin realm guard is the access boundary).
func (s *Service) requireAdminRole(ctx context.Context) error {
	if s.platformAdminGuard != nil {
		return s.platformAdminGuard.RequirePlatformAdmin(ctx)
	}
	if s.roleGuard == nil {
		return nil
	}
	orgID := ""
	if s.sessionOrgResolver != nil {
		org, err := s.sessionOrgResolver.SessionActiveOrg(ctx)
		if err != nil && apierrors.CodeOf(err) != apierrors.CodeSessionInvalid {
			return err
		}
		orgID = org
	}
	if orgID == "" {
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

// actor returns the acting user id for audit, or "system" when unknown.
func (s *Service) actor(ctx context.Context) string {
	userID, ok, _ := s.resolveSessionActor(ctx)
	if ok {
		return userID
	}
	return "system"
}

// GetRuntimeStatus returns the runtime overview (AC1).
func (s *Service) GetRuntimeStatus(ctx context.Context, _ *runtimev1.GetRuntimeStatusRequest) (*runtimev1.GetRuntimeStatusResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	setting, err := repo.GetScheme(ctx)
	if err != nil {
		return nil, err
	}
	snapshot := s.cache.Snapshot()

	status := &runtimev1.RuntimeStatus{Scheme: setting.Scheme}
	if snapshot == nil {
		// No snapshot has arrived yet: serve an unknown view, never an
		// error (AC11, AD4).
		status.Availability = AvailabilityUnknown
		status.RuntimeName = setting.Scheme
	} else {
		status.Availability = snapshot.Availability
		status.RuntimeName = snapshot.RuntimeName
		if status.RuntimeName == "" {
			status.RuntimeName = setting.Scheme
		}
		status.RuntimeVersion = snapshot.RuntimeVersion
		status.ClusterName = snapshot.ClusterName
		status.Reason = snapshot.Reason
		if !snapshot.ReportedAt.IsZero() {
			status.LastCheckedAt = snapshot.ReportedAt.Unix()
		}
		for i := range snapshot.Components {
			status.Components = append(status.Components, componentToProto(&snapshot.Components[i]))
		}
	}
	return &runtimev1.GetRuntimeStatusResponse{Response: okResponse(), Runtime: status}, nil
}

// ListRuntimeComponents returns the runtime components (AC2).
func (s *Service) ListRuntimeComponents(ctx context.Context, _ *runtimev1.ListRuntimeComponentsRequest) (*runtimev1.ListRuntimeComponentsResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	comps := s.cache.Components("", "")
	out := make([]*runtimev1.RuntimeComponent, 0, len(comps))
	for i := range comps {
		out = append(out, componentToProto(&comps[i]))
	}
	return &runtimev1.ListRuntimeComponentsResponse{Response: okResponse(), Components: out}, nil
}

// GetServingScheme returns the persisted serving scheme (AC3).
func (s *Service) GetServingScheme(ctx context.Context, _ *runtimev1.GetServingSchemeRequest) (*runtimev1.GetServingSchemeResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	setting, err := repo.GetScheme(ctx)
	if err != nil {
		return nil, err
	}
	return &runtimev1.GetServingSchemeResponse{Response: okResponse(), Scheme: schemeToProto(setting)}, nil
}

// UpdateServingScheme sets the scheme (AC3, AC11). It rejects an unknown
// value with 12902, rejects switching to aibrix while the runtime is
// unavailable with 12901, and is idempotent (no audit, no broadcast).
func (s *Service) UpdateServingScheme(ctx context.Context, req *runtimev1.UpdateServingSchemeRequest) (*runtimev1.UpdateServingSchemeResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	scheme := strings.TrimSpace(req.GetScheme())
	if !validScheme(scheme) {
		return nil, apierrors.New(apierrors.CodeRuntimeSchemeInvalid)
	}
	current, err := repo.GetScheme(ctx)
	if err != nil {
		return nil, err
	}
	if scheme == current.Scheme {
		// Idempotent no-op success (AD7).
		return &runtimev1.UpdateServingSchemeResponse{Response: okResponse(), Scheme: schemeToProto(current)}, nil
	}
	// FR2.5 / AD12: an operator cannot switch to a runtime that is not
	// observed.
	if scheme == SchemeAIBrix {
		if avail := s.cache.Availability(); avail != AvailabilityAvailable {
			return nil, apierrors.New(apierrors.CodeRuntimeUnavailable)
		}
	}
	actor := s.actor(ctx)
	updated, err := repo.UpdateScheme(ctx, scheme, actor)
	if err != nil {
		return nil, err
	}
	if s.auditRecorder != nil {
		before, _ := json.Marshal(map[string]string{"scheme": current.Scheme})
		after, _ := json.Marshal(map[string]string{"scheme": scheme})
		s.auditRecorder.Record(ctx, &audit.AuditEvent{
			ActorUserID:  actor,
			ActorType:    "user",
			Action:       "runtime.scheme_changed",
			ResourceType: "runtime",
			ResourceID:   runtimeSettingID,
			Result:       "success",
			Metadata:     string(mustJSON(map[string]string{"before": string(before), "after": string(after)})),
		})
	}
	if s.broadcaster != nil {
		s.broadcaster.Notify()
	}
	return &runtimev1.UpdateServingSchemeResponse{Response: okResponse(), Scheme: schemeToProto(updated)}, nil
}

// ListRuntimeModels returns the ModelClaim projection (AC4).
func (s *Service) ListRuntimeModels(ctx context.Context, req *runtimev1.ListRuntimeModelsRequest) (*runtimev1.ListRuntimeModelsResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	offset, limit := normalizePage(req.GetPage())
	models, total := s.cache.Models(req.GetName(), req.GetEngine(), req.GetStatus(), offset, limit)
	out := make([]*runtimev1.RegisteredModel, 0, len(models))
	for i := range models {
		out = append(out, modelToProto(&models[i]))
	}
	return &runtimev1.ListRuntimeModelsResponse{
		Response: okResponse(),
		Models:   out,
		PageMeta: &commonv1.PageMeta{
			Total:  total,
			Offset: int64(offset),
			Limit:  int32(limit), //nolint:gosec // limit is capped at 100 by normalizePage.
		},
	}, nil
}

// RegisterRuntimeModel registers a model with the runtime (AC5, AD8,
// AD12). It validates the fields, requires the aibrix scheme with an
// available runtime, rejects duplicates, and publishes the desired-state
// change the controller reconciles into a ModelClaim.
func (s *Service) RegisterRuntimeModel(ctx context.Context, req *runtimev1.RegisterRuntimeModelRequest) (*runtimev1.RegisterRuntimeModelResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetModelName())
	if !validModelName(name) {
		return nil, apierrors.New(apierrors.CodeRuntimeModelNameInvalid)
	}
	engine := strings.TrimSpace(req.GetEngine())
	if engine != EngineVLLM && engine != EngineSGLang {
		return nil, apierrors.New(apierrors.CodeRuntimeEngineUnsupported)
	}
	replicas := req.GetReplicas()
	if replicas < 0 || replicas > maxReplicas {
		return nil, apierrors.New(apierrors.CodeRuntimeReplicasInvalid)
	}
	artifact := strings.TrimSpace(req.GetArtifactUrl())
	if artifact == "" || len(artifact) > maxArtifactLen {
		return nil, apierrors.Newf(apierrors.CodeRuntimeModelNameInvalid, "artifact_url is required")
	}
	if err := validatePodSelector(req.GetPodSelector()); err != nil {
		return nil, err
	}
	if err := s.requireActiveAIBrix(ctx); err != nil {
		return nil, err
	}
	if _, found := s.cache.FindModel(name); found {
		return nil, apierrors.New(apierrors.CodeRuntimeModelExists)
	}
	change := &ModelChangeMessage{
		EventType:   ModelEventUpsert,
		ModelName:   name,
		Engine:      engine,
		Replicas:    replicas,
		ArtifactURL: artifact,
		PodSelector: req.GetPodSelector(),
		EngineArgs:  req.GetEngineArgs(),
	}
	if err := s.publishModelChange(ctx, change); err != nil {
		return nil, err
	}
	actor := s.actor(ctx)
	if s.auditRecorder != nil {
		meta, _ := json.Marshal(map[string]any{"engine": engine, "replicas": replicas})
		s.auditRecorder.Record(ctx, &audit.AuditEvent{
			ActorUserID:  actor,
			ActorType:    "user",
			Action:       "runtime.model_registered",
			ResourceType: "runtime_model",
			ResourceID:   name,
			Result:       "success",
			Metadata:     string(meta),
		})
	}
	model := &runtimev1.RegisteredModel{
		ModelName: name,
		Engine:    engine,
		Replicas:  replicas,
		Status:    "progressing",
		CreatedAt: time.Now().UTC().Unix(),
	}
	return &runtimev1.RegisterRuntimeModelResponse{Response: okResponse(), Model: model}, nil
}

// DeregisterRuntimeModel removes a model registration (AC6, AD12). It
// requires the aibrix scheme with an available runtime and a known model,
// then publishes the delete the controller reconciles.
func (s *Service) DeregisterRuntimeModel(ctx context.Context, req *runtimev1.DeregisterRuntimeModelRequest) (*runtimev1.DeregisterRuntimeModelResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetModelName())
	if name == "" {
		return nil, apierrors.New(apierrors.CodeRuntimeModelNameInvalid)
	}
	if err := s.requireActiveAIBrix(ctx); err != nil {
		return nil, err
	}
	if _, found := s.cache.FindModel(name); !found {
		return nil, apierrors.New(apierrors.CodeRuntimeModelNotFound)
	}
	if err := s.publishModelChange(ctx, &ModelChangeMessage{EventType: ModelEventDelete, ModelName: name}); err != nil {
		return nil, err
	}
	actor := s.actor(ctx)
	if s.auditRecorder != nil {
		s.auditRecorder.Record(ctx, &audit.AuditEvent{
			ActorUserID:  actor,
			ActorType:    "user",
			Action:       "runtime.model_deregistered",
			ResourceType: "runtime_model",
			ResourceID:   name,
			Result:       "success",
			Metadata:     "{}",
		})
	}
	return &runtimev1.DeregisterRuntimeModelResponse{Response: okResponse()}, nil
}

// requireActiveAIBrix enforces AD12: register/deregister require the
// aibrix scheme with an available runtime, else 12901.
func (s *Service) requireActiveAIBrix(ctx context.Context) error {
	repo, err := s.repository()
	if err != nil {
		return err
	}
	setting, err := repo.GetScheme(ctx)
	if err != nil {
		return err
	}
	if setting.Scheme != SchemeAIBrix {
		return apierrors.New(apierrors.CodeRuntimeUnavailable)
	}
	if avail := s.cache.Availability(); avail != AvailabilityAvailable {
		return apierrors.New(apierrors.CodeRuntimeUnavailable)
	}
	return nil
}

// publishModelChange publishes a model desired-state change to the
// controller.
func (s *Service) publishModelChange(ctx context.Context, change *ModelChangeMessage) error {
	if s.publisher == nil {
		return apierrors.Newf(apierrors.CodeInternal, "runtime: model-change publisher unavailable")
	}
	body, err := json.Marshal(change)
	if err != nil {
		return err
	}
	return s.publisher.Publish(ctx, mq.DefaultSubjects().RuntimeModelChanges, body, nil)
}

// validatePodSelector validates the optional pod-selector keys/values
// (AD8).
func validatePodSelector(selector map[string]string) error {
	for k, v := range selector {
		if !podSelectorKeyPattern.MatchString(k) || len(v) > 253 {
			return apierrors.Newf(apierrors.CodeRuntimeModelNameInvalid, "invalid pod_selector")
		}
	}
	return nil
}

// validModelName reports whether name matches the accepted model-name
// shape and length (AD8).
func validModelName(name string) bool {
	return name != "" && len(name) <= maxModelNameLen && modelNamePattern.MatchString(name)
}

// normalizePage applies the pagination defaults: limit 20, cap 100,
// negative offset clamped to 0.
func normalizePage(page *commonv1.PageRequest) (offset, limit int) {
	offset = int(page.GetOffset())
	limit = int(page.GetLimit())
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return offset, limit
}

// componentToProto maps a cache component to its wire shape.
func componentToProto(c *Component) *runtimev1.RuntimeComponent {
	var checked int64
	if !c.LastCheckedAt.IsZero() {
		checked = c.LastCheckedAt.Unix()
	}
	return &runtimev1.RuntimeComponent{
		ComponentId:     c.ComponentID,
		Name:            c.Name,
		Kind:            c.Kind,
		Namespace:       c.Namespace,
		Status:          c.Status,
		ReadyReplicas:   c.ReadyReplicas,
		DesiredReplicas: c.DesiredReplicas,
		LastCheckedAt:   checked,
	}
}

// modelToProto maps a cache model to its wire shape, deriving the age in
// seconds.
func modelToProto(m *RegisteredModel) *runtimev1.RegisteredModel {
	out := &runtimev1.RegisteredModel{
		ModelName:       m.ModelName,
		Engine:          m.Engine,
		Replicas:        m.Replicas,
		ReadyReplicas:   m.ReadyReplicas,
		ReadyCandidates: m.ReadyCandidates,
		Status:          m.Status,
	}
	if !m.CreatedAt.IsZero() {
		out.CreatedAt = m.CreatedAt.Unix()
		out.AgeSeconds = int64(time.Since(m.CreatedAt).Seconds())
		if out.AgeSeconds < 0 {
			out.AgeSeconds = 0
		}
	}
	for i := range m.Conditions {
		cond := m.Conditions[i]
		var transition int64
		if !cond.LastTransitionTime.IsZero() {
			transition = cond.LastTransitionTime.Unix()
		}
		out.Conditions = append(out.Conditions, &runtimev1.ModelCondition{
			Type:               cond.Type,
			Status:             cond.Status,
			Reason:             cond.Reason,
			Message:            cond.Message,
			LastTransitionTime: transition,
		})
	}
	return out
}

// schemeToProto maps a setting to its wire shape.
func schemeToProto(setting *RuntimeSetting) *runtimev1.ServingScheme {
	out := &runtimev1.ServingScheme{Scheme: setting.Scheme, UpdatedBy: setting.UpdatedBy}
	if !setting.UpdatedAt.IsZero() {
		out.UpdatedAt = setting.UpdatedAt.Unix()
	}
	return out
}

// okResponse builds the success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}

// mustJSON marshals v, returning a JSON null on the (impossible) error.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}
