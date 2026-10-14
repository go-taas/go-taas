// Package routing implements the intelligent-model-routing module
// (feature #49). It owns the routing-group registry (candidates, intent
// rules), the request-time virtual-name resolver, the decision log and
// the group grant list. The admin surface serves the registry under
// /api/v1/admin/routing-groups/*; the resolver is an internal RPC
// consumed by the data-plane gateway and the playground seam.
package routing

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	routingv1 "github.com/go-taas/go-taas/proto/taas/routing/v1"
)

// ServiceName is the service registration name.
const ServiceName = "routing"

// roleAdmin is the minimum org role on the routing RPCs.
const roleAdmin = "admin"

// groupNamePattern is the accepted virtual-name shape (AD10):
// ^[a-z][a-z0-9-]{2,63}$.
var groupNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{2,63}$`)

// SessionOrgResolver resolves the session's active organization.
type SessionOrgResolver interface {
	SessionActiveOrg(ctx context.Context) (string, error)
}

// SessionUserResolver resolves the authenticated caller's user id.
type SessionUserResolver interface {
	SessionUserID(ctx context.Context) (string, error)
}

// RoleGuard enforces the minimum org role on the routing RPCs.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// AuditRecorder records routing mutations best-effort.
type AuditRecorder interface {
	Record(ctx context.Context, ev *audit.AuditEvent)
}

// VirtualModelEntry is the masked virtual-model projection returned to
// the model module's user-realm catalog (the VirtualModelLister seam's
// row).
type VirtualModelEntry struct {
	// ID is the group id (surfaced as the virtual model_id).
	ID string
	// Name is the virtual name tenants call.
	Name string
	// Description is the group description.
	Description string
}

// GroupGrantRow is one group grant row of the GroupAuthorization seam.
type GroupGrantRow struct {
	// OrganizationID is the granted organization.
	OrganizationID string
	// GrantedBy is the acting admin user id.
	GrantedBy string
	// CreatedAt is the grant time.
	CreatedAt time.Time
}

// Service implements the routing gRPC service (feature #49). It is
// admin-surface, admin-role, platform-scoped for the registry RPCs; the
// internal ResolveRoutingGroup RPC is called by the data plane.
type Service struct {
	routingv1.UnimplementedRoutingServiceServer

	components server.Components

	groupRepo    *Repository
	decisionRepo *DecisionRepository
	resolver     *resolver

	// catalog is the model-module seam (availability, org grant,
	// reserved names, candidate existence).
	catalog ModelCatalog
	// keyScope is the auth-module seam (the key-scope check).
	keyScope KeyScopeLister

	sessionOrgResolver  SessionOrgResolver
	sessionUserResolver SessionUserResolver
	roleGuard           RoleGuard
	auditRecorder       AuditRecorder
}

// New constructs the routing service.
func New(components server.Components) *Service {
	return &Service{components: components}
}

// NewForFVT constructs a routing service bound to a caller-provided GORM
// database for full-verification tests.
func NewForFVT(db *gorm.DB) *Service {
	groupRepo := NewRepository(db)
	decisionRepo := NewDecisionRepository(db)
	return &Service{
		groupRepo:    groupRepo,
		decisionRepo: decisionRepo,
		resolver:     newResolver(groupRepo, decisionRepo),
	}
}

// MigrateSchemaForFVT applies the routing schema onto a caller-provided
// database.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&Group{}, &Candidate{}, &Rule{}, &GroupGrant{}, &Decision{})
}

// SetModelCatalog injects the model-module seam.
func (s *Service) SetModelCatalog(c ModelCatalog) { s.catalog = c }

// SetKeyScopeLister injects the auth-module key-scope seam.
func (s *Service) SetKeyScopeLister(l KeyScopeLister) { s.keyScope = l }

// SetLatencySource injects the observed-latency seam (tests; production
// uses the decision repository's request_logs query).
func (s *Service) SetLatencySource(src LatencySource) {
	s.ensureResolver().latency = src
}

// SetSessionOrgResolver injects the session-organization resolver.
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver.
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard.
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// SetAuditRecorder injects the best-effort audit recorder.
func (s *Service) SetAuditRecorder(r AuditRecorder) { s.auditRecorder = r }

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	routingv1.RegisterRoutingServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return routingv1.RegisterRoutingServiceHandler
}

// Migrate implements server.Migrator: it creates the five routing
// tables.
func (s *Service) Migrate(ctx context.Context) error {
	if s.groupRepo != nil {
		return nil
	}
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&Group{}, &Candidate{}, &Rule{}, &GroupGrant{}, &Decision{})
}

// gormDB resolves the *gorm.DB from the shared components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "routing: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "routing: unexpected database handle type %T", raw)
	}
	return db, nil
}

// groups lazily wires the group repository and the resolver.
func (s *Service) groups() (*Repository, error) {
	if s.groupRepo != nil {
		return s.groupRepo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.groupRepo = NewRepository(db)
	s.resolver = newResolver(s.groupRepo, s.decisionRepoOf(db))
	return s.groupRepo, nil
}

// decisions lazily wires the decision repository.
func (s *Service) decisions() (*DecisionRepository, error) {
	if s.decisionRepo != nil {
		return s.decisionRepo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.decisionRepo = NewDecisionRepository(db)
	if s.resolver != nil {
		s.resolver.decisionRepo = s.decisionRepo
	}
	return s.decisionRepo, nil
}

// decisionRepoOf constructs the decision repository when needed.
func (s *Service) decisionRepoOf(db *gorm.DB) *DecisionRepository {
	if s.decisionRepo != nil {
		return s.decisionRepo
	}
	s.decisionRepo = NewDecisionRepository(db)
	return s.decisionRepo
}

// ensureResolver returns the resolver, wiring it lazily and propagating
// the service's seams (catalog, key scope) so a resolver constructed
// before the setters still sees them.
func (s *Service) ensureResolver() *resolver {
	if s.resolver == nil {
		s.resolver = newResolver(s.groupRepo, s.decisionRepo)
	}
	if s.resolver.groupRepo == nil {
		s.resolver.groupRepo = s.groupRepo
	}
	if s.resolver.decisionRepo == nil {
		s.resolver.decisionRepo = s.decisionRepo
	}
	if s.resolver.catalog == nil {
		s.resolver.catalog = s.catalog
	}
	if s.resolver.keyScope == nil {
		s.resolver.keyScope = s.keyScope
	}
	return s.resolver
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

// requireAdminRole enforces the admin/owner role on the routing RPCs
// (AD6). The routing registry is platform-scoped, so a missing session
// context is NOT authorized: the caller must be an authenticated admin
// of the active organization (the #45 fail-closed pattern).
func (s *Service) requireAdminRole(ctx context.Context) error {
	if s.roleGuard == nil || s.sessionUserResolver == nil {
		return apierrors.New(apierrors.CodeUnauthorized)
	}
	userID, err := s.sessionUserResolver.SessionUserID(ctx)
	if err != nil {
		return err
	}
	if userID == "" {
		return apierrors.New(apierrors.CodeUnauthorized)
	}
	orgID := ""
	if s.sessionOrgResolver != nil {
		if org, orgErr := s.sessionOrgResolver.SessionActiveOrg(ctx); orgErr == nil {
			orgID = org
		}
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

// recordAudit records one best-effort audit event.
func (s *Service) recordAudit(ctx context.Context, action, resourceID, result string) {
	if s.auditRecorder == nil {
		return
	}
	s.auditRecorder.Record(ctx, &audit.AuditEvent{
		ActorUserID:  s.actor(ctx),
		ActorType:    "user",
		Action:       action,
		ResourceType: "routing_group",
		ResourceID:   resourceID,
		Result:       result,
	})
}

// validateGroupName validates the virtual-name shape (13103).
func validateGroupName(name string) error {
	if !groupNamePattern.MatchString(name) {
		return apierrors.New(apierrors.CodeRoutingGroupNameInvalid)
	}
	return nil
}

// validStrategy reports whether the strategy is one of the four values.
func validStrategy(strategy string) bool {
	for _, s := range Strategies {
		if s == strategy {
			return true
		}
	}
	return false
}

// validateNameUniqueness checks the group-name uniqueness (13102) and
// the reserved real-model-name rule (13113).
func (s *Service) validateNameUniqueness(ctx context.Context, repo *Repository, name, selfID string) error {
	existing, err := repo.FindByName(ctx, name)
	if err != nil {
		return err
	}
	if existing != nil && existing.ID != selfID {
		return apierrors.New(apierrors.CodeRoutingGroupNameConflict)
	}
	if s.catalog != nil {
		reserved, err := s.catalog.CatalogModelNameExists(ctx, name)
		if err != nil {
			return err
		}
		if reserved {
			return apierrors.New(apierrors.CodeRoutingGroupNameReserved)
		}
	}
	return nil
}

// ListRoutingGroups returns one page of groups with candidate/rule
// counts and the 24h request count (AD15).
func (s *Service) ListRoutingGroups(ctx context.Context, req *routingv1.ListRoutingGroupsRequest) (*routingv1.ListRoutingGroupsResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.groups()
	if err != nil {
		return nil, err
	}
	decisions, err := s.decisions()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePage(req.GetPage())
	rows, total, err := repo.List(ctx, GroupFilter{
		Name:     req.GetName(),
		Strategy: req.GetStrategy(),
		Enabled:  req.GetEnabled(),
		Offset:   offset,
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	since := time.Now().UTC().Add(-24 * time.Hour)
	out := make([]*routingv1.RoutingGroup, 0, len(rows))
	for _, row := range rows {
		candidates, err := repo.ListCandidates(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		rules, err := repo.ListRules(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		count24h, err := decisions.CountSince(ctx, row.ID, since)
		if err != nil {
			return nil, err
		}
		out = append(out, groupToProto(row, int64(len(candidates)), int64(len(rules)), count24h))
	}
	return &routingv1.ListRoutingGroupsResponse{
		Response: okResponse(),
		Groups:   out,
		PageMeta: &commonv1.PageMeta{
			Total:  total,
			Offset: int64(offset),
			Limit:  int32(limit), //nolint:gosec // limit is capped at 100 by normalizePage.
		},
	}, nil
}

// CreateRoutingGroup creates a disabled group (AC1).
func (s *Service) CreateRoutingGroup(ctx context.Context, req *routingv1.CreateRoutingGroupRequest) (*routingv1.CreateRoutingGroupResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.groups()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if err := validateGroupName(name); err != nil {
		return nil, err
	}
	description := strings.TrimSpace(req.GetDescription())
	if len(description) > maxDescriptionLen {
		return nil, apierrors.New(apierrors.CodeRoutingGroupNameInvalid)
	}
	strategy := strings.TrimSpace(req.GetStrategy())
	if strategy == "" {
		strategy = StrategyWeighted
	}
	if !validStrategy(strategy) {
		return nil, apierrors.New(apierrors.CodeRoutingGroupStrategyInvalid)
	}
	if err := s.validateNameUniqueness(ctx, repo, name, ""); err != nil {
		return nil, err
	}
	actor := s.actor(ctx)
	now := time.Now().UTC()
	row := &Group{
		ID:          uuid.NewString(),
		Name:        name,
		Description: description,
		Strategy:    strategy,
		Enabled:     false,
		CreatedBy:   actor,
		UpdatedBy:   actor,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := repo.Create(ctx, row); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "routing.group_created", row.ID, "success")
	return &routingv1.CreateRoutingGroupResponse{
		Response: okResponse(),
		Group:    groupToProto(row, 0, 0, 0),
	}, nil
}

// GetRoutingGroup returns one group with its ordered candidate and rule
// lists (AC2).
func (s *Service) GetRoutingGroup(ctx context.Context, req *routingv1.GetRoutingGroupRequest) (*routingv1.GetRoutingGroupResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.groups()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetByID(ctx, req.GetGroupId())
	if err != nil {
		return nil, err
	}
	candidates, err := repo.ListCandidates(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	rules, err := repo.ListRules(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return &routingv1.GetRoutingGroupResponse{
		Response:   okResponse(),
		Group:      groupToProto(row, int64(len(candidates)), int64(len(rules)), 0),
		Candidates: candidatesToProto(candidates),
		Rules:      rulesToProto(rules),
	}, nil
}

// UpdateRoutingGroup edits name, description, strategy and enabled
// (AC3). Enabling requires >= 1 candidate; enabling a manual group
// requires exactly one pinned candidate (13107).
func (s *Service) UpdateRoutingGroup(ctx context.Context, req *routingv1.UpdateRoutingGroupRequest) (*routingv1.UpdateRoutingGroupResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.groups()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetByID(ctx, req.GetGroupId())
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if err := validateGroupName(name); err != nil {
		return nil, err
	}
	description := strings.TrimSpace(req.GetDescription())
	if len(description) > maxDescriptionLen {
		return nil, apierrors.New(apierrors.CodeRoutingGroupNameInvalid)
	}
	// PATCH semantics over proto3 scalars: an empty strategy keeps the
	// current value; a non-empty one must be valid (13107).
	strategy := strings.TrimSpace(req.GetStrategy())
	if strategy == "" {
		strategy = row.Strategy
	}
	if !validStrategy(strategy) {
		return nil, apierrors.New(apierrors.CodeRoutingGroupStrategyInvalid)
	}
	if name != row.Name {
		if err := s.validateNameUniqueness(ctx, repo, name, row.ID); err != nil {
			return nil, err
		}
		row.Name = name
	}
	row.Description = description
	enabled := req.GetEnabled()
	if enabled && !row.Enabled {
		// Enabling requires >= 1 candidate; manual requires exactly one
		// pinned candidate (13107).
		candidates, err := repo.ListCandidates(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			return nil, apierrors.New(apierrors.CodeRoutingGroupStrategyInvalid)
		}
		if strategy == StrategyManual {
			pins := 0
			for _, c := range candidates {
				if c.Pinned {
					pins++
				}
			}
			if pins != 1 {
				return nil, apierrors.New(apierrors.CodeRoutingGroupStrategyInvalid)
			}
		}
	}
	// Switching away from manual drops the pin's meaning: the UI clears
	// it and the resolver ignores a stale pin on a non-manual group.
	row.Strategy = strategy
	wasEnabled := row.Enabled
	row.Enabled = enabled
	row.UpdatedBy = s.actor(ctx)
	if err := repo.Update(ctx, row); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "routing.group_updated", row.ID, "success")
	if enabled != wasEnabled {
		if enabled {
			s.recordAudit(ctx, "routing.group_enabled", row.ID, "success")
		} else {
			s.recordAudit(ctx, "routing.group_disabled", row.ID, "success")
		}
	}
	candidates, err := repo.ListCandidates(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	rules, err := repo.ListRules(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return &routingv1.UpdateRoutingGroupResponse{
		Response: okResponse(),
		Group:    groupToProto(row, int64(len(candidates)), int64(len(rules)), 0),
	}, nil
}

// DeleteRoutingGroup removes the group (AC4). Blocked while any
// organization grant references it (13111).
func (s *Service) DeleteRoutingGroup(ctx context.Context, req *routingv1.DeleteRoutingGroupRequest) (*routingv1.DeleteRoutingGroupResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.groups()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetByID(ctx, req.GetGroupId())
	if err != nil {
		return nil, err
	}
	grants, err := repo.CountGrants(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	if grants > 0 {
		return nil, apierrors.New(apierrors.CodeRoutingGroupInUse)
	}
	if err := repo.Delete(ctx, row.ID); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "routing.group_deleted", row.ID, "success")
	return &routingv1.DeleteRoutingGroupResponse{Response: okResponse()}, nil
}

// ReplaceRoutingGroupCandidates atomically replaces the ordered
// candidate list (AC5, §5.4 validation).
func (s *Service) ReplaceRoutingGroupCandidates(ctx context.Context, req *routingv1.ReplaceRoutingGroupCandidatesRequest) (*routingv1.ReplaceRoutingGroupCandidatesResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.groups()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetByID(ctx, req.GetGroupId())
	if err != nil {
		return nil, err
	}
	inputs := req.GetCandidates()
	if len(inputs) < minCandidateEntries || len(inputs) > maxCandidateEntries {
		return nil, apierrors.New(apierrors.CodeRoutingGroupCandidateInvalid)
	}
	seen := make(map[string]struct{}, len(inputs))
	rows := make([]*Candidate, 0, len(inputs))
	pins := 0
	for _, in := range inputs {
		modelID := strings.TrimSpace(in.GetModelId())
		if modelID == "" {
			return nil, apierrors.New(apierrors.CodeRoutingGroupCandidateInvalid)
		}
		if _, dup := seen[modelID]; dup {
			return nil, apierrors.New(apierrors.CodeRoutingGroupCandidateExists)
		}
		seen[modelID] = struct{}{}
		// Each entry must reference a real catalog model (a nested group
		// id fails with 13104 — GetCatalogModel misses it).
		var modelName string
		if s.catalog != nil {
			m, err := s.catalog.GetCatalogModel(ctx, modelID)
			if err != nil || m == nil {
				return nil, apierrors.New(apierrors.CodeRoutingGroupCandidateInvalid)
			}
			modelName = m.Name
		}
		weight := int(in.GetWeight())
		if weight == 0 {
			weight = defaultWeight
		}
		if weight < minWeight || weight > maxWeight {
			return nil, apierrors.New(apierrors.CodeRoutingGroupCandidateInvalid)
		}
		costTier := int(in.GetCostTier())
		if costTier != 0 && (costTier < minTier || costTier > maxTier) {
			return nil, apierrors.New(apierrors.CodeRoutingGroupCandidateInvalid)
		}
		latencyTier := int(in.GetLatencyTier())
		if latencyTier != 0 && (latencyTier < minTier || latencyTier > maxTier) {
			return nil, apierrors.New(apierrors.CodeRoutingGroupCandidateInvalid)
		}
		pinned := in.GetPinned()
		if pinned {
			if row.Strategy != StrategyManual {
				return nil, apierrors.New(apierrors.CodeRoutingGroupCandidateInvalid)
			}
			pins++
			if pins > 1 {
				return nil, apierrors.New(apierrors.CodeRoutingGroupCandidateInvalid)
			}
		}
		rows = append(rows, &Candidate{
			ModelID:     modelID,
			ModelName:   modelName,
			Weight:      weight,
			CostTier:    costTier,
			LatencyTier: latencyTier,
			Pinned:      pinned,
		})
	}
	if err := repo.ReplaceCandidates(ctx, row.ID, rows); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "routing.candidates_updated", row.ID, "success")
	saved, err := repo.ListCandidates(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return &routingv1.ReplaceRoutingGroupCandidatesResponse{
		Response:   okResponse(),
		Candidates: candidatesToProto(saved),
	}, nil
}

// ReplaceRoutingGroupRules atomically replaces the ordered rule list
// (AC6, §5.4 validation).
func (s *Service) ReplaceRoutingGroupRules(ctx context.Context, req *routingv1.ReplaceRoutingGroupRulesRequest) (*routingv1.ReplaceRoutingGroupRulesResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.groups()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetByID(ctx, req.GetGroupId())
	if err != nil {
		return nil, err
	}
	inputs := req.GetRules()
	if len(inputs) > maxRuleEntries {
		return nil, apierrors.New(apierrors.CodeRoutingGroupRuleInvalid)
	}
	candidates, err := repo.ListCandidates(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	candidateNames := make(map[string]string, len(candidates))
	for _, c := range candidates {
		candidateNames[c.ModelID] = c.ModelName
	}
	seen := make(map[string]struct{}, len(inputs))
	rows := make([]*Rule, 0, len(inputs))
	for _, in := range inputs {
		name := strings.TrimSpace(in.GetName())
		if name == "" || len(name) > maxRuleNameLen {
			return nil, apierrors.New(apierrors.CodeRoutingGroupRuleInvalid)
		}
		if _, dup := seen[name]; dup {
			return nil, apierrors.New(apierrors.CodeRoutingGroupRuleExists)
		}
		seen[name] = struct{}{}
		matchType := strings.TrimSpace(in.GetMatchType())
		if matchType != MatchKeyword && matchType != MatchRegex {
			return nil, apierrors.New(apierrors.CodeRoutingGroupRuleInvalid)
		}
		pattern := strings.TrimSpace(in.GetPattern())
		if pattern == "" || len(pattern) > maxPatternLen {
			return nil, apierrors.New(apierrors.CodeRoutingGroupRuleInvalid)
		}
		if matchType == MatchRegex {
			if _, err := regexp.Compile(pattern); err != nil {
				return nil, apierrors.New(apierrors.CodeRoutingGroupRuleInvalid)
			}
		}
		target := strings.TrimSpace(in.GetTargetModelId())
		targetName, ok := candidateNames[target]
		if !ok {
			return nil, apierrors.New(apierrors.CodeRoutingGroupRuleInvalid)
		}
		rows = append(rows, &Rule{
			Name:            name,
			MatchType:       matchType,
			Pattern:         pattern,
			TargetModelID:   target,
			TargetModelName: targetName,
			Enabled:         in.GetEnabled(),
		})
	}
	if err := repo.ReplaceRules(ctx, row.ID, rows); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "routing.rules_updated", row.ID, "success")
	saved, err := repo.ListRules(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return &routingv1.ReplaceRoutingGroupRulesResponse{
		Response: okResponse(),
		Rules:    rulesToProto(saved),
	}, nil
}

// TestRoutingResolution is the dry run (AC7): the platform-org context,
// no decision write.
func (s *Service) TestRoutingResolution(ctx context.Context, req *routingv1.TestRoutingResolutionRequest) (*routingv1.TestRoutingResolutionResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.groups()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetByID(ctx, req.GetGroupId())
	if err != nil {
		return nil, err
	}
	res, err := s.ensureResolver().Resolve(ctx, row, req.GetPrompt(), "", "", true)
	if err != nil {
		return nil, err
	}
	return &routingv1.TestRoutingResolutionResponse{
		Response:        okResponse(),
		MatchedRuleId:   res.MatchedRuleID,
		MatchedRuleName: res.MatchedRuleName,
		Attempts:        candidatesToProto(res.Attempts),
		Skipped:         res.Skipped,
	}, nil
}

// ListRoutingDecisions returns the decision log (AC8).
func (s *Service) ListRoutingDecisions(ctx context.Context, req *routingv1.ListRoutingDecisionsRequest) (*routingv1.ListRoutingDecisionsResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	decisions, err := s.decisions()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePage(req.GetPage())
	since := time.Time{}
	switch strings.TrimSpace(req.GetRange()) {
	case "1h":
		since = time.Now().UTC().Add(-time.Hour)
	case "24h":
		since = time.Now().UTC().Add(-24 * time.Hour)
	case "7d":
		since = time.Now().UTC().Add(-7 * 24 * time.Hour)
	}
	rows, total, err := decisions.List(ctx, DecisionFilter{
		GroupID:       req.GetGroupId(),
		Strategy:      req.GetStrategy(),
		MatchedRule:   req.GetMatchedRule(),
		SelectedModel: req.GetSelectedModel(),
		Outcome:       req.GetOutcome(),
		Since:         since,
		Offset:        offset,
		Limit:         limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*routingv1.RoutingDecision, 0, len(rows))
	for _, row := range rows {
		out = append(out, decisionToProto(row))
	}
	return &routingv1.ListRoutingDecisionsResponse{
		Response:  okResponse(),
		Decisions: out,
		PageMeta: &commonv1.PageMeta{
			Total:  total,
			Offset: int64(offset),
			Limit:  int32(limit), //nolint:gosec // limit is capped at 100 by normalizePage.
		},
	}, nil
}

// GetRoutingDecision returns one decision (AC8).
func (s *Service) GetRoutingDecision(ctx context.Context, req *routingv1.GetRoutingDecisionRequest) (*routingv1.GetRoutingDecisionResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	decisions, err := s.decisions()
	if err != nil {
		return nil, err
	}
	row, err := decisions.GetByID(ctx, req.GetDecisionId())
	if err != nil {
		return nil, err
	}
	return &routingv1.GetRoutingDecisionResponse{
		Response: okResponse(),
		Decision: decisionToProto(row),
	}, nil
}

// ResolveRoutingGroup is the internal enforcement RPC (AD14): it
// resolves a virtual name to an ordered attempt list plus the skipped
// candidates, or fails closed (13101/13110/13109).
func (s *Service) ResolveRoutingGroup(ctx context.Context, req *routingv1.ResolveRoutingGroupRequest) (*routingv1.ResolveRoutingGroupResponse, error) {
	repo, err := s.groups()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetGroupName())
	if name == "" {
		return nil, apierrors.New(apierrors.CodeRoutingGroupNotFound)
	}
	row, err := repo.FindByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, apierrors.New(apierrors.CodeRoutingGroupNotFound)
	}
	if !row.Enabled {
		return nil, apierrors.New(apierrors.CodeRoutingGroupDisabled)
	}
	res, err := s.ensureResolver().Resolve(ctx, row, req.GetPrompt(), req.GetOrgId(), req.GetApiKeyId(), false)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(res.Attempts))
	names := make([]string, 0, len(res.Attempts))
	for _, c := range res.Attempts {
		ids = append(ids, c.ModelID)
		names = append(names, c.ModelName)
	}
	return &routingv1.ResolveRoutingGroupResponse{
		Response:          okResponse(),
		AttemptModelIds:   ids,
		AttemptModelNames: names,
		Skipped:           res.Skipped,
		MatchedRuleId:     res.MatchedRuleID,
		MatchedRuleName:   res.MatchedRuleName,
		Strategy:          res.Strategy,
	}, nil
}

// RecordDecision persists the final outcome of a resolved request
// (FR5.5, the infer.RoutingResolver seam's second half). outcomes maps
// model ids to their final outcome strings; the served model is named
// by selectedModelID/selectedModelName.
func (s *Service) RecordDecision(ctx context.Context, res *routingv1.ResolveRoutingGroupResponse, requestID, groupName, selectedModelID, selectedModelName string, outcomes map[string]string) error {
	decisions, err := s.decisions()
	if err != nil {
		return err
	}
	repo, err := s.groups()
	if err != nil {
		return err
	}
	group, err := repo.FindByName(ctx, groupName)
	if err != nil {
		return err
	}
	groupID := ""
	if group != nil {
		groupID = group.ID
	}
	candidates := make(DecisionCandidates, 0, len(res.GetAttemptModelIds())+len(res.GetSkipped()))
	for i, id := range res.GetAttemptModelIds() {
		name := ""
		if i < len(res.GetAttemptModelNames()) {
			name = res.GetAttemptModelNames()[i]
		}
		outcome := OutcomeNotAttempted
		if o, ok := outcomes[id]; ok && o != "" {
			outcome = o
		}
		candidates = append(candidates, &DecisionCandidate{
			ModelID:   id,
			ModelName: name,
			Outcome:   outcome,
		})
	}
	for _, sk := range res.GetSkipped() {
		candidates = append(candidates, &DecisionCandidate{
			ModelID:   sk.GetModelId(),
			ModelName: sk.GetModelName(),
			Outcome:   sk.GetReason(),
		})
	}
	return decisions.Insert(ctx, &Decision{
		RequestID:         requestID,
		GroupID:           groupID,
		GroupName:         groupName,
		Strategy:          res.GetStrategy(),
		MatchedRuleID:     res.GetMatchedRuleId(),
		MatchedRuleName:   res.GetMatchedRuleName(),
		SelectedModelID:   selectedModelID,
		SelectedModelName: selectedModelName,
		Candidates:        candidates,
	})
}

// GroupExists implements the auth.GroupExistenceChecker seam (AD13).
func (s *Service) GroupExists(ctx context.Context, groupID string) (bool, error) {
	repo, err := s.groups()
	if err != nil {
		return false, err
	}
	if _, err := uuid.Parse(groupID); err != nil {
		return false, nil
	}
	row, err := repo.GetByID(ctx, groupID)
	if err != nil {
		if apierrors.CodeOf(err) == apierrors.CodeRoutingGroupNotFound {
			return false, nil
		}
		return false, err
	}
	return row != nil, nil
}

// GrantGroupAccess implements the model.GroupAuthorization seam (AD12).
func (s *Service) GrantGroupAccess(ctx context.Context, groupID, organizationID, grantedBy string) error {
	repo, err := s.groups()
	if err != nil {
		return err
	}
	if _, err := repo.GetByID(ctx, groupID); err != nil {
		return err
	}
	return repo.GrantAccess(ctx, groupID, organizationID, grantedBy)
}

// RevokeGroupAccess implements the model.GroupAuthorization seam (AD12).
func (s *Service) RevokeGroupAccess(ctx context.Context, groupID, organizationID string) error {
	repo, err := s.groups()
	if err != nil {
		return err
	}
	if _, err := repo.GetByID(ctx, groupID); err != nil {
		return err
	}
	return repo.RevokeAccess(ctx, groupID, organizationID)
}

// ListGroupAuthorizations implements the model.GroupAuthorization seam
// (AD12).
func (s *Service) ListGroupAuthorizations(ctx context.Context, groupID string, offset, limit int) ([]*GroupGrantRow, int64, error) {
	repo, err := s.groups()
	if err != nil {
		return nil, 0, err
	}
	if _, err := repo.GetByID(ctx, groupID); err != nil {
		return nil, 0, err
	}
	rows, total, err := repo.ListGrants(ctx, groupID, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]*GroupGrantRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, &GroupGrantRow{
			OrganizationID: row.OrganizationID,
			GrantedBy:      row.GrantedBy,
			CreatedAt:      row.CreatedAt,
		})
	}
	return out, total, nil
}

// CountGroupGrants implements the model.GroupAuthorization seam (AD12).
func (s *Service) CountGroupGrants(ctx context.Context, groupID string) (int64, error) {
	repo, err := s.groups()
	if err != nil {
		return 0, err
	}
	return repo.CountGrants(ctx, groupID)
}

// ListGrantedGroups implements the model.VirtualModelLister seam
// (AD12): the organization's granted groups as masked virtual entries,
// honoring the default-allow rule (zero grants = all groups).
func (s *Service) ListGrantedGroups(ctx context.Context, organizationID string) ([]*VirtualModelEntry, error) {
	repo, err := s.groups()
	if err != nil {
		return nil, err
	}
	rows, err := repo.ListGrantedGroups(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	out := make([]*VirtualModelEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, &VirtualModelEntry{
			ID:          row.ID,
			Name:        row.Name,
			Description: row.Description,
		})
	}
	return out, nil
}

// groupToProto maps a group row to its proto projection.
func groupToProto(g *Group, candidateCount, ruleCount, requestCount24h int64) *routingv1.RoutingGroup {
	return &routingv1.RoutingGroup{
		GroupId:          g.ID,
		Name:             g.Name,
		Description:      g.Description,
		Strategy:         g.Strategy,
		Enabled:          g.Enabled,
		CandidateCount:   candidateCount,
		RuleCount:        ruleCount,
		RequestCount_24H: requestCount24h, //nolint:staticcheck // generated field name.
		CreatedAt:        g.CreatedAt.Unix(),
		UpdatedAt:        g.UpdatedAt.Unix(),
	}
}

// candidatesToProto maps candidate rows to their proto projection.
func candidatesToProto(rows []*Candidate) []*routingv1.RoutingGroupCandidate {
	out := make([]*routingv1.RoutingGroupCandidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, &routingv1.RoutingGroupCandidate{
			CandidateId: row.ID,
			ModelId:     row.ModelID,
			ModelName:   row.ModelName,
			Weight:      int32(row.Weight),      //nolint:gosec // weight is capped at 100.
			CostTier:    int32(row.CostTier),    //nolint:gosec // tier is capped at 5.
			LatencyTier: int32(row.LatencyTier), //nolint:gosec // tier is capped at 5.
			Pinned:      row.Pinned,
			Position:    int32(row.Position), //nolint:gosec // position is capped at 20.
		})
	}
	return out
}

// rulesToProto maps rule rows to their proto projection.
func rulesToProto(rows []*Rule) []*routingv1.RoutingGroupRule {
	out := make([]*routingv1.RoutingGroupRule, 0, len(rows))
	for _, row := range rows {
		out = append(out, &routingv1.RoutingGroupRule{
			RuleId:          row.ID,
			Name:            row.Name,
			MatchType:       row.MatchType,
			Pattern:         row.Pattern,
			TargetModelId:   row.TargetModelID,
			TargetModelName: row.TargetModelName,
			Enabled:         row.Enabled,
			Position:        int32(row.Position), //nolint:gosec // position is capped at 50.
		})
	}
	return out
}

// decisionToProto maps a decision row to its proto projection.
func decisionToProto(row *Decision) *routingv1.RoutingDecision {
	candidates := make([]*routingv1.DecisionCandidate, 0, len(row.Candidates))
	for _, c := range row.Candidates {
		candidates = append(candidates, &routingv1.DecisionCandidate{
			ModelId:   c.ModelID,
			ModelName: c.ModelName,
			Outcome:   c.Outcome,
			Detail:    c.Detail,
		})
	}
	return &routingv1.RoutingDecision{
		DecisionId:           row.ID,
		RequestId:            row.RequestID,
		GroupId:              row.GroupID,
		GroupName:            row.GroupName,
		Strategy:             row.Strategy,
		MatchedRuleId:        row.MatchedRuleID,
		MatchedRuleName:      row.MatchedRuleName,
		SelectedModelId:      row.SelectedModelID,
		SelectedModelName:    row.SelectedModelName,
		CandidatesConsidered: candidates,
		ApiKeyId:             row.APIKeyID,
		OrganizationId:       row.OrganizationID,
		CreatedAt:            row.CreatedAt.Unix(),
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
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return offset, limit
}

// okResponse is the standard success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}

// latencyWindow returns the configured observed-latency window.
func latencyWindow() time.Duration {
	cfg := config.GetConfig()
	if cfg.Routing.LatencyWindow > 0 {
		return cfg.Routing.LatencyWindow
	}
	return 15 * time.Minute
}
