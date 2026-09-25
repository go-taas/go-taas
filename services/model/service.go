// Package model implements the model registry service: registering
// models and their versions, and serving registry queries to the rest of
// the control plane.
package model

import (
	"context"
	"math"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/tenancy"
)

// ServiceName is the unique name of this service.
const ServiceName = "model"

// Pagination bounds for ListModels.
const (
	listDefaultLimit = 20
	listMaxLimit     = 100
)

// organizationMetadataKey is the gRPC metadata key carrying the
// transitional caller organization (set by the gateway from the
// X-Organization-Id header).
const organizationMetadataKey = "x-organization-id"

// grantedByPlaceholder is recorded as granted_by when no caller identity
// can be resolved at all (AD8).
const grantedByPlaceholder = "unknown"

// SessionResolver resolves the authenticated caller's user id
// (feature-13, AD8). It is implemented by the auth module (which owns the
// session store) and injected at wiring time, following the
// tenancy.SessionResolver pattern. Nil until wired: the grant records the
// transitional caller organization instead and still succeeds.
type SessionResolver interface {
	// SessionUserID returns the authenticated caller's user id, or an
	// error when there is no valid session.
	SessionUserID(ctx context.Context) (string, error)
}

// SessionOrgResolver resolves the session's active organization
// (feature-17 AD6). It is implemented by the auth module and injected at
// wiring time. Nil until wired: the transitional X-Organization-Id
// header is used.
type SessionOrgResolver interface {
	// SessionActiveOrg returns the session's active organization, or
	// ("", nil) when no session is present (transitional access).
	SessionActiveOrg(ctx context.Context) (string, error)
}

// Service implements the model registry gRPC service.
type Service struct {
	modelv1.UnimplementedModelServiceServer

	components server.Components
	repo       *Repository
	// deleteGuard optionally blocks DeleteModel while a non-terminated
	// inference service references the model (AC3). The infer module
	// depends on model, so the guard is injected at wiring time by the
	// composing layer (apps/taas-server) rather than imported here.
	deleteGuard DeleteGuard

	// orgGuard validates the organization a grant names (feature-13,
	// AC1). Nil until wired: unit tests skip validation; main.go and FVT
	// always wire it.
	orgGuard *tenancy.OrgGuard

	// sessionResolver resolves the granting caller's user id for the
	// granted_by audit column (feature-13, AD8). Nil until wired: the
	// transitional caller organization is recorded instead.
	sessionResolver SessionResolver

	// sessionOrgResolver resolves the session's active organization for
	// the user-realm catalog (feature-17 AD6). Nil until wired: the
	// transitional X-Organization-Id header is used.
	sessionOrgResolver SessionOrgResolver

	// auditRecorder is the best-effort audit recorder (feature #15, AD3).
	// Nil until wired: no audit events are produced.
	auditRecorder AuditRecorder
}

// AuditRecorder is the best-effort, non-fatal audit recorder seam
// (feature #15, AD3). It is implemented by the audit module and injected
// at wiring time.
type AuditRecorder interface {
	// Record writes one audit event best-effort; it never returns an
	// error.
	Record(ctx context.Context, ev *audit.AuditEvent)
}

// DeleteGuard blocks the deletion of a model. It returns a non-nil
// error (typically 10101 with a detail naming the blocking inference
// service) when the model is still referenced.
type DeleteGuard func(ctx context.Context, modelID string) error

// New constructs the model registry service. The repository is wired
// lazily on first use from the shared components (the database component
// is initialized by server Init, which runs after service construction).
func New(components server.Components) *Service {
	return &Service{components: components}
}

// NewWithRepository constructs a model service bound directly to a
// repository. It is the injection point used by tests and by any
// embedding that bypasses the shared components.
func NewWithRepository(repo *Repository) *Service {
	return &Service{repo: repo}
}

// SetDeleteGuard installs the delete-model reference guard (AC3). It
// must be called before the service starts serving.
func (s *Service) SetDeleteGuard(guard DeleteGuard) {
	s.deleteGuard = guard
}

// SetOrgGuard injects the tenancy read guard used to validate the
// organization a grant names (the SetDeleteGuard pattern). Production and
// FVT wire it; unit tests leave it nil so checkOrg no-ops.
func (s *Service) SetOrgGuard(g *tenancy.OrgGuard) { s.orgGuard = g }

// SetSessionResolver injects the caller-identity resolver used to fill
// the granted_by audit column (feature-13, AD8). Production and FVT wire
// the auth service; unit tests may inject a fake.
func (s *Service) SetSessionResolver(r SessionResolver) { s.sessionResolver = r }

