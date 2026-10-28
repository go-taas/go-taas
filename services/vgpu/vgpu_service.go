package vgpu

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"google.golang.org/grpc"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	vgpuv1 "github.com/go-taas/go-taas/proto/taas/vgpu/v1"

	"github.com/go-taas/go-taas/pkg/authctx"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/infer"
)

// ServiceName is the unique name of this service.
const ServiceName = "vgpu"

// Pagination bounds (architecture Section 4.2).
const (
	listDefaultLimit = 20
	listMaxLimit     = 100
)

// roleAdmin is the minimum role for the admin vgpu RPCs (feature #52,
// AD15).
const roleAdmin = "admin"

// poolNamePattern is the DNS-safe pool name pattern (1-63 chars).
var poolNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// SessionOrgResolver resolves the session's active organization
// (feature #52, AD10). Implemented by the auth module; nil until wired.
type SessionOrgResolver interface {
	SessionActiveOrg(ctx context.Context) (string, error)
}

// SessionResolver resolves the authenticated caller's user id
// (feature #52, AD16). Implemented by the auth module; nil until wired.
type SessionResolver interface {
	SessionUserID(ctx context.Context) (string, error)
}

// RoleGuard enforces the admin role on the admin vgpu RPCs (feature
// #52, AD15). Implemented by the tenancy module; nil until wired.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// GrantChecker is the pool-grant seam (feature #52, AD10). It reports
// whether an organization may use a pool. Nil until wired: every pool
// is granted (default-allow, matching the model-authorization
// default-allow rule).
type GrantChecker interface {
	IsPoolGranted(ctx context.Context, orgID, poolID string) (bool, error)
}

// AuditRecorder is the best-effort, non-fatal audit recorder seam
// (feature #15, AD3). It is implemented by the audit module and injected
// at wiring time.
type AuditRecorder interface {
	Record(ctx context.Context, ev *audit.AuditEvent)
}

// Service implements the vGPU slicing-pool gRPC service (feature #52).
type Service struct {
	vgpuv1.UnimplementedVGPUServiceServer

	components server.Components

	// repo is the injection point used by tests and FVT; production
	// resolves it lazily from the shared components.
	repo *Repository

	// membership is the pool membership/capacity projection (AD12).
	// Nil-safe: a nil projection answers 0.
	membership *MembershipProjection

	// roleGuard enforces the admin gate (AD15). Nil until wired: unit
	// tests skip the gate.
	roleGuard RoleGuard

	// sessionResolver resolves the authenticated caller for audit
	// (AD16). Nil until wired.
	sessionResolver SessionResolver

	// sessionOrgResolver resolves the session's active org for the
	// tenant-visible pool list (AD10). Nil until wired: falls back to
	// the transitional header.
	sessionOrgResolver SessionOrgResolver

	// grantChecker is the pool-grant seam (AD10). Nil until wired:
	// every pool is granted (default-allow).
	grantChecker GrantChecker

	// auditRecorder is the best-effort audit recorder (AD16). Nil until
	// wired: no audit events are produced.
	auditRecorder AuditRecorder
}

// New constructs the vgpu service. The repository is wired lazily on
// first use from the shared components.
func New(components server.Components) *Service {
	return &Service{components: components}
}

// NewForFVT constructs a vgpu service bound to a caller-provided GORM
// database. It exists so full-verification tests can wire the real
// service stack against a disposable database.
func NewForFVT(db *gorm.DB) *Service {
	return &Service{repo: NewRepository(db), membership: NewMembershipProjection()}
}

// SetRoleGuard injects the admin role guard (AD15).
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// SetSessionResolver injects the caller-identity resolver (AD16).
func (s *Service) SetSessionResolver(r SessionResolver) { s.sessionResolver = r }

// SetSessionOrgResolver injects the session active-org resolver (AD10).
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetGrantChecker injects the pool-grant seam (AD10).
func (s *Service) SetGrantChecker(g GrantChecker) { s.grantChecker = g }

// SetAuditRecorder injects the best-effort audit recorder (AD16).
func (s *Service) SetAuditRecorder(r AuditRecorder) { s.auditRecorder = r }

// SetMembership injects the membership projection (AD12). Used by FVT
// and tests.
func (s *Service) SetMembership(p *MembershipProjection) { s.membership = p }

