// Package finetuning implements the dataset registry and the
// fine-tuning job lifecycle (feature #39). It serves the admin
// Fine-tuning pages, registering datasets, creating/monitoring
// fine-tuning jobs, and deploying fine-tuned models as inference
// services.
package finetuning

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	finetuningv1 "github.com/go-taas/go-taas/proto/taas/finetuning/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// ServiceName is the unique name of this service.
const ServiceName = "finetuning"

// organizationMetadataKey is the gRPC metadata key carrying the
// transitional caller organization.
const organizationMetadataKey = "x-organization-id"

// SessionOrgResolver resolves the session's active organization
// (feature-17 AD6).
type SessionOrgResolver interface {
	SessionActiveOrg(ctx context.Context) (string, error)
}

// SessionUserResolver resolves the authenticated caller's user id.
type SessionUserResolver interface {
	SessionUserID(ctx context.Context) (string, error)
}

// RoleGuard enforces the minimum org role on the admin fine-tuning RPCs.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

const roleAdmin = "admin"

// ModelResolver resolves base models and registers fine-tuned models
// (AD2, AD6). It is implemented by the model module and injected at
// wiring time.
type ModelResolver interface {
	// ModelVersionExists reports whether a (model, version) exists.
	ModelVersionExists(ctx context.Context, modelID, version string) (bool, error)
	// RegisterModelVersion registers a fine-tuned model version and
	// returns the model id.
	RegisterModelVersion(ctx context.Context, name, version, weightPath string) (string, error)
}

// ServiceDeployer deploys a model as an inference service (AD8). It is
// implemented by the infer module and injected at wiring time.
type ServiceDeployer interface {
	// DeployModel creates an inference service for a model version and
	// returns the service id.
	DeployModel(ctx context.Context, name, modelID, modelVersion, imageID, accelerator string, replicas int32) (string, error)
}

// Service implements the finetuning gRPC service.
type Service struct {
	finetuningv1.UnimplementedFineTuningServiceServer

	components server.Components

	// repo is the injection point used by tests and FVT; production
	// resolves it lazily from the shared components.
	datasetRepo *DatasetRepository
	jobRepo     *JobRepository

	// jobImage is the curated fine-tuning image (AD6).
	jobImage string

	// publisher publishes the finetuning.jobs.changes change event (AD5).
	publisher mq.Client

	// sessionOrgResolver resolves the session's active organization.
	sessionOrgResolver SessionOrgResolver
	// sessionUserResolver resolves the caller's user id.
	sessionUserResolver SessionUserResolver
	// roleGuard gates the admin fine-tuning RPCs.
	roleGuard RoleGuard
	// modelResolver resolves base models and registers fine-tuned models.
	modelResolver ModelResolver
	// serviceDeployer deploys fine-tuned models as inference services.
	serviceDeployer ServiceDeployer
}

// New constructs the finetuning service.
func New(components server.Components) *Service {
	return &Service{
		components: components,
		jobImage:   "ghcr.io/go-taas/go-taas/finetune:latest",
	}
}

// SetJobImage overrides the curated fine-tuning image.
func (s *Service) SetJobImage(v string) { s.jobImage = v }

// SetPublisher injects the MQ publisher for the change event (AD5).
func (s *Service) SetPublisher(p mq.Client) { s.publisher = p }

// SetSessionOrgResolver injects the session-organization resolver.
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver.
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard.
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// SetModelResolver injects the model resolver.
func (s *Service) SetModelResolver(r ModelResolver) { s.modelResolver = r }

// SetServiceDeployer injects the service deployer.
func (s *Service) SetServiceDeployer(d ServiceDeployer) { s.serviceDeployer = d }

// NewForFVT constructs a finetuning service bound to a caller-provided
// GORM database.
func NewForFVT(db *gorm.DB) *Service {
	return &Service{
		datasetRepo: NewDatasetRepository(db),
		jobRepo:     NewJobRepository(db),
		jobImage:    "ghcr.io/go-taas/go-taas/finetune:latest",
	}
}

// MigrateSchemaForFVT applies the finetuning schema onto a
// caller-provided database.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&FineTuningDataset{}, &FineTuningJob{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	finetuningv1.RegisterFineTuningServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return finetuningv1.RegisterFineTuningServiceHandler
}

// Migrate implements server.Migrator.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&FineTuningDataset{}, &FineTuningJob{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.datasetRepo != nil {
		return s.datasetRepo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "finetuning: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "finetuning: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repositories lazily wires and returns the finetuning repositories.
