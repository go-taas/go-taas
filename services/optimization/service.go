package optimization

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	optimizationv1 "github.com/go-taas/go-taas/proto/taas/optimization/v1"
)

// ServiceName is the service registration name.
const ServiceName = "optimization"

// roleAdmin is the minimum org role on the admin RPCs.
const roleAdmin = "admin"

// SessionOrgResolver resolves the session's active organization.
type SessionOrgResolver interface {
	SessionActiveOrg(ctx context.Context) (string, error)
}

// SessionUserResolver resolves the authenticated caller's user id.
type SessionUserResolver interface {
	SessionUserID(ctx context.Context) (string, error)
}

// RoleGuard enforces the minimum org role on the admin RPCs.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// AuditRecorder records optimization mutations best-effort.
type AuditRecorder interface {
	Record(ctx context.Context, ev *audit.AuditEvent)
}

// Service implements the optimization gRPC service (feature #50): the
// admin worklist RPCs under /api/v1/admin/optimization* and the
// org-scoped masked user RPCs under /api/v1/optimization*.
type Service struct {
	optimizationv1.UnimplementedOptimizationServiceServer

	components server.Components

	db     *gorm.DB
	repo   *Repository
	engine *Engine

	sessionOrgResolver  SessionOrgResolver
	sessionUserResolver SessionUserResolver
	roleGuard           RoleGuard
	auditRecorder       AuditRecorder
}

// New constructs the optimization service.
func New(components server.Components) *Service {
	return &Service{components: components}
}

// NewForFVT constructs an optimization service bound to a
// caller-provided GORM database for full-verification tests.
func NewForFVT(db *gorm.DB) *Service {
	return &Service{db: db, repo: NewRepository(db)}
}

// MigrateSchemaForFVT applies the optimization schema onto a
// caller-provided database.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&Recommendation{}, &Dismissal{}, &Run{})
}

// SetSessionOrgResolver injects the session-organization resolver.
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver.
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard.
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// SetAuditRecorder injects the best-effort audit recorder.
func (s *Service) SetAuditRecorder(r AuditRecorder) { s.auditRecorder = r }

// SetCardTypeLister injects the accelerator card-type seam into the
// engine (AD14).
func (s *Service) SetCardTypeLister(l CardTypeLister) {
	if e := s.ensureEngine(); e != nil {
		e.SetCardTypeLister(l)
	}
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	optimizationv1.RegisterOptimizationServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return optimizationv1.RegisterOptimizationServiceHandler
}

// Migrate implements server.Migrator: it creates the three
// optimization tables.
func (s *Service) Migrate(ctx context.Context) error {
	if s.repo != nil {
		return nil
	}
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&Recommendation{}, &Dismissal{}, &Run{})
}

// gormDB resolves the *gorm.DB from the shared components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.db != nil {
		return s.db, nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "optimization: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "optimization: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repositories lazily wires the repository.
