package tenancy

import (
	"context"
	"errors"
	"math"
	"strings"

	"google.golang.org/grpc"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/server"
)

// ServiceName is the unique name of this service.
const ServiceName = "tenancy"

// Pagination bounds (architecture Section 4.2).
const (
	listDefaultLimit = 20
	listMaxLimit     = 100
)

// Service implements the tenancy gRPC service: the organization and
// project directory. It is platform-global — no X-Organization-Id
// header is required or consumed (D8).
type Service struct {
	tenancyv1.UnimplementedTenancyServiceServer

	components server.Components

	// repo is the injection point used by tests and FVT; production
	// resolves it lazily from the shared components.
	repo *Repository
}

// New constructs the tenancy service. The repository is wired lazily
// on first use from the shared components (the database component is
// initialized by server Init, which runs after service construction).
func New(components server.Components) *Service {
	return &Service{components: components}
}

// NewForFVT constructs a tenancy service bound to a caller-provided
// GORM database. It exists so full-verification tests can wire the
// real service stack against a disposable database.
func NewForFVT(db *gorm.DB) *Service {
	return &Service{repo: NewRepository(db)}
}

// MigrateSchemaForFVT applies the tenancy schema (organizations,
// projects) onto a caller-provided database for full-verification
// tests.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&Organization{}, &Project{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	tenancyv1.RegisterTenancyServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return tenancyv1.RegisterTenancyServiceHandler
}

// Migrate implements server.Migrator: it creates/updates the
// organizations and projects tables via GORM AutoMigrate and runs the
// first-boot default-organization seed (D5: insert-only, gated by an
// empty table, never re-runs — deletions stick, edits win).
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	if err := db.WithContext(ctx).AutoMigrate(&Organization{}, &Project{}); err != nil {
		return err
	}
	repo, err := s.repository()
	if err != nil {
		return err
	}
	count, err := repo.CountOrganizations(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		logger.S().Infow("tenancy: organizations table non-empty, skipping first-boot seed", "rows", count)
		return nil
	}
	cfg := config.GetConfig()
	if cfg == nil {
		return nil
	}
	if err := repo.SeedDefaultOrganization(ctx, cfg.Tenancy.DefaultOrgID, cfg.Tenancy.DefaultOrgDisplayName); err != nil {
		return apierrors.Newf(apierrors.CodeInternal, "tenancy: first-boot seed failed: %v", err)
	}
	logger.S().Infow("tenancy: first-boot default-organization seed complete",
		"organizationId", cfg.Tenancy.DefaultOrgID)
	return nil
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "tenancy: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "tenancy: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the tenancy repository.
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