// recordAudit writes one audit event best-effort (AD16).
func (s *Service) recordAudit(ctx context.Context, ev *audit.AuditEvent) {
	if s.auditRecorder == nil {
		return
	}
	s.auditRecorder.Record(ctx, ev)
}

// MigrateSchemaForFVT applies the vgpu schema onto a caller-provided
// database for full-verification tests.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&VGPUPool{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	vgpuv1.RegisterVGPUServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return vgpuv1.RegisterVGPUServiceHandler
}

// Migrate implements server.Migrator: it creates/updates the vgpu_pools
// table via GORM AutoMigrate.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&VGPUPool{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "vgpu: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "vgpu: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the vgpu repository.
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

// requireAdminRole enforces the admin role on the admin vgpu RPCs
// (feature #52, AD15). The vgpu RPCs are platform-scoped, so when the
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

// ListVGPUPools returns the pool list with search, policy filter and
// pagination (AC1).
func (s *Service) ListVGPUPools(ctx context.Context, req *vgpuv1.ListVGPUPoolsRequest) (*vgpuv1.ListVGPUPoolsResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	policy := strings.TrimSpace(req.GetPlacementPolicy())
	if policy != "" && !isValidPolicy(policy) {
		return nil, apierrors.New(apierrors.CodeVGPUPoolInvalid)
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePage(req.GetPage())
	rows, total, err := repo.ListVGPUPools(ctx, PoolListFilter{
		Name:            strings.TrimSpace(req.GetName()),
		PlacementPolicy: policy,
		Offset:          offset,
		Limit:           limit,
	})
	if err != nil {
		return nil, err
	}
	pools := make([]*vgpuv1.VGPUPool, 0, len(rows))
	for _, row := range rows {
		pools = append(pools, s.poolProjection(ctx, row))
	}
	return &vgpuv1.ListVGPUPoolsResponse{
		Response: okResponse(),
		Pools:    pools,
		//nolint:gosec // G115: limit is capped by normalizePage.
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: int32(limit)},
	}, nil
}

// GetVGPUPool returns one pool's full projection (AC3, AC4).
func (s *Service) GetVGPUPool(ctx context.Context, req *vgpuv1.GetVGPUPoolRequest) (*vgpuv1.GetVGPUPoolResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetVGPUPool(ctx, req.GetPoolId())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeVGPUPoolNotFound)
	}
	if err != nil {
		return nil, err
	}
	resp := &vgpuv1.GetVGPUPoolResponse{
		Response: okResponse(),
		Pool:     s.poolProjection(ctx, row),
	}
	if s.membership != nil {
		for _, n := range s.membership.Nodes(row.ID, row.LabelKey, row.LabelValue) {
			resp.Nodes = append(resp.Nodes, nodeToProto(n))
		}
	}
	services, err := repo.ListSliceServices(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	for _, svc := range services {
		resp.Services = append(resp.Services, &vgpuv1.SliceService{
			ServiceId:    svc.ServiceID,
			Name:         svc.Name,
			TemplateName: svc.TemplateName,
			NodeId:       svc.NodeID,
			GpuIndex:     svc.GPUIndex,
			MemoryGib:    svc.MemoryGiB,
			ComputePct:   svc.ComputePct,
			State:        svc.State,
		})
	}
	return resp, nil
}