// SetSessionOrgResolver injects the session-organization resolver used
// by the user-realm catalog (feature-17 AD6). Production and FVT wire
// the auth service; unit tests may inject a fake.
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetAuditRecorder injects the best-effort audit recorder (feature #15,
// AD3). Production wires the audit module; unit tests may inject a fake.
func (s *Service) SetAuditRecorder(r AuditRecorder) { s.auditRecorder = r }

// recordAudit writes one audit event best-effort (feature #15, AD3). A
// recorder failure is logged and never fails or rolls back the mutation.
func (s *Service) recordAudit(ctx context.Context, ev *audit.AuditEvent) {
	if s.auditRecorder == nil {
		return
	}
	s.auditRecorder.Record(ctx, ev)
}

// checkOrg validates that the organization exists (10005 when unknown).
// No-op when the guard is not wired.
func (s *Service) checkOrg(ctx context.Context, orgID string) error {
	if s.orgGuard == nil {
		return nil
	}
	return s.orgGuard.RequireExists(ctx, orgID)
}

// grantedBy resolves the granted_by audit value of a grant (AD8): the
// authenticated caller's user id when a session is resolvable, otherwise
// the transitional caller organization, otherwise a placeholder. A grant
// is a platform-level act and the column is audit metadata, so an
// unresolvable caller never fails the call.
func (s *Service) grantedBy(ctx context.Context) string {
	if s.sessionResolver != nil {
		if userID, err := s.sessionResolver.SessionUserID(ctx); err == nil && userID != "" {
			return userID
		}
	}
	if orgID := callerOrgID(ctx); orgID != "" {
		return orgID
	}
	return grantedByPlaceholder
}

// callerOrgID reads the optional transitional caller organization from
// the x-organization-id gRPC metadata. Grant and revoke are
// platform-global, so the header is optional here and only feeds the
// granted_by fallback.
func callerOrgID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get(organizationMetadataKey)
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

// NewForFVT constructs a model service bound to a caller-provided GORM
// database. It exists so full-verification tests can wire the real
// service stack against a disposable database.
func NewForFVT(db *gorm.DB) *Service {
	return &Service{repo: NewRepository(db)}
}

// MigrateSchemaForFVT applies the model schema (models, model_versions,
// model_authorizations) onto a caller-provided database for
// full-verification tests.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&Model{}, &Version{}, &Authorization{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	modelv1.RegisterModelServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return modelv1.RegisterModelServiceHandler
}

// Migrate implements server.Migrator: it creates/updates the models,
// model_versions and model_authorizations tables via GORM AutoMigrate.
// The GORM models are the single source of truth for the schema.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&Model{}, &Version{}, &Authorization{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "model: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "model: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the model repository.
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

// RegisterModel registers a new model with its first version, or appends
// a version to an existing model. The response carries the model id,
// stable across versions.
func (s *Service) RegisterModel(ctx context.Context, req *modelv1.RegisterModelRequest) (*modelv1.RegisterModelResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(req.GetName())
	version := strings.TrimSpace(req.GetVersion())
	weightPath := strings.TrimSpace(req.GetWeightPath())
	description := strings.TrimSpace(req.GetDescription())

	if name == "" || len(name) > 128 {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: name must be 1-128 characters")
	}
	if version == "" || len(version) > 64 {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: version must be 1-64 characters")
	}
	if len(description) > 1024 {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: description must be at most 1024 characters")
	}
	if err := validateWeightPath(weightPath); err != nil {
		return nil, err
	}

	modelID, err := repo.RegisterModelOrCreateVersion(ctx, name, description, version, weightPath)
	if err != nil {
		return nil, err
	}
	// Feature #15: record the successful registration best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		ActorUserID:  s.grantedBy(ctx),
		ActorType:    "user",
		Action:       "model.create",
		ResourceType: "model",
		ResourceID:   modelID,
		Result:       "success",
	})
	return &modelv1.RegisterModelResponse{
		Response: okResponse(),
		ModelId:  modelID,
	}, nil
}

// validateWeightPath enforces the weight-path syntax rules (FR1.3):
// non-empty, at most 512 chars, no ".." segments, no leading "/", no
// backslashes. Existence in object storage is the controller's job.
func validateWeightPath(path string) error {
	if path == "" || len(path) > 512 {
		return apierrors.Newf(apierrors.CodeModelPathInvalid, "model: weight path must be 1-512 characters")
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return apierrors.Newf(apierrors.CodeModelPathInvalid, "model: weight path must be a relative path without backslashes")
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." {
			return apierrors.Newf(apierrors.CodeModelPathInvalid, "model: weight path must not contain '..' segments")
		}
	}
	return nil
}

