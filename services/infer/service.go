// Package infer implements the inference service lifecycle API: creating,
// scaling and deleting inference services. Desired-state changes are
// published to the message queue; the controller applies them to the
// cluster.
package infer

import (
	"context"
	"math"
	"regexp"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/image"
	"github.com/go-taas/go-taas/services/model"
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
}

// New constructs the inference service from the shared server
// components.
func New(components server.Components) *Service {
	return &Service{components: components}
}

// NewWithDependencies constructs a Service with explicit dependencies
// (tests, tools).
func NewWithDependencies(
	repo *InferenceServiceRepository,
	modelRepo *model.Repository,
	mqClient mq.Client,
) *Service {
	return &Service{repo: repo, modelRepo: modelRepo, mqClient: mqClient}
}

// NewForFVT constructs a Service bound to a test database and MQ
// client for FVT.
func NewForFVT(db *gorm.DB, mqClient mq.Client) *Service {
	return NewWithDependencies(
		NewInferenceServiceRepository(db),
		model.NewRepository(db),
		mqClient,
	)
}

// MigrateSchemaForFVT applies the infer schema (inference_services) to
// the given database. FVT-only helper.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&InferenceService{})
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
	return db.WithContext(ctx).AutoMigrate(&InferenceService{})
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

// CreateInferenceService deploys a model with the given image and
// replicas. The change is published to the message queue and applied by
// the controller.
func (s *Service) CreateInferenceService(ctx context.Context, req *inferv1.CreateInferenceServiceRequest) (*inferv1.CreateInferenceServiceResponse, error) {
	orgID, err := resolveOrganizationID(ctx)
	if err != nil {
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
	img, err := image.Lookup(req.GetImageId())
	if err != nil {
		return nil, err
	}
	if img.Accelerator != accelerator {
		return nil, apierrors.New(apierrors.CodeImageIncompatible)
	}

	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	client, err := s.mqClientFor()
	if err != nil {
		return nil, err
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
	}
	if err := repo.Create(ctx, svc); err != nil {
		return nil, err
	}

	evt := buildChangeEvent(EventTypeUpsert, svc, modelVersion.WeightPath, img.Reference(), img.Engine)
	if err := publishChangeWithCompensation(ctx, client, repo, evt, svc.ID); err != nil {
		logger.S().Errorw("infer: publish create change failed",
			"service_id", svc.ID, "err", err)
		return nil, apierrors.Newf(apierrors.CodeInternal, "infer: publish change failed")
	}

	return &inferv1.CreateInferenceServiceResponse{
		Response:  okResponse(),
		ServiceId: svc.ID,
	}, nil
}

// ListInferenceServices returns the inference services of the caller's
// organization.
func (s *Service) ListInferenceServices(ctx context.Context, req *inferv1.ListInferenceServicesRequest) (*inferv1.ListInferenceServicesResponse, error) {
	orgID, err := resolveOrganizationID(ctx)
	if err != nil {
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

	services := make([]*inferv1.InferenceServiceSummary, 0, len(rows))
	for _, row := range rows {
		services = append(services, summarizeService(row))
	}
	return &inferv1.ListInferenceServicesResponse{
		Response: okResponse(),
		Services: services,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampToInt32(limit)},
	}, nil
}

// GetInferenceService returns one inference service with its status.
func (s *Service) GetInferenceService(ctx context.Context, req *inferv1.GetInferenceServiceRequest) (*inferv1.GetInferenceServiceResponse, error) {
	orgID, err := resolveOrganizationID(ctx)
	if err != nil {
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

	// Endpoints are only exposed while running.
	var endpoints []string
	if row.State == StateRunning {
		endpoints = row.Endpoints
	}
	return &inferv1.GetInferenceServiceResponse{
		Response:  okResponse(),
		Service:   summarizeService(row),
		Endpoints: endpoints,
	}, nil
}

// ScaleInferenceService changes the replica count of an inference
// service.
func (s *Service) ScaleInferenceService(ctx context.Context, req *inferv1.ScaleInferenceServiceRequest) (*inferv1.ScaleInferenceServiceResponse, error) {
	orgID, err := resolveOrganizationID(ctx)
	if err != nil {
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

	return &inferv1.ScaleInferenceServiceResponse{Response: okResponse()}, nil
}

// DeleteInferenceService removes an inference service.
func (s *Service) DeleteInferenceService(ctx context.Context, req *inferv1.DeleteInferenceServiceRequest) (*inferv1.DeleteInferenceServiceResponse, error) {
	orgID, err := resolveOrganizationID(ctx)
	if err != nil {
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

	return &inferv1.DeleteInferenceServiceResponse{Response: okResponse()}, nil
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

// summarizeService maps a row to the API summary.
func summarizeService(row *InferenceService) *inferv1.InferenceServiceSummary {
	return &inferv1.InferenceServiceSummary{
		ServiceId:    row.ID,
		Name:         row.Name,
		ModelId:      row.ModelID,
		ModelVersion: row.ModelVersion,
		ImageId:      row.ImageID,
		Replicas:     clampToInt32(row.Replicas),
		State:        row.State,
		UpdatedAt:    row.UpdatedAt.Unix(),
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