func (s *Service) repositories() (*Repository, error) {
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

// ensureEngine lazily wires the engine over the shared handle.
func (s *Service) ensureEngine() *Engine {
	if s.engine != nil {
		return s.engine
	}
	db, err := s.gormDB()
	if err != nil {
		return nil
	}
	s.engine = NewEngine(db, optimizationConfig(), nil)
	return s.engine
}

// optimizationConfig returns the optimization section, falling back to
// the zero config (the engine's own guards handle unset values) when
// no configuration is loaded.
func optimizationConfig() config.OptimizationConfig {
	if cfg := config.GetConfig(); cfg != nil {
		return cfg.Optimization
	}
	return config.OptimizationConfig{}
}

// resolveSessionActor returns the caller's user id and whether a
// session is present.
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

// requireAdminRole enforces the admin/owner role on the admin-surface
// RPCs (the routing pattern): resolve the active org, resolve the
// actor, RequireRole(admin). A missing session context is NOT
// authorized — the admin realm guard is the outer boundary, this is
// the inner one.
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

// actor returns the acting user id for audit, or "system" when
// unknown.
func (s *Service) actor(ctx context.Context) string {
	userID, ok, _ := s.resolveSessionActor(ctx)
	if ok {
		return userID
	}
	return "system"
}

// recordAudit records one best-effort audit event.
func (s *Service) recordAudit(ctx context.Context, action, resourceID string, metadata map[string]string) {
	if s.auditRecorder == nil {
		return
	}
	ev := &audit.AuditEvent{
		ActorUserID:  s.actor(ctx),
		ActorType:    "user",
		Action:       action,
		ResourceType: "optimization_recommendation",
		ResourceID:   resourceID,
		Result:       "success",
	}
	if metadata != nil {
		if b, err := json.Marshal(metadata); err == nil {
			ev.Metadata = string(b)
		}
	}
	s.auditRecorder.Record(ctx, ev)
}

// ---- admin surface ----

// GetOptimizationOverview returns the fleet summary cards (FR2.2).
func (s *Service) GetOptimizationOverview(ctx context.Context, _ *optimizationv1.GetOptimizationOverviewRequest) (*optimizationv1.GetOptimizationOverviewResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repositories()
	if err != nil {
		return nil, err
	}
	overview, err := repo.Overview(ctx, SurfaceAdmin, "")
	if err != nil {
		return nil, err
	}
	return &optimizationv1.GetOptimizationOverviewResponse{
		Response: okResponse(),
		Overview: overviewToProto(overview),
	}, nil
}

// ListOptimizationRecommendations returns the ranked, filtered
// worklist (FR2.1).
func (s *Service) ListOptimizationRecommendations(ctx context.Context, req *optimizationv1.ListOptimizationRecommendationsRequest) (*optimizationv1.ListOptimizationRecommendationsResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repositories()
	if err != nil {
		return nil, err
	}
	filter, err := parseListFilters(req.GetRule(), req.GetStatus(), req.GetOrganizationId(), req.GetModelId(), req.GetMinSaving())
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePage(req.GetPage())
	filter.Offset, filter.Limit = offset, limit
	rows, total, err := repo.List(ctx, filter, SurfaceAdmin, "")
	if err != nil {
		return nil, err
	}
	out := make([]*optimizationv1.Recommendation, 0, len(rows))
	for i := range rows {
		out = append(out, project(&rows[i].Recommendation, rows[i].Dismissal, SurfaceAdmin))
	}
	return &optimizationv1.ListOptimizationRecommendationsResponse{
		Response:        okResponse(),
		Recommendations: out,
		PageMeta:        pageMeta(total, offset, limit),
	}, nil
}

// GetOptimizationRecommendation returns one recommendation (FR2.3).
func (s *Service) GetOptimizationRecommendation(ctx context.Context, req *optimizationv1.GetOptimizationRecommendationRequest) (*optimizationv1.GetOptimizationRecommendationResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repositories()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetByID(ctx, req.GetRecommendationId())
	if err != nil {
		return nil, err
	}
	dismissal, err := repo.dismissalFor(ctx, SurfaceAdmin, row.Fingerprint)
	if err != nil {
		return nil, err
	}
	return &optimizationv1.GetOptimizationRecommendationResponse{
		Response:       okResponse(),
		Recommendation: project(row, dismissal, SurfaceAdmin),
	}, nil
}

// DismissRecommendation records an admin dismissal (FR3.1).
func (s *Service) DismissRecommendation(ctx context.Context, req *optimizationv1.DismissRecommendationRequest) (*optimizationv1.DismissRecommendationResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repositories()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetByID(ctx, req.GetRecommendationId())
	if err != nil {
		return nil, err
	}
	reason, note, err := validateDismissal(req.GetReason(), req.GetNote())
	if err != nil {
		return nil, err
	}
	if err := repo.InsertDismissal(ctx, SurfaceAdmin, row.Fingerprint, reason, note, s.actor(ctx)); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "optimization.recommendation_dismissed", row.ID, map[string]string{
		"rule":    row.Rule,
		"target":  row.TargetDisplay,
		"reason":  reason,
		"note":    note,
		"surface": SurfaceAdmin,
	})
	dismissal, err := repo.dismissalFor(ctx, SurfaceAdmin, row.Fingerprint)
	if err != nil {
		return nil, err
	}
	return &optimizationv1.DismissRecommendationResponse{
		Response:       okResponse(),
		Recommendation: project(row, dismissal, SurfaceAdmin),
	}, nil
}

// RestoreRecommendation undoes an admin dismissal (FR3.2).
func (s *Service) RestoreRecommendation(ctx context.Context, req *optimizationv1.RestoreRecommendationRequest) (*optimizationv1.RestoreRecommendationResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repositories()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetByID(ctx, req.GetRecommendationId())
	if err != nil {
		return nil, err
	}
	if err := repo.DeleteDismissal(ctx, SurfaceAdmin, row.Fingerprint); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "optimization.recommendation_restored", row.ID, map[string]string{
		"rule":    row.Rule,
		"target":  row.TargetDisplay,
		"surface": SurfaceAdmin,
	})
	return &optimizationv1.RestoreRecommendationResponse{
		Response:       okResponse(),
		Recommendation: project(row, nil, SurfaceAdmin),
	}, nil
}

