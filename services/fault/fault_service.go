package fault

import (
	"context"
	"errors"
	"strings"
	"time"

	"google.golang.org/grpc"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	faultv1 "github.com/go-taas/go-taas/proto/taas/fault/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/infer"
)

// ServiceName is the unique name of this service.
const ServiceName = "fault"

// Pagination bounds (architecture Section 4.2).
const (
	listDefaultLimit = 20
	listMaxLimit     = 100
)

// roleAdmin is the minimum role for the admin fault RPCs (feature #53,
// AD15).
const roleAdmin = "admin"

// SessionOrgResolver resolves the session's active organization
// (feature #53, AD15). Implemented by the auth module; nil until wired.
type SessionOrgResolver interface {
	SessionActiveOrg(ctx context.Context) (string, error)
}

// SessionResolver resolves the authenticated caller's user id (feature
// #53, AD16). Implemented by the auth module; nil until wired.
type SessionResolver interface {
	SessionUserID(ctx context.Context) (string, error)
}

// RoleGuard enforces the admin role on the admin fault RPCs (feature
// #53, AD15). Implemented by the tenancy module; nil until wired.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// AuditRecorder is the best-effort, non-fatal audit recorder seam
// (feature #15, AD3). It is implemented by the audit module and injected
// at wiring time.
type AuditRecorder interface {
	Record(ctx context.Context, ev *audit.AuditEvent)
}

// DeploymentTrail is the infer module's deployment-history seam (feature
// #53, AD16). It records recovery actions on service targets.
type DeploymentTrail interface {
	RecordDeploymentEvent(ctx context.Context, serviceID, eventType, actor, detail string) error
}

// Service implements the fault-diagnosis gRPC service (feature #53).
type Service struct {
	faultv1.UnimplementedFaultServiceServer

	components server.Components

	// repo is the injection point used by tests and FVT; production
	// resolves it lazily from the shared components.
	repo *Repository

	// classifier is the deterministic root-cause classifier (AD12).
	classifier *Classifier

	// recovery executes recovery actions (AD14).
	recovery *RecoveryRunner

	// roleGuard enforces the admin gate (AD15). Nil until wired.
	roleGuard RoleGuard

	// sessionResolver resolves the authenticated caller for audit
	// (AD16). Nil until wired.
	sessionResolver SessionResolver

	// sessionOrgResolver resolves the session's active org (AD15). Nil
	// until wired.
	sessionOrgResolver SessionOrgResolver

	// auditRecorder is the best-effort audit recorder (AD16). Nil until
	// wired.
	auditRecorder AuditRecorder

	// deploymentTrail is the infer deployment-history seam (AD16). Nil
	// until wired: recovery actions are audit-only.
	deploymentTrail DeploymentTrail
}

// New constructs the fault service. The repository is wired lazily on
// first use from the shared components.
func New(components server.Components) *Service {
	return &Service{components: components, classifier: NewClassifier()}
}

// NewForFVT constructs a fault service bound to a caller-provided GORM
// database. It exists so full-verification tests can wire the real
// service stack against a disposable database.
func NewForFVT(db *gorm.DB) *Service {
	return &Service{repo: NewRepository(db), classifier: NewClassifier()}
}

// SetRoleGuard injects the admin role guard (AD15).
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// SetSessionResolver injects the caller-identity resolver (AD16).
func (s *Service) SetSessionResolver(r SessionResolver) { s.sessionResolver = r }

// SetSessionOrgResolver injects the session active-org resolver (AD15).
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetAuditRecorder injects the best-effort audit recorder (AD16).
func (s *Service) SetAuditRecorder(r AuditRecorder) { s.auditRecorder = r }

// SetDeploymentTrail injects the infer deployment-history seam (AD16).
func (s *Service) SetDeploymentTrail(t DeploymentTrail) { s.deploymentTrail = t }

// SetRecovery injects the recovery runner (AD14). Used by FVT and tests.
func (s *Service) SetRecovery(r *RecoveryRunner) { s.recovery = r }

// recordAudit writes one audit event best-effort (AD16).
func (s *Service) recordAudit(ctx context.Context, ev *audit.AuditEvent) {
	if s.auditRecorder == nil {
		return
	}
	s.auditRecorder.Record(ctx, ev)
}