// ListModels returns one page of the catalog, newest first, with each
// row's latest version derived at read time. An organization_id filter
// applies the default-allow rule (AC11) so the deploy form can list only
// the models the organization may use; restricted is always populated so
// the catalog can render the badge (AC13).
func (s *Service) ListModels(ctx context.Context, req *modelv1.ListModelsRequest) (*modelv1.ListModelsResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}

	offset, limit := normalizePagination(req.GetPage())
	var (
		rows  []*Model
		total int64
	)
	if orgID := strings.TrimSpace(req.GetOrganizationId()); orgID != "" {
		rows, total, err = repo.ListModelsForOrganization(ctx, orgID, offset, limit)
	} else {
		rows, total, err = repo.ListModels(ctx, offset, limit)
	}
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	restricted, err := repo.RestrictedModelIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	models := make([]*modelv1.ModelSummary, 0, len(rows))
	for _, row := range rows {
		summary := &modelv1.ModelSummary{
			ModelId:    row.ID,
			Name:       row.Name,
			CreatedAt:  row.CreatedAt.Unix(),
			Restricted: restricted[row.ID],
		}
		// latest_version and its weight_path come from the first row of
		// the version ordering. A per-row lookup is acceptable at catalog
		// scale; a joined single query is a documented optimization TODO.
		if latest, latestErr := repo.LatestVersion(ctx, row.ID); latestErr == nil && latest != nil {
			summary.LatestVersion = latest.Version
			summary.WeightPath = latest.WeightPath
		}
		models = append(models, summary)
	}
	return &modelv1.ListModelsResponse{
		Response: okResponse(),
		Models:   models,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampToInt32(limit)},
	}, nil
}

// resolveOrg returns the organization context for the user-realm
// catalog (feature-17 AD6): the session's active org when a session is
// present, otherwise the transitional X-Organization-Id header. 10001
// when neither is available.
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
// unauthorized.
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

// ListAvailableModels returns the masked user-realm catalog (feature-17
// AD8/AD11): the models the caller's organization may use, under
// feature-13's default-allow rule. The projection carries no weight_path,
// version list or grant rows.
func (s *Service) ListAvailableModels(ctx context.Context, req *modelv1.ListAvailableModelsRequest) (*modelv1.ListAvailableModelsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListModelsForOrganization(ctx, orgID, offset, limit)
	if err != nil {
		return nil, err
	}
	models := make([]*modelv1.AvailableModel, 0, len(rows))
	for _, row := range rows {
		summary := &modelv1.AvailableModel{
			ModelId: row.ID,
			Name:    row.Name,
		}
		if latest, latestErr := repo.LatestVersion(ctx, row.ID); latestErr == nil && latest != nil {
			summary.LatestVersion = latest.Version
		}
		models = append(models, summary)
	}
	return &modelv1.ListAvailableModelsResponse{
		Response: okResponse(),
		Models:   models,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampToInt32(limit)},
	}, nil
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

// GetModel returns one model with its full, ordered version list.
func (s *Service) GetModel(ctx context.Context, req *modelv1.GetModelRequest) (*modelv1.GetModelResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if req.GetModelId() == "" {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}

	m, err := repo.GetModel(ctx, req.GetModelId())
	if err != nil {
		return nil, err
	}
	versions, err := repo.ListVersionsByModel(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	restricted, err := repo.CountAuthorizations(ctx, m.ID)
	if err != nil {
		return nil, err
	}

	summary := &modelv1.ModelSummary{
		ModelId:    m.ID,
		Name:       m.Name,
		CreatedAt:  m.CreatedAt.Unix(),
		Restricted: restricted > 0,
	}
	versionStrings := make([]string, 0, len(versions))
	for i, v := range versions {
		versionStrings = append(versionStrings, v.Version)
		if i == 0 {
			summary.LatestVersion = v.Version
			summary.WeightPath = v.WeightPath
		}
	}
	return &modelv1.GetModelResponse{
		Response: okResponse(),
		Model:    summary,
		Versions: versionStrings,
	}, nil
}

// DeleteModel removes a model from the registry. It is blocked while a
// non-terminated inference service references the model: the injected
// guard returns 10101 with a detail naming the blocking service (AC3).
func (s *Service) DeleteModel(ctx context.Context, req *modelv1.DeleteModelRequest) (*modelv1.DeleteModelResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if req.GetModelId() == "" {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}

	if _, err := repo.GetModel(ctx, req.GetModelId()); err != nil {
		return nil, err
	}
	if s.deleteGuard != nil {
		if err := s.deleteGuard(ctx, req.GetModelId()); err != nil {
			return nil, err
		}
	}
	if err := repo.DeleteModel(ctx, req.GetModelId()); err != nil {
		return nil, err
	}
	// Feature #15: record the successful deletion best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		ActorUserID:  s.grantedBy(ctx),
		ActorType:    "user",
		Action:       "model.delete",
		ResourceType: "model",
		ResourceID:   req.GetModelId(),
		Result:       "success",
	})
	return &modelv1.DeleteModelResponse{Response: okResponse()}, nil
}