// RefreshOptimization triggers an immediate engine run (FR2.4).
func (s *Service) RefreshOptimization(ctx context.Context, _ *optimizationv1.RefreshOptimizationRequest) (*optimizationv1.RefreshOptimizationResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repositories()
	if err != nil {
		return nil, err
	}
	engine := s.ensureEngine()
	if engine == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "optimization: engine unavailable")
	}
	started := time.Now().UTC()
	emitted, err := engine.Run(ctx, TriggerAdmin)
	if err != nil {
		engine.RunFailure(ctx, TriggerAdmin, started, err)
		return nil, err
	}
	s.recordAudit(ctx, "optimization.refreshed", "", map[string]string{
		"emitted":     strconv.Itoa(emitted),
		"duration_ms": strconv.FormatInt(time.Since(started).Milliseconds(), 10),
		"trigger":     TriggerAdmin,
	})
	overview, err := repo.Overview(ctx, SurfaceAdmin, "")
	if err != nil {
		return nil, err
	}
	return &optimizationv1.RefreshOptimizationResponse{
		Response: okResponse(),
		Overview: overviewToProto(overview),
	}, nil
}

// ---- user surface (org-scoped, masked: R1/R2/R4 only) ----

// resolveOrg resolves the caller's active organization (the user
// surface's scope).
func (s *Service) resolveOrg(ctx context.Context) (string, error) {
	if s.sessionOrgResolver == nil {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	orgID, err := s.sessionOrgResolver.SessionActiveOrg(ctx)
	if err != nil {
		return "", err
	}
	if orgID == "" {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	return orgID, nil
}

// GetMyOptimization returns the caller org's summary and first page
// (FR4.1).
func (s *Service) GetMyOptimization(ctx context.Context, _ *optimizationv1.GetMyOptimizationRequest) (*optimizationv1.GetMyOptimizationResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repositories()
	if err != nil {
		return nil, err
	}
	overview, err := repo.Overview(ctx, SurfaceUser, orgID)
	if err != nil {
		return nil, err
	}
	rows, _, err := repo.List(ctx, Filter{Limit: 20}, SurfaceUser, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]*optimizationv1.Recommendation, 0, len(rows))
	for i := range rows {
		out = append(out, project(&rows[i].Recommendation, rows[i].Dismissal, SurfaceUser))
	}
	return &optimizationv1.GetMyOptimizationResponse{
		Response:        okResponse(),
		Overview:        overviewToProto(overview),
		Recommendations: out,
	}, nil
}

// ListMyOptimizationRecommendations returns the caller org's filtered
// worklist (FR4.2).
func (s *Service) ListMyOptimizationRecommendations(ctx context.Context, req *optimizationv1.ListMyOptimizationRecommendationsRequest) (*optimizationv1.ListMyOptimizationRecommendationsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repositories()
	if err != nil {
		return nil, err
	}
	filter, err := parseListFilters(req.GetRule(), req.GetStatus(), "", "", req.GetMinSaving())
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePage(req.GetPage())
	filter.Offset, filter.Limit = offset, limit
	rows, total, err := repo.List(ctx, filter, SurfaceUser, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]*optimizationv1.Recommendation, 0, len(rows))
	for i := range rows {
		out = append(out, project(&rows[i].Recommendation, rows[i].Dismissal, SurfaceUser))
	}
	return &optimizationv1.ListMyOptimizationRecommendationsResponse{
		Response:        okResponse(),
		Recommendations: out,
		PageMeta:        pageMeta(total, offset, limit),
	}, nil
}

// GetMyOptimizationRecommendation returns one of the caller org's
// recommendations; a foreign org's or an R3 row is 13201 (AC9).
func (s *Service) GetMyOptimizationRecommendation(ctx context.Context, req *optimizationv1.GetMyOptimizationRecommendationRequest) (*optimizationv1.GetMyOptimizationRecommendationResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repositories()
	if err != nil {
		return nil, err
	}
	row, err := s.loadScoped(ctx, repo, req.GetRecommendationId(), orgID)
	if err != nil {
		return nil, err
	}
	dismissal, err := repo.dismissalFor(ctx, SurfaceUser, row.Fingerprint)
	if err != nil {
		return nil, err
	}
	return &optimizationv1.GetMyOptimizationRecommendationResponse{
		Response:       okResponse(),
		Recommendation: project(row, dismissal, SurfaceUser),
	}, nil
}