// MigrateSchemaForFVT applies the fault schema onto a caller-provided
// database for full-verification tests.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&Incident{}, &IncidentStep{}, &FaultPolicy{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	faultv1.RegisterFaultServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return faultv1.RegisterFaultServiceHandler
}

// Migrate implements server.Migrator: it creates/updates the fault
// tables via GORM AutoMigrate.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&Incident{}, &IncidentStep{}, &FaultPolicy{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "fault: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "fault: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the fault repository.
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

// requireAdminRole enforces the admin role on the admin fault RPCs
// (feature #53, AD15). The fault RPCs are platform-scoped, so when the
// session carries no active org there is nothing to resolve a role in
// and the check is skipped (the admin realm guard is the boundary).
func (s *Service) requireAdminRole(ctx context.Context) error {
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

// resolveSessionActor returns the authenticated caller's user id, or
// ("", false, nil) when there is no valid session.
func (s *Service) resolveSessionActor(ctx context.Context) (string, bool, error) {
	if s.sessionResolver == nil {
		return "", false, nil
	}
	userID, err := s.sessionResolver.SessionUserID(ctx)
	if err != nil {
		if apierrors.CodeOf(err) == apierrors.CodeSessionInvalid {
			return "", false, nil
		}
		return "", false, err
	}
	return userID, true, nil
}

// actor returns the acting user id for audit, or "system" when unknown.
func (s *Service) actor(ctx context.Context) string {
	userID, ok, _ := s.resolveSessionActor(ctx)
	if ok {
		return userID
	}
	return "system"
}

// ListIncidents returns the incident list with filters and pagination
// (AC1).
func (s *Service) ListIncidents(ctx context.Context, req *faultv1.ListIncidentsRequest) (*faultv1.ListIncidentsResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	if req.GetStatus() != "" && !ValidStatus(req.GetStatus()) {
		return nil, apierrors.Newf(apierrors.CodeFaultPolicyInvalid, "invalid fault policy: unknown status %q", req.GetStatus())
	}
	if req.GetSeverity() != "" && !ValidSeverity(req.GetSeverity()) {
		return nil, apierrors.Newf(apierrors.CodeFaultPolicyInvalid, "invalid fault policy: unknown severity %q", req.GetSeverity())
	}
	if req.GetRootCause() != "" && !ValidRootCause(req.GetRootCause()) {
		return nil, apierrors.Newf(apierrors.CodeFaultPolicyInvalid, "invalid fault policy: unknown root cause %q", req.GetRootCause())
	}
	if req.GetTargetType() != "" && !ValidTargetType(req.GetTargetType()) {
		return nil, apierrors.Newf(apierrors.CodeFaultPolicyInvalid, "invalid fault policy: unknown target type %q", req.GetTargetType())
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePage(req.GetPage())
	rows, total, err := repo.ListIncidents(ctx, IncidentListFilter{
		Status:     req.GetStatus(),
		Severity:   req.GetSeverity(),
		RootCause:  req.GetRootCause(),
		TargetType: req.GetTargetType(),
		Search:     strings.TrimSpace(req.GetSearch()),
		Offset:     offset,
		Limit:      limit,
	})
	if err != nil {
		return nil, err
	}
	incidents := make([]*faultv1.Incident, 0, len(rows))
	for _, row := range rows {
		incidents = append(incidents, incidentToProto(row))
	}
	return &faultv1.ListIncidentsResponse{
		Response: okResponse(),
		Incidents: incidents,
		//nolint:gosec // G115: limit is capped by normalizePage.
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: int32(limit)},
	}, nil
}

// GetIncident returns one incident's full detail (AC2).
func (s *Service) GetIncident(ctx context.Context, req *faultv1.GetIncidentRequest) (*faultv1.GetIncidentResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetIncident(ctx, req.GetIncidentId())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeFaultNotFound)
	}
	if err != nil {
		return nil, err
	}
	steps, err := repo.ListSteps(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	actions, err := repo.ListActions(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	resp := &faultv1.GetIncidentResponse{
		Response: okResponse(),
		Incident: incidentToProto(row),
	}
	for _, st := range steps {
		resp.Steps = append(resp.Steps, &faultv1.IncidentStep{
			StepType:  st.StepType,
			Actor:     st.Actor,
			Detail:    st.Detail,
			CreatedAt: st.CreatedAt.Unix(),
		})
	}
	for _, a := range actions {
		resp.Actions = append(resp.Actions, &faultv1.RecoveryAction{
			Action:    a.Action,
			Status:    a.Status,
			Actor:     a.Actor,
			CreatedAt: a.CreatedAt.Unix(),
		})
	}
	return resp, nil
}

// UpdateAutoRecoveryPolicy sets the per-category auto-recovery toggles
// (AC7).
func (s *Service) UpdateAutoRecoveryPolicy(ctx context.Context, req *faultv1.UpdateAutoRecoveryPolicyRequest) (*faultv1.UpdateAutoRecoveryPolicyResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	for k := range req.GetAutoRestart() {
		if !ValidRootCause(k) {
			return nil, apierrors.Newf(apierrors.CodeFaultPolicyInvalid, "invalid fault policy: unknown category %q", k)
		}
	}
	for k := range req.GetAutoReschedule() {
		if !ValidRootCause(k) {
			return nil, apierrors.Newf(apierrors.CodeFaultPolicyInvalid, "invalid fault policy: unknown category %q", k)
		}
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	policy, err := repo.UpdatePolicy(ctx, req.GetAutoRestart(), req.GetAutoReschedule(), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: "",
		ActorUserID:    s.actor(ctx),
		ActorType:      "user",
		Action:         "fault.policy.updated",
		ResourceType:   "fault_policy",
		ResourceID:     "1",
		Result:         "success",
	})
	return &faultv1.UpdateAutoRecoveryPolicyResponse{
		Response: okResponse(),
		Policy:   policyToProto(policy),
	}, nil
}

// GetAutoRecoveryPolicy returns the current policy (AC7).
func (s *Service) GetAutoRecoveryPolicy(ctx context.Context, _ *faultv1.GetAutoRecoveryPolicyRequest) (*faultv1.GetAutoRecoveryPolicyResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	policy, err := repo.GetPolicy(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &faultv1.GetAutoRecoveryPolicyResponse{
		Response: okResponse(),
		Policy:   policyToProto(policy),
	}, nil
}

// TriggerRecoveryAction manually triggers a restart/reschedule/drain
// (AC6).
func (s *Service) TriggerRecoveryAction(ctx context.Context, req *faultv1.TriggerRecoveryActionRequest) (*faultv1.TriggerRecoveryActionResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	action := strings.TrimSpace(req.GetAction())
	if !ValidAction(action) {
		return nil, apierrors.Newf(apierrors.CodeFaultRecoveryInvalid, "invalid recovery action: unknown action %q", action)
	}
	if action == ActionDrain && !req.GetConfirm() {
		return nil, apierrors.Newf(apierrors.CodeFaultRecoveryInvalid, "invalid recovery action: drain requires confirmation")
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetIncident(ctx, req.GetIncidentId())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeFaultNotFound)
	}
	if err != nil {
		return nil, err
	}
	actor := s.actor(ctx)
	// Record the manual action step.
	step := &IncidentStep{
		ID:        newUUID(),
		StepType:  StepManualAction,
		Actor:     actor,
		Detail:    action,
		CreatedAt: time.Now().UTC(),
	}
	if err := repo.AddStep(ctx, row.ID, step); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: "",
		ActorUserID:    actor,
		ActorType:      "user",
		Action:         "fault.recovery.triggered",
		ResourceType:   "incident",
		ResourceID:     row.ID,
		Result:         "success",
	})
	return &faultv1.TriggerRecoveryActionResponse{
		Response: okResponse(),
		Action: &faultv1.RecoveryAction{
			Action:    action,
			Status:    ActionStatusSucceeded,
			Actor:     actor,
			CreatedAt: step.CreatedAt.Unix(),
		},
	}, nil
}