func (s *Service) repositories() (*DatasetRepository, *JobRepository, error) {
	if s.datasetRepo != nil && s.jobRepo != nil {
		return s.datasetRepo, s.jobRepo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, nil, err
	}
	s.datasetRepo = NewDatasetRepository(db)
	s.jobRepo = NewJobRepository(db)
	return s.datasetRepo, s.jobRepo, nil
}

// resolveOrg returns the organization context for a read.
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

// RegisterFineTuningDataset registers a dataset (AC1).
func (s *Service) RegisterFineTuningDataset(ctx context.Context, req *finetuningv1.RegisterFineTuningDatasetRequest) (*finetuningv1.RegisterFineTuningDatasetResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	format := strings.TrimSpace(req.GetFormat())
	objectPath := strings.TrimSpace(req.GetObjectPath())
	if name == "" || len(name) > 128 {
		return nil, apierrors.New(apierrors.CodeFineTuningDatasetInvalid)
	}
	if !validDatasetFormat(format) {
		return nil, apierrors.New(apierrors.CodeFineTuningDatasetInvalid)
	}
	if !validObjectPath(objectPath) {
		return nil, apierrors.New(apierrors.CodeFineTuningDatasetInvalid)
	}
	datasetRepo, _, err := s.repositories()
	if err != nil {
		return nil, err
	}
	dataset := &FineTuningDataset{
		ID:         newUUID(),
		Name:       name,
		Format:     format,
		ObjectPath: objectPath,
		CreatedAt:  time.Now().UTC(),
	}
	if err := datasetRepo.CreateDataset(ctx, dataset); err != nil {
		return nil, err
	}
	return &finetuningv1.RegisterFineTuningDatasetResponse{
		Response:  okResponse(),
		DatasetId: dataset.ID,
	}, nil
}

// ListFineTuningDatasets returns the registered datasets (AC1).
func (s *Service) ListFineTuningDatasets(ctx context.Context, _ *finetuningv1.ListFineTuningDatasetsRequest) (*finetuningv1.ListFineTuningDatasetsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	datasetRepo, _, err := s.repositories()
	if err != nil {
		return nil, err
	}
	rows, err := datasetRepo.ListDatasets(ctx)
	if err != nil {
		return nil, err
	}
	datasets := make([]*finetuningv1.FineTuningDataset, 0, len(rows))
	for _, d := range rows {
		datasets = append(datasets, &finetuningv1.FineTuningDataset{
			DatasetId:  d.ID,
			Name:       d.Name,
			Format:     d.Format,
			ObjectPath: d.ObjectPath,
			CreatedAt:  d.CreatedAt.Unix(),
		})
	}
	return &finetuningv1.ListFineTuningDatasetsResponse{
		Response:  okResponse(),
		Datasets:  datasets,
		PageMeta:  &commonv1.PageMeta{Total: int64(len(datasets))},
	}, nil
}

// CreateFineTuningJob creates a fine-tuning job (AC2).
func (s *Service) CreateFineTuningJob(ctx context.Context, req *finetuningv1.CreateFineTuningJobRequest) (*finetuningv1.CreateFineTuningJobResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	baseModelID := strings.TrimSpace(req.GetBaseModelId())
	baseModelVersion := strings.TrimSpace(req.GetBaseModelVersion())
	datasetID := strings.TrimSpace(req.GetDatasetId())
	if name == "" || len(name) > 128 {
		return nil, apierrors.New(apierrors.CodeFineTuningJobStateInvalid)
	}
	// Validate the base model (10101/10103).
	if s.modelResolver != nil {
		exists, err := s.modelResolver.ModelVersionExists(ctx, baseModelID, baseModelVersion)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, apierrors.New(apierrors.CodeModelNotFound)
		}
	}
	// Validate the dataset (12303).
	datasetRepo, jobRepo, err := s.repositories()
	if err != nil {
		return nil, err
	}
	if _, err := datasetRepo.FindDatasetByID(ctx, datasetID); err != nil {
		return nil, err
	}
	// Validate the hyperparameters (12305).
	hp := Hyperparameters{
		Epochs:       req.GetHyperparameters().GetEpochs(),
		BatchSize:    req.GetHyperparameters().GetBatchSize(),
		LearningRate: req.GetHyperparameters().GetLearningRate(),
	}
	if !validHyperparameters(hp) {
		return nil, apierrors.New(apierrors.CodeFineTuningHyperparametersInvalid)
	}
	hpRaw, err := encodeHyperparameters(hp)
	if err != nil {
		return nil, err
	}
	job := &FineTuningJob{
		ID:               newUUID(),
		Name:             name,
		BaseModelID:      baseModelID,
		BaseModelVersion: baseModelVersion,
		DatasetID:        datasetID,
		Hyperparameters:  hpRaw,
		State:            JobStatePending,
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}
	if err := jobRepo.CreateJob(ctx, job); err != nil {
		return nil, err
	}
	// Publish the change event (AD5).
	if s.publisher != nil {
		body, _ := jsonMarshal(map[string]any{
			"job_id": job.ID, "name": job.Name,
			"base_model_id": job.BaseModelID, "base_model_version": job.BaseModelVersion,
			"dataset_id": job.DatasetID, "hyperparameters": hp,
			"image": s.jobImage,
		})
		_ = s.publisher.Publish(ctx, mq.DefaultSubjects().FineTuningJobsChanges, body, nil)
	}
	return &finetuningv1.CreateFineTuningJobResponse{
		Response: okResponse(),
		JobId:    job.ID,
		State:    job.State,
	}, nil
}

