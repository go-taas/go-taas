// Package model implements the model registry service: registering
// models and their versions, and serving registry queries to the rest of
// the control plane.
package model

import (
	"context"
	"math"
	"strings"

	"google.golang.org/grpc"
	"gorm.io/gorm"

	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// ServiceName is the unique name of this service.
const ServiceName = "model"

// Pagination bounds for ListModels.
const (
	listDefaultLimit = 20
	listMaxLimit     = 100
)

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

// NewForFVT constructs a model service bound to a caller-provided GORM
// database. It exists so full-verification tests can wire the real
// service stack against a disposable database.
func NewForFVT(db *gorm.DB) *Service {
	return &Service{repo: NewRepository(db)}
}

// MigrateSchemaForFVT applies the model schema (models, model_versions)
// onto a caller-provided database for full-verification tests.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&Model{}, &Version{})
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

// Migrate implements server.Migrator: it creates/updates the models and
// model_versions tables via GORM AutoMigrate. The GORM models are the
// single source of truth for the schema.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&Model{}, &Version{})
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
// row's latest version derived at read time.
func (s *Service) ListModels(ctx context.Context, req *modelv1.ListModelsRequest) (*modelv1.ListModelsResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}

	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListModels(ctx, offset, limit)
	if err != nil {
		return nil, err
	}

	models := make([]*modelv1.ModelSummary, 0, len(rows))
	for _, row := range rows {
		summary := &modelv1.ModelSummary{
			ModelId:   row.ID,
			Name:      row.Name,
			CreatedAt: row.CreatedAt.Unix(),
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

	summary := &modelv1.ModelSummary{
		ModelId:   m.ID,
		Name:      m.Name,
		CreatedAt: m.CreatedAt.Unix(),
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
	return &modelv1.DeleteModelResponse{Response: okResponse()}, nil
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