// DismissMyRecommendation records the tenant's own dismissal (FR3.4).
func (s *Service) DismissMyRecommendation(ctx context.Context, req *optimizationv1.DismissMyRecommendationRequest) (*optimizationv1.DismissMyRecommendationResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repositories()
	if err != nil {
		return nil, err
	}
	row, err := s.loadScoped(ctx, repo, req.GetRecommendationId(), orgID)
	if err != nil {
		return nil, err
	}
	reason, note, err := validateDismissal(req.GetReason(), req.GetNote())
	if err != nil {
		return nil, err
	}
	if err := repo.InsertDismissal(ctx, SurfaceUser, row.Fingerprint, reason, note, s.actor(ctx)); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "optimization.recommendation_dismissed", row.ID, map[string]string{
		"rule":    row.Rule,
		"target":  row.TargetDisplay,
		"reason":  reason,
		"note":    note,
		"surface": SurfaceUser,
	})
	dismissal, err := repo.dismissalFor(ctx, SurfaceUser, row.Fingerprint)
	if err != nil {
		return nil, err
	}
	return &optimizationv1.DismissMyRecommendationResponse{
		Response:       okResponse(),
		Recommendation: project(row, dismissal, SurfaceUser),
	}, nil
}

// RestoreMyRecommendation undoes the tenant's own dismissal (FR3.4).
func (s *Service) RestoreMyRecommendation(ctx context.Context, req *optimizationv1.RestoreMyRecommendationRequest) (*optimizationv1.RestoreMyRecommendationResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repositories()
	if err != nil {
		return nil, err
	}
	row, err := s.loadScoped(ctx, repo, req.GetRecommendationId(), orgID)
	if err != nil {
		return nil, err
	}
	if err := repo.DeleteDismissal(ctx, SurfaceUser, row.Fingerprint); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "optimization.recommendation_restored", row.ID, map[string]string{
		"rule":    row.Rule,
		"target":  row.TargetDisplay,
		"surface": SurfaceUser,
	})
	return &optimizationv1.RestoreMyRecommendationResponse{
		Response:       okResponse(),
		Recommendation: project(row, nil, SurfaceUser),
	}, nil
}

// loadScoped loads a recommendation enforcing the user surface's
// scope: the row must belong to the caller's org and must not be R3
// (AC9); anything else is 13201.
func (s *Service) loadScoped(ctx context.Context, repo *Repository, id, orgID string) (*Recommendation, error) {
	row, err := repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row.OrganizationID != orgID || row.Rule == RuleIdleService {
		return nil, apierrors.New(apierrors.CodeRecommendationNotFound)
	}
	return row, nil
}

// ---- helpers ----

// parseListFilters validates and normalizes the shared list filters
// (§5.4; invalid values are 13205).
func parseListFilters(rule, status, orgID, modelID, minSaving string) (Filter, error) {
	filter := Filter{
		Rule:    strings.TrimSpace(rule),
		Status:  strings.TrimSpace(status),
		OrgID:   strings.TrimSpace(orgID),
		ModelID: strings.TrimSpace(modelID),
	}
	if filter.Rule != "" {
		valid := false
		for _, r := range Rules {
			if filter.Rule == r {
				valid = true
				break
			}
		}
		if !valid {
			return filter, apierrors.New(apierrors.CodeOptimizationRangeInvalid)
		}
	}
	switch filter.Status {
	case "", StatusActive, StatusDismissed, StatusAll:
	default:
		return filter, apierrors.New(apierrors.CodeOptimizationRangeInvalid)
	}
	if minSaving != "" {
		v, err := strconv.ParseFloat(strings.TrimSpace(minSaving), 64)
		if err != nil || v < 0 {
			return filter, apierrors.New(apierrors.CodeOptimizationRangeInvalid)
		}
		filter.MinSaving = v
	}
	return filter, nil
}