// CreateVGPUPool creates a pool (AC2).
func (s *Service) CreateVGPUPool(ctx context.Context, req *vgpuv1.CreateVGPUPoolRequest) (*vgpuv1.CreateVGPUPoolResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if !poolNamePattern.MatchString(name) || len(name) > 63 {
		return nil, apierrors.Newf(apierrors.CodeVGPUPoolInvalid, "invalid vGPU pool: name must be DNS-safe, 1-63 chars")
	}
	labelKey := strings.TrimSpace(req.GetLabelKey())
	labelValue := strings.TrimSpace(req.GetLabelValue())
	if labelKey == "" || labelValue == "" || len(labelKey) > 128 || len(labelValue) > 128 {
		return nil, apierrors.Newf(apierrors.CodeVGPUPoolInvalid, "invalid vGPU pool: node label selector is required")
	}
	policy := strings.TrimSpace(req.GetPlacementPolicy())
	if policy == "" {
		policy = PolicyBinpack
	}
	if !isValidPolicy(policy) {
		return nil, apierrors.Newf(apierrors.CodeVGPUPoolInvalid, "invalid vGPU pool: placement policy must be binpack, spread or mutex")
	}
	templates, err := validateTemplates(req.GetTemplates())
	if err != nil {
		return nil, err
	}
	templatesJSON, err := json.Marshal(templates)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	pool := &VGPUPool{
		ID:              newPoolID(),
		Name:            name,
		LabelKey:        labelKey,
		LabelValue:      labelValue,
		PlacementPolicy: policy,
		Templates:       templatesJSON,
	}
	if err := repo.CreateVGPUPool(ctx, pool); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: "",
		ActorUserID:    s.actor(ctx),
		ActorType:      "user",
		Action:         "pool.created",
		ResourceType:   "vgpu_pool",
		ResourceID:     pool.ID,
		Result:         "success",
	})
	return &vgpuv1.CreateVGPUPoolResponse{
		Response: okResponse(),
		Pool:     s.poolProjection(ctx, pool),
	}, nil
}

// UpdateVGPUPool updates the placement policy, slice templates and
// node-label selector (AC8).
func (s *Service) UpdateVGPUPool(ctx context.Context, req *vgpuv1.UpdateVGPUPoolRequest) (*vgpuv1.UpdateVGPUPoolResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetVGPUPool(ctx, req.GetPoolId())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeVGPUPoolNotFound)
	}
	if err != nil {
		return nil, err
	}
	patch := map[string]any{}
	changed := false
	if req.PlacementPolicy != nil {
		policy := strings.TrimSpace(req.GetPlacementPolicy())
		if !isValidPolicy(policy) {
			return nil, apierrors.Newf(apierrors.CodeVGPUPoolInvalid, "invalid vGPU pool: placement policy must be binpack, spread or mutex")
		}
		if policy != row.PlacementPolicy {
			patch["placement_policy"] = policy
			changed = true
		}
	}
	if req.LabelKey != nil || req.LabelValue != nil {
		labelKey := row.LabelKey
		labelValue := row.LabelValue
		if req.LabelKey != nil {
			labelKey = strings.TrimSpace(req.GetLabelKey())
		}
		if req.LabelValue != nil {
			labelValue = strings.TrimSpace(req.GetLabelValue())
		}
		if labelKey == "" || labelValue == "" || len(labelKey) > 128 || len(labelValue) > 128 {
			return nil, apierrors.Newf(apierrors.CodeVGPUPoolInvalid, "invalid vGPU pool: node label selector is required")
		}
		if labelKey != row.LabelKey || labelValue != row.LabelValue {
			patch["label_key"] = labelKey
			patch["label_value"] = labelValue
			changed = true
		}
	}
	if req.Templates != nil {
		templates, err := validateTemplates(req.GetTemplates())
		if err != nil {
			return nil, err
		}
		templatesJSON, err := json.Marshal(templates)
		if err != nil {
			return nil, err
		}
		patch["templates"] = string(templatesJSON)
		changed = true
	}
	if !changed {
		return &vgpuv1.UpdateVGPUPoolResponse{
			Response: okResponse(),
			Pool:     s.poolProjection(ctx, row),
		}, nil
	}
	if err := repo.UpdateVGPUPool(ctx, row.ID, patch, time.Now().UTC()); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: "",
		ActorUserID:    s.actor(ctx),
		ActorType:      "user",
		Action:         "pool.updated",
		ResourceType:   "vgpu_pool",
		ResourceID:     row.ID,
		Result:         "success",
	})
	updated, err := repo.GetVGPUPool(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return &vgpuv1.UpdateVGPUPoolResponse{
		Response: okResponse(),
		Pool:     s.poolProjection(ctx, updated),
	}, nil
}