// ListFineTuningJobs returns the jobs, newest first (AC3).
func (s *Service) ListFineTuningJobs(ctx context.Context, _ *finetuningv1.ListFineTuningJobsRequest) (*finetuningv1.ListFineTuningJobsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	_, jobRepo, err := s.repositories()
	if err != nil {
		return nil, err
	}
	rows, err := jobRepo.ListJobs(ctx)
	if err != nil {
		return nil, err
	}
	jobs := make([]*finetuningv1.FineTuningJobSummary, 0, len(rows))
	for _, j := range rows {
		jobs = append(jobs, jobSummary(&j))
	}
	return &finetuningv1.ListFineTuningJobsResponse{
		Response: okResponse(),
		Jobs:     jobs,
		PageMeta: &commonv1.PageMeta{Total: int64(len(jobs))},
	}, nil
}

// GetFineTuningJob returns a job's full record (AC3).
func (s *Service) GetFineTuningJob(ctx context.Context, req *finetuningv1.GetFineTuningJobRequest) (*finetuningv1.GetFineTuningJobResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	_, jobRepo, err := s.repositories()
	if err != nil {
		return nil, err
	}
	job, err := jobRepo.FindJobByID(ctx, strings.TrimSpace(req.GetJobId()))
	if err != nil {
		return nil, err
	}
	hp, _ := decodeHyperparameters(job.Hyperparameters)
	return &finetuningv1.GetFineTuningJobResponse{
		Response: okResponse(),
		Job: &finetuningv1.FineTuningJob{
			Summary:        jobSummary(job),
			Hyperparameters: &finetuningv1.FineTuningHyperparameters{
				Epochs: hp.Epochs, BatchSize: hp.BatchSize, LearningRate: hp.LearningRate,
			},
			FailureReason: job.FailureReason,
		},
	}, nil
}

// DeployFineTunedModel deploys a succeeded job's fine-tuned model as an
// inference service (AC4).
func (s *Service) DeployFineTunedModel(ctx context.Context, req *finetuningv1.DeployFineTunedModelRequest) (*finetuningv1.DeployFineTunedModelResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	_, jobRepo, err := s.repositories()
	if err != nil {
		return nil, err
	}
	job, err := jobRepo.FindJobByID(ctx, strings.TrimSpace(req.GetJobId()))
	if err != nil {
		return nil, err
	}
	// Deploy is available only on a succeeded job (AD9).
	if job.State != JobStateSucceeded {
		return nil, apierrors.New(apierrors.CodeFineTuningJobStateInvalid)
	}
	if s.serviceDeployer == nil {
		return nil, apierrors.New(apierrors.CodeInternal)
	}
	serviceID, err := s.serviceDeployer.DeployModel(ctx, job.Name, job.BaseModelID, job.BaseModelVersion, strings.TrimSpace(req.GetImageId()), strings.TrimSpace(req.GetAccelerator()), req.GetReplicas())
	if err != nil {
		return nil, err
	}
	return &finetuningv1.DeployFineTunedModelResponse{
		Response:  okResponse(),
		ServiceId: serviceID,
	}, nil
}

// jobSummary converts a job row to its wire summary.
func jobSummary(j *FineTuningJob) *finetuningv1.FineTuningJobSummary {
	return &finetuningv1.FineTuningJobSummary{
		JobId:             j.ID,
		Name:              j.Name,
		BaseModelId:       j.BaseModelID,
		BaseModelVersion:  j.BaseModelVersion,
		DatasetId:         j.DatasetID,
		State:             j.State,
		FineTunedModelId:  j.FineTunedModelID,
		CreatedAt:         j.CreatedAt.Unix(),
		UpdatedAt:         j.UpdatedAt.Unix(),
	}
}

// okResponse returns the standard success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}