// CreateOrganization creates a tenant with a caller-supplied id
// (validation matrix: architecture Section 4.3, AC1).
func (s *Service) CreateOrganization(ctx context.Context, req *tenancyv1.CreateOrganizationRequest) (*tenancyv1.CreateOrganizationResponse, error) {
	id := strings.TrimSpace(req.GetOrganizationId())
	if !config.TenancyIDRegex().MatchString(id) {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	name := strings.TrimSpace(req.GetDisplayName())
	if len(name) < 1 || len(name) > maxDisplayNameLen {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	if len(req.GetDescription()) > maxDescriptionLen {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if _, err := repo.FindOrganization(ctx, id); err == nil {
		return nil, apierrors.New(apierrors.CodeOrganizationExists)
	} else if !isRecordNotFound(err) {
		return nil, err
	}
	org := &Organization{
		ID:          id,
		DisplayName: name,
		Description: req.GetDescription(),
		State:       StateActive,
	}
	if err := repo.CreateOrganization(ctx, org); err != nil {
		return nil, err
	}
	summary, err := s.summarizeOrganization(ctx, repo, org)
	if err != nil {
		return nil, err
	}
	return &tenancyv1.CreateOrganizationResponse{Response: okResponse(), Organization: summary}, nil
}

// ListOrganizations returns the organization directory, newest first,
// with per-organization resource counts (AC2).
func (s *Service) ListOrganizations(ctx context.Context, req *tenancyv1.ListOrganizationsRequest) (*tenancyv1.ListOrganizationsResponse, error) {
	state := req.GetState()
	if state != "" && state != StateActive && state != StateDisabled {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListOrganizations(ctx, OrgFilter{State: state}, offset, limit)
	if err != nil {
		return nil, err
	}
	organizations := make([]*tenancyv1.OrganizationSummary, 0, len(rows))
	for _, row := range rows {
		summary, err := s.summarizeOrganization(ctx, repo, row)
		if err != nil {
			return nil, err
		}
		organizations = append(organizations, summary)
	}
	return &tenancyv1.ListOrganizationsResponse{
		Response:      okResponse(),
		Organizations: organizations,
		PageMeta:      &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// GetOrganization returns one organization; unknown ids return 10005.
func (s *Service) GetOrganization(ctx context.Context, req *tenancyv1.GetOrganizationRequest) (*tenancyv1.GetOrganizationResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	org, err := repo.FindOrganization(ctx, req.GetOrganizationId())
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeOrganizationNotFound)
	}
	if err != nil {
		return nil, err
	}
	summary, err := s.summarizeOrganization(ctx, repo, org)
	if err != nil {
		return nil, err
	}
	return &tenancyv1.GetOrganizationResponse{Response: okResponse(), Organization: summary}, nil
}

// UpdateOrganization edits the display name and description; the id
// and state are immutable (AC3).
func (s *Service) UpdateOrganization(ctx context.Context, req *tenancyv1.UpdateOrganizationRequest) (*tenancyv1.UpdateOrganizationResponse, error) {
	name := strings.TrimSpace(req.GetDisplayName())
	if len(name) < 1 || len(name) > maxDisplayNameLen {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	if len(req.GetDescription()) > maxDescriptionLen {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	org, err := repo.UpdateOrganization(ctx, req.GetOrganizationId(), name, req.GetDescription())
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeOrganizationNotFound)
	}
	if err != nil {
		return nil, err
	}
	summary, err := s.summarizeOrganization(ctx, repo, org)
	if err != nil {
		return nil, err
	}
	return &tenancyv1.UpdateOrganizationResponse{Response: okResponse(), Organization: summary}, nil
}

// DisableOrganization moves an active organization to disabled. It is
// idempotent (AC4).
func (s *Service) DisableOrganization(ctx context.Context, req *tenancyv1.DisableOrganizationRequest) (*tenancyv1.DisableOrganizationResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	org, err := repo.SetOrganizationState(ctx, req.GetOrganizationId(), StateDisabled)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeOrganizationNotFound)
	}
	if err != nil {
		return nil, err
	}
	summary, err := s.summarizeOrganization(ctx, repo, org)
	if err != nil {
		return nil, err
	}
	return &tenancyv1.DisableOrganizationResponse{Response: okResponse(), Organization: summary}, nil
}

// EnableOrganization moves a disabled organization back to active. It
// is idempotent (AC4).
func (s *Service) EnableOrganization(ctx context.Context, req *tenancyv1.EnableOrganizationRequest) (*tenancyv1.EnableOrganizationResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	org, err := repo.SetOrganizationState(ctx, req.GetOrganizationId(), StateActive)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeOrganizationNotFound)
	}
	if err != nil {
		return nil, err
	}
	summary, err := s.summarizeOrganization(ctx, repo, org)
	if err != nil {
		return nil, err
	}
	return &tenancyv1.EnableOrganizationResponse{Response: okResponse(), Organization: summary}, nil
}