// OverrideRecoveryAction cancels an in-flight recovery action (AC6).
func (s *Service) OverrideRecoveryAction(ctx context.Context, req *faultv1.OverrideRecoveryActionRequest) (*faultv1.OverrideRecoveryActionResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetIncident(ctx, req.GetIncidentId())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeFaultNotFound)
	}
	if err != nil {
		return nil, err
	}
	// Find an in-flight action (a recovery_started step without a
	// terminal step after it).
	steps, err := repo.ListSteps(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	inFlight := false
	for _, st := range steps {
		if st.StepType == StepRecoveryStarted {
			inFlight = true
		}
		if st.StepType == StepRecoverySucceeded || st.StepType == StepRecoveryFailed || st.StepType == StepOverridden {
			inFlight = false
		}
	}
	if !inFlight {
		return nil, apierrors.New(apierrors.CodeFaultNoActionInFlight)
	}
	actor := s.actor(ctx)
	step := &IncidentStep{
		ID:        newUUID(),
		StepType:  StepOverridden,
		Actor:     actor,
		Detail:    "recovery action overridden",
		CreatedAt: time.Now().UTC(),
	}
	if err := repo.AddStep(ctx, row.ID, step); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: "",
		ActorUserID:    actor,
		ActorType:      "user",
		Action:         "fault.recovery.overridden",
		ResourceType:   "incident",
		ResourceID:     row.ID,
		Result:         "success",
	})
	return &faultv1.OverrideRecoveryActionResponse{
		Response: okResponse(),
		Action: &faultv1.RecoveryAction{
			Action:    "restart",
			Status:    ActionStatusOverridden,
			Actor:     actor,
			CreatedAt: step.CreatedAt.Unix(),
		},
	}, nil
}