// DeleteVGPUPool deletes a pool, blocked when services use it (AC9).
func (s *Service) DeleteVGPUPool(ctx context.Context, req *vgpuv1.DeleteVGPUPoolRequest) (*vgpuv1.DeleteVGPUPoolResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetVGPUPool(ctx, req.GetPoolId())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeVGPUPoolNotFound)
	}
	if err != nil {
		return nil, err
	}
	count, err := repo.CountSliceServices(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, apierrors.Newf(apierrors.CodeVGPUPoolInUse, "%d services are using this pool", count)
	}
	if err := repo.DeleteVGPUPool(ctx, row.ID); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: "",
		ActorUserID:    s.actor(ctx),
		ActorType:      "user",
		Action:         "pool.deleted",
		ResourceType:   "vgpu_pool",
		ResourceID:     row.ID,
		Result:         "success",
	})
	return &vgpuv1.DeleteVGPUPoolResponse{Response: okResponse()}, nil
}

// ListTenantVGPUPools returns the grant-filtered pools the caller's org
// may use (AC7).
func (s *Service) ListTenantVGPUPools(ctx context.Context, _ *vgpuv1.ListTenantVGPUPoolsRequest) (*vgpuv1.ListTenantVGPUPoolsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	rows, err := repo.ListGrantedPools(ctx, orgID, s.grantChecker)
	if err != nil {
		return nil, err
	}
	pools := make([]*vgpuv1.TenantVGPUPool, 0, len(rows))
	for _, row := range rows {
		templates, err := templatesFromJSON(row.Templates)
		if err != nil {
			continue
		}
		protoTemplates := make([]*vgpuv1.SliceTemplate, 0, len(templates))
		for _, t := range templates {
			protoTemplates = append(protoTemplates, &vgpuv1.SliceTemplate{
				Name: t.Name, MemoryGib: t.MemoryGiB, ComputePct: t.ComputePct, Description: t.Description,
			})
		}
		pools = append(pools, &vgpuv1.TenantVGPUPool{
			PoolId:    row.ID,
			Name:      row.Name,
			Templates: protoTemplates,
		})
	}
	return &vgpuv1.ListTenantVGPUPoolsResponse{
		Response: okResponse(),
		Pools:    pools,
	}, nil
}

// ValidateSlice implements the infer module's SliceValidator seam
// (feature #52, AD13). It returns the resolved slice template when the
// pool exists, is granted to the org, and the template exists in the
// pool; otherwise it returns 13403.
func (s *Service) ValidateSlice(ctx context.Context, orgID, poolID, templateID string) (infer.SliceTemplateInfo, error) {
	repo, err := s.repository()
	if err != nil {
		return infer.SliceTemplateInfo{}, err
	}
	row, err := repo.GetVGPUPool(ctx, poolID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return infer.SliceTemplateInfo{}, apierrors.New(apierrors.CodeVGPUSliceUnavailable)
	}
	if err != nil {
		return infer.SliceTemplateInfo{}, err
	}
	// Grant check: a nil grant checker grants everything (default-allow).
	if s.grantChecker != nil {
		granted, err := s.grantChecker.IsPoolGranted(ctx, orgID, row.ID)
		if err != nil {
			return infer.SliceTemplateInfo{}, err
		}
		if !granted {
			return infer.SliceTemplateInfo{}, apierrors.New(apierrors.CodeVGPUSliceUnavailable)
		}
	}
	templates, err := templatesFromJSON(row.Templates)
	if err != nil {
		return infer.SliceTemplateInfo{}, apierrors.New(apierrors.CodeVGPUSliceUnavailable)
	}
	for _, t := range templates {
		if t.Name == templateID {
			return infer.SliceTemplateInfo{MemoryGiB: t.MemoryGiB, ComputePct: t.ComputePct}, nil
		}
	}
	return infer.SliceTemplateInfo{}, apierrors.New(apierrors.CodeVGPUSliceUnavailable)
}

// resolveOrg returns the organization context for a user-realm read
// (feature #52, AD10): the session's active org when a session is
// present, otherwise the transitional X-Organization-Id header.
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