// CreateProject creates a project under an organization; the
// organization must exist and be active (validation matrix:
// architecture Section 4.3, AC5).
func (s *Service) CreateProject(ctx context.Context, req *tenancyv1.CreateProjectRequest) (*tenancyv1.CreateProjectResponse, error) {
	id := strings.TrimSpace(req.GetProjectId())
	if !config.TenancyIDRegex().MatchString(id) {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	orgID := strings.TrimSpace(req.GetOrganizationId())
	if orgID == "" {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	name := strings.TrimSpace(req.GetDisplayName())
	if len(name) < 1 || len(name) > maxDisplayNameLen {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	if len(req.GetDescription()) > maxDescriptionLen {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	org, err := repo.FindOrganization(ctx, orgID)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeOrganizationNotFound)
	}
	if err != nil {
		return nil, err
	}
	if org.State != StateActive {
		return nil, apierrors.New(apierrors.CodeOrganizationDisabled)
	}
	if _, err := repo.FindProject(ctx, id); err == nil {
		return nil, apierrors.New(apierrors.CodeProjectExists)
	} else if !isRecordNotFound(err) {
		return nil, err
	}
	project := &Project{
		ID:             id,
		OrganizationID: orgID,
		DisplayName:    name,
		Description:    req.GetDescription(),
		State:          StateActive,
	}
	if err := repo.CreateProject(ctx, project); err != nil {
		return nil, err
	}
	return &tenancyv1.CreateProjectResponse{Response: okResponse(), Project: summarizeProject(project)}, nil
}

// ListProjects returns the project directory, newest first, with
// organization and state filters (AC6).
func (s *Service) ListProjects(ctx context.Context, req *tenancyv1.ListProjectsRequest) (*tenancyv1.ListProjectsResponse, error) {
	state := req.GetState()
	if state != "" && state != StateActive && state != StateDisabled {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListProjects(ctx, ProjectFilter{
		OrganizationID: strings.TrimSpace(req.GetOrganizationId()),
		State:          state,
	}, offset, limit)
	if err != nil {
		return nil, err
	}
	projects := make([]*tenancyv1.ProjectSummary, 0, len(rows))
	for _, row := range rows {
		projects = append(projects, summarizeProject(row))
	}
	return &tenancyv1.ListProjectsResponse{
		Response: okResponse(),
		Projects: projects,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// GetProject returns one project; unknown ids return 10006.
func (s *Service) GetProject(ctx context.Context, req *tenancyv1.GetProjectRequest) (*tenancyv1.GetProjectResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	project, err := repo.FindProject(ctx, req.GetProjectId())
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeProjectNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &tenancyv1.GetProjectResponse{Response: okResponse(), Project: summarizeProject(project)}, nil
}

// UpdateProject edits the display name and description; the id, the
// owning organization, and the state are immutable.
func (s *Service) UpdateProject(ctx context.Context, req *tenancyv1.UpdateProjectRequest) (*tenancyv1.UpdateProjectResponse, error) {
	name := strings.TrimSpace(req.GetDisplayName())
	if len(name) < 1 || len(name) > maxDisplayNameLen {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	if len(req.GetDescription()) > maxDescriptionLen {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	project, err := repo.UpdateProject(ctx, req.GetProjectId(), name, req.GetDescription())
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeProjectNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &tenancyv1.UpdateProjectResponse{Response: okResponse(), Project: summarizeProject(project)}, nil
}

// DisableProject moves an active project to disabled. It is
// idempotent.
func (s *Service) DisableProject(ctx context.Context, req *tenancyv1.DisableProjectRequest) (*tenancyv1.DisableProjectResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	project, err := repo.SetProjectState(ctx, req.GetProjectId(), StateDisabled)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeProjectNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &tenancyv1.DisableProjectResponse{Response: okResponse(), Project: summarizeProject(project)}, nil
}

// EnableProject moves a disabled project back to active. It is blocked
// while the owning organization is disabled (10017, AC7).
func (s *Service) EnableProject(ctx context.Context, req *tenancyv1.EnableProjectRequest) (*tenancyv1.EnableProjectResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	project, err := repo.FindProject(ctx, req.GetProjectId())
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeProjectNotFound)
	}
	if err != nil {
		return nil, err
	}
	org, err := repo.FindOrganization(ctx, project.OrganizationID)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeOrganizationNotFound)
	}
	if err != nil {
		return nil, err
	}
	if org.State != StateActive {
		return nil, apierrors.New(apierrors.CodeOrganizationDisabled)
	}
	project, err = repo.SetProjectState(ctx, req.GetProjectId(), StateActive)
	if err != nil {
		return nil, err
	}
	return &tenancyv1.EnableProjectResponse{Response: okResponse(), Project: summarizeProject(project)}, nil
}

// summarizeOrganization maps a row to the proto summary, filling the
// read-time resource counts (D11).
func (s *Service) summarizeOrganization(ctx context.Context, repo *Repository, org *Organization) (*tenancyv1.OrganizationSummary, error) {
	keyCount, err := repo.APIKeyCountByOrganization(ctx, org.ID)
	if err != nil {
		return nil, err
	}
	svcCount, err := repo.InferenceServiceCountByOrganization(ctx, org.ID)
	if err != nil {
		return nil, err
	}
	projectCount, err := repo.ProjectCountByOrganization(ctx, org.ID)
	if err != nil {
		return nil, err
	}
	return &tenancyv1.OrganizationSummary{
		OrganizationId:         org.ID,
		DisplayName:            org.DisplayName,
		Description:            org.Description,
		State:                  org.State,
		ApiKeyCount:            keyCount,
		InferenceServiceCount:  svcCount,
		ProjectCount:           projectCount,
		CreatedAt:              org.CreatedAt.Unix(),
		UpdatedAt:              org.UpdatedAt.Unix(),
	}, nil
}

// summarizeProject maps a row to the proto summary.
func summarizeProject(project *Project) *tenancyv1.ProjectSummary {
	return &tenancyv1.ProjectSummary{
		ProjectId:      project.ID,
		OrganizationId: project.OrganizationID,
		DisplayName:    project.DisplayName,
		Description:    project.Description,
		State:          project.State,
		CreatedAt:      project.CreatedAt.Unix(),
		UpdatedAt:      project.UpdatedAt.Unix(),
	}
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

// clampInt32 clamps v into the int32 range so proto fields never
// overflow on 32-bit hosts.
func clampInt32(v int) int32 {
	if v < math.MinInt32 {
		return math.MinInt32
	}
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v)
}

// okResponse is the shared success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}

// isRecordNotFound reports whether err is GORM's ErrRecordNotFound.
func isRecordNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