// validateDismissal checks the reason/note shape (§5.4; 13203).
func validateDismissal(reason, note string) (string, string, error) {
	reason = strings.TrimSpace(reason)
	note = strings.TrimSpace(note)
	valid := false
	for _, r := range DismissReasons {
		if reason == r {
			valid = true
			break
		}
	}
	if !valid {
		return "", "", apierrors.New(apierrors.CodeRecommendationDismissReasonInvalid)
	}
	if len(note) > maxDismissNoteLen {
		return "", "", apierrors.New(apierrors.CodeRecommendationDismissReasonInvalid)
	}
	if reason == ReasonOther && note == "" {
		return "", "", apierrors.New(apierrors.CodeRecommendationDismissReasonInvalid)
	}
	return reason, note, nil
}

// normalizePage clamps the page request.
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

// pageMeta builds the standard page meta.
func pageMeta(total int64, offset, limit int) *commonv1.PageMeta {
	return &commonv1.PageMeta{
		Total:  total,
		Offset: int64(offset),
		Limit:  int32(limit), //nolint:gosec // limit is capped at 100 by normalizePage.
	}
}

// okResponse is the standard success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}

// project maps a row + derived dismissal to the proto projection for
// one surface (AD11/AD16): status and dismissed_* from the dismissal,
// acting_page from the surface's jsonb column, the saving formatted to
// 2 decimals as a string.
func project(row *Recommendation, dismissal *Dismissal, surface string) *optimizationv1.Recommendation {
	out := &optimizationv1.Recommendation{
		RecommendationId:       row.ID,
		Rule:                   row.Rule,
		TargetType:             row.TargetType,
		TargetId:               row.TargetID,
		TargetDisplay:          row.TargetDisplay,
		Suggestion:             row.Suggestion,
		EstimatedMonthlySaving: formatSaving(row.EstimatedMonthlySaving),
		Currency:               row.Currency,
		InputsJson:             string(row.Inputs),
		Arithmetic:             row.Arithmetic,
		Status:                 StatusActive,
		Fingerprint:            row.Fingerprint,
		FirstSeenAt:            row.FirstSeenAt.Unix(),
		LastComputedAt:         row.LastComputedAt.Unix(),
	}
	if dismissal != nil {
		out.Status = StatusDismissed
		out.DismissedReason = dismissal.Reason
		out.DismissedNote = dismissal.Note
		out.DismissedBy = dismissal.DismissedBy
		out.DismissedAt = dismissal.DismissedAt.Unix()
	}
	// The acting link is the surface's variant (AD16).
	linkJSON := row.ActingPageAdmin
	if surface == SurfaceUser {
		linkJSON = row.ActingPageUser
	}
	var link ActingLink
	if len(linkJSON) > 0 {
		_ = json.Unmarshal(linkJSON, &link)
	}
	out.ActingPage = &optimizationv1.ActingPage{Route: link.Route, Label: link.Label}
	// R1's alternatives.
	if len(row.Alternatives) > 0 && string(row.Alternatives) != "[]" {
		var entries []AlternativeEntry
		if err := json.Unmarshal(row.Alternatives, &entries); err == nil {
			for _, e := range entries {
				out.Alternatives = append(out.Alternatives, &optimizationv1.AlternativeModel{
					ModelId:                e.ModelID,
					ModelName:              e.ModelName,
					EstimatedMonthlySaving: formatSaving(e.EstimatedMonthlySaving),
					InputPricePerMillion:   formatPrice2(e.InputPricePerMillion),
					OutputPricePerMillion:  formatPrice2(e.OutputPricePerMillion),
				})
			}
		}
	}
	return out
}

// overviewToProto maps the aggregate row to the proto overview.
func overviewToProto(row *OverviewRow) *optimizationv1.OptimizationOverview {
	out := &optimizationv1.OptimizationOverview{
		TotalEstimatedMonthlySaving: formatSaving(row.TotalSaving),
		ActiveCount:                 row.ActiveCount,
		DismissedCount:              row.DismissedCount,
	}
	for _, rule := range Rules {
		if count, ok := row.CountByRule[rule]; ok {
			out.CountByRule = append(out.CountByRule, &optimizationv1.RuleCount{Rule: rule, Count: count})
		}
	}
	if !row.LastRefreshAt.IsZero() {
		out.LastRefreshAt = row.LastRefreshAt.Unix()
	}
	if row.TopOrgID != "" {
		out.TopOrganizationId = row.TopOrgID
		out.TopOrganizationName = row.TopOrgName
		out.TopOrganizationSaving = formatSaving(row.TopOrgSaving)
	}
	return out
}

// formatSaving renders a money value with exactly 2 decimals.
func formatSaving(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// formatPrice2 renders a per-million rate with 2 decimals.
func formatPrice2(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}