// GrantModelAccess adds an organization to the model's grant list,
// recording the granting caller and the grant time. The first grant flips
// the model from default-allow to restricted (AC1/AC3); repeating it is
// an idempotent no-op (AC2). Unknown model → 10101, unknown organization
// → 10005. The RPC is platform-global: every organization's grants are
// managed from one admin console, so it takes no org context.
func (s *Service) GrantModelAccess(ctx context.Context, req *modelv1.GrantModelAccessRequest) (*modelv1.GrantModelAccessResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetModelId()) == "" {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}
	if _, err := repo.GetModel(ctx, req.GetModelId()); err != nil {
		return nil, err
	}
	orgID := strings.TrimSpace(req.GetOrganizationId())
	if orgID == "" {
		return nil, apierrors.New(apierrors.CodeOrganizationNotFound)
	}
	if err := s.checkOrg(ctx, orgID); err != nil {
		return nil, err
	}

	if err := repo.GrantAccess(ctx, req.GetModelId(), orgID, s.grantedBy(ctx)); err != nil {
		return nil, err
	}
	// Feature #15: record the successful grant best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    s.grantedBy(ctx),
		ActorType:      "user",
		Action:         "model.grant",
		ResourceType:   "model",
		ResourceID:     req.GetModelId(),
		Result:         "success",
	})
	return &modelv1.GrantModelAccessResponse{Response: okResponse()}, nil
}

// RevokeModelAccess removes an organization from the model's grant list.
// Revoking a grant that does not exist is an idempotent no-op (AC4), and
// revoking the last grant returns the model to default-allow (AC3). The
// organization is not required to exist: a grant left behind by a
// removed organization must still be revocable.
func (s *Service) RevokeModelAccess(ctx context.Context, req *modelv1.RevokeModelAccessRequest) (*modelv1.RevokeModelAccessResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetModelId()) == "" {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}
	if _, err := repo.GetModel(ctx, req.GetModelId()); err != nil {
		return nil, err
	}
	orgID := strings.TrimSpace(req.GetOrganizationId())
	if orgID == "" {
		return nil, apierrors.New(apierrors.CodeOrganizationNotFound)
	}

	if err := repo.RevokeAccess(ctx, req.GetModelId(), orgID); err != nil {
		return nil, err
	}
	// Feature #15: record the successful revoke best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    s.grantedBy(ctx),
		ActorType:      "user",
		Action:         "model.revoke",
		ResourceType:   "model",
		ResourceID:     req.GetModelId(),
		Result:         "success",
	})
	return &modelv1.RevokeModelAccessResponse{Response: okResponse()}, nil
}

// ListModelAuthorizations returns one page of the model's grant rows,
// newest first (AC10). Unknown model → 10101.
func (s *Service) ListModelAuthorizations(ctx context.Context, req *modelv1.ListModelAuthorizationsRequest) (*modelv1.ListModelAuthorizationsResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetModelId()) == "" {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}
	if _, err := repo.GetModel(ctx, req.GetModelId()); err != nil {
		return nil, err
	}

	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListAuthorizations(ctx, req.GetModelId(), offset, limit)
	if err != nil {
		return nil, err
	}

	authorizations := make([]*modelv1.ModelAuthorization, 0, len(rows))
	for _, row := range rows {
		authorizations = append(authorizations, &modelv1.ModelAuthorization{
			OrganizationId: row.OrganizationID,
			GrantedBy:      row.GrantedBy,
			CreatedAt:      row.CreatedAt.Unix(),
		})
	}
	return &modelv1.ListModelAuthorizationsResponse{
		Response:       okResponse(),
		Authorizations: authorizations,
		PageMeta:       &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampToInt32(limit)},
	}, nil
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

// okResponse builds the success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}