// poolProjection assembles the FR1.1 projection for a pool row. The
// membership/capacity fields are best-effort: a nil projection answers
// 0 (the transitional shape every seam follows).
func (s *Service) poolProjection(_ context.Context, row *VGPUPool) *vgpuv1.VGPUPool {
	templates, err := templatesFromJSON(row.Templates)
	if err != nil {
		templates = []SliceTemplate{}
	}
	protoTemplates := make([]*vgpuv1.SliceTemplate, 0, len(templates))
	for _, t := range templates {
		protoTemplates = append(protoTemplates, &vgpuv1.SliceTemplate{
			Name: t.Name, MemoryGib: t.MemoryGiB, ComputePct: t.ComputePct, Description: t.Description,
		})
	}
	out := &vgpuv1.VGPUPool{
		PoolId:          row.ID,
		Name:            row.Name,
		LabelKey:        row.LabelKey,
		LabelValue:      row.LabelValue,
		PlacementPolicy: row.PlacementPolicy,
		Templates:       protoTemplates,
	}
	if s.membership != nil {
		out.NodeCount = s.membership.NodeCount(row.ID, row.LabelKey, row.LabelValue)
		agg := s.membership.TemplateAllocation(row.ID, row.LabelKey, row.LabelValue)
		for _, t := range templates {
			a := agg[t.Name]
			out.Allocation = append(out.Allocation, &vgpuv1.TemplateAllocation{
				TemplateName: t.Name,
				Allocated:    a.Allocated,
				Free:         a.Free,
			})
		}
		out.UtilizationPct = s.membership.Utilization(row.ID, row.LabelKey, row.LabelValue)
	}
	return out
}

// nodeToProto maps a membership node to its wire projection.
func nodeToProto(n *acceleratorNode) *vgpuv1.PoolNode {
	out := &vgpuv1.PoolNode{
		NodeId: n.NodeID,
		Name:   n.Name,
		Health: n.Health,
	}
	for _, g := range n.GPUs {
		gpu := &vgpuv1.PoolGPU{
			Index: g.Index,
			Model: g.Model,
			Full:  g.Allocated && !g.Free,
		}
		for _, s := range g.Slices {
			gpu.Slices = append(gpu.Slices, &vgpuv1.SliceAllocation{
				TemplateName: s.TemplateName,
				ServiceId:    s.ServiceID,
				MemoryGib:    s.MemoryGiB,
				ComputePct:   s.ComputePct,
			})
		}
		out.Gpus = append(out.Gpus, gpu)
	}
	return out
}

// validateTemplates validates the slice templates per FR2.2 (AD3).
func validateTemplates(templates []*vgpuv1.SliceTemplate) ([]SliceTemplate, error) {
	if len(templates) == 0 {
		return nil, apierrors.Newf(apierrors.CodeVGPUSliceTemplateInvalid, "invalid slice template: at least one template is required")
	}
	seen := map[string]bool{}
	out := make([]SliceTemplate, 0, len(templates))
	for _, t := range templates {
		name := strings.TrimSpace(t.GetName())
		if name == "" || len(name) > 63 {
			return nil, apierrors.Newf(apierrors.CodeVGPUSliceTemplateInvalid, "invalid slice template: name is required")
		}
		if seen[name] {
			return nil, apierrors.Newf(apierrors.CodeVGPUSliceTemplateInvalid, "invalid slice template: duplicate name %q", name)
		}
		seen[name] = true
		if t.GetMemoryGib() < 1 {
			return nil, apierrors.Newf(apierrors.CodeVGPUSliceTemplateInvalid, "invalid slice template %q: memory must be at least 1 GiB", name)
		}
		if t.GetComputePct() < 5 || t.GetComputePct() > 100 {
			return nil, apierrors.Newf(apierrors.CodeVGPUSliceTemplateInvalid, "invalid slice template %q: compute must be 5-100", name)
		}
		out = append(out, SliceTemplate{
			Name:        name,
			MemoryGiB:   t.GetMemoryGib(),
			ComputePct:  t.GetComputePct(),
			Description: strings.TrimSpace(t.GetDescription()),
		})
	}
	return out, nil
}

// isValidPolicy reports whether the placement policy is a closed enum
// value.
func isValidPolicy(policy string) bool {
	switch policy {
	case PolicyBinpack, PolicySpread, PolicyMutex:
		return true
	default:
		return false
	}
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

// newPoolID returns a new pool id. It is a variable so tests can
// override it.
var newPoolID = func() string {
	return newUUID()
}

// TemplateAllocation is the aggregate per-template allocation (FR3.2).
type TemplateAllocation struct {
	TemplateName string
	Allocated    int64
	Free         int64
}