// ActiveIncidentForService implements the infer module's IncidentReader
// seam (feature #53, AD10). It returns the active incident for a
// service, or (zero, false, nil) when healthy.
func (s *Service) ActiveIncidentForService(ctx context.Context, serviceID string) (infer.ServiceIncident, bool, error) {
	repo, err := s.repository()
	if err != nil {
		return infer.ServiceIncident{}, false, err
	}
	row, err := repo.FindActiveIncidentByTarget(ctx, TargetService, serviceID)
	if err != nil {
		return infer.ServiceIncident{}, false, err
	}
	if row == nil {
		return infer.ServiceIncident{}, false, nil
	}
	return infer.ServiceIncident{
		IncidentID: row.ID,
		Severity:   row.Severity,
		Status:     row.Status,
		RootCause:  row.RootCause,
		Message:    incidentMessage(row),
	}, true, nil
}

// incidentMessage returns the plain-language banner copy for an
// incident (feature #53, AD10).
func incidentMessage(row *Incident) string {
	switch row.RootCause {
	case CauseServiceCrashLoop:
		return "This service is being recovered — a failure was detected and the platform is restarting it."
	case CauseOOM:
		return "This service ran out of memory and is being restarted."
	case CauseImagePullFailure:
		return "This service could not pull its image and is being restarted."
	case CauseNodeNotReady:
		return "This service's node is not ready and it is being rescheduled."
	case CauseAcceleratorDevicePluginFailure:
		return "This service's accelerator is unhealthy and it is being rescheduled."
	default:
		return "A failure was detected and the platform is recovering this service."
	}
}

// incidentToProto maps an incident row to its wire projection.
func incidentToProto(row *Incident) *faultv1.Incident {
	out := &faultv1.Incident{
		IncidentId:    row.ID,
		Severity:      row.Severity,
		Status:        row.Status,
		RootCause:     row.RootCause,
		TargetType:    row.TargetType,
		TargetName:    row.TargetName,
		StartedAt:     row.StartedAt.Unix(),
		AutoRecovered: row.AutoRecovered,
	}
	if row.ResolvedAt != nil {
		out.ResolvedAt = row.ResolvedAt.Unix()
	}
	return out
}

// policyToProto maps a policy row to its wire projection.
func policyToProto(row *FaultPolicy) *faultv1.AutoRecoveryPolicy {
	out := &faultv1.AutoRecoveryPolicy{
		AutoRestart:       map[string]bool{},
		AutoReschedule:    map[string]bool{},
		DrainAlwaysConfirm: row.DrainAlwaysConfirm,
	}
	_ = jsonUnmarshalMap(row.AutoRestart, out.AutoRestart)
	_ = jsonUnmarshalMap(row.AutoReschedule, out.AutoReschedule)
	return out
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
		limit = listDefaultLimit
	}
	if limit > listMaxLimit {
		limit = listMaxLimit
	}
	return offset, limit
}

// okResponse builds the success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}