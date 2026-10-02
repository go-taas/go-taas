package finetuning

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	finetuningv1 "github.com/go-taas/go-taas/proto/taas/finetuning/v1"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

type fakeSessionOrgResolver struct{ org string }

func (f fakeSessionOrgResolver) SessionActiveOrg(context.Context) (string, error) {
	return f.org, nil
}

type fakeSessionUserResolver struct{ user string }

func (f fakeSessionUserResolver) SessionUserID(context.Context) (string, error) {
	return f.user, nil
}

type fakeRoleGuard struct{ allowed bool }

func (f fakeRoleGuard) RequireRole(_ context.Context, _, _, _ string) error {
	if !f.allowed {
		return apierrors.New(apierrors.CodeForbidden)
	}
	return nil
}

type fakeModelResolver struct{ exists bool }

func (f fakeModelResolver) ModelVersionExists(context.Context, string, string) (bool, error) {
	return f.exists, nil
}

func (f fakeModelResolver) RegisterModelVersion(_ context.Context, _, _, _ string) (string, error) {
	return "ft-model", nil
}

type fakeServiceDeployer struct{ serviceID string }

func (f fakeServiceDeployer) DeployModel(_ context.Context, _, _, _, _, _ string, _ int32) (string, error) {
	return f.serviceID, nil
}

func ctxWithOrg(ctx context.Context, org string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs("x-organization-id", org))
}

func newService(t *testing.T, db *gorm.DB, roleAllowed bool) *Service {
	t.Helper()
	svc := NewForFVT(db)
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-1"})
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "user-1"})
	svc.SetRoleGuard(fakeRoleGuard{allowed: roleAllowed})
	svc.SetModelResolver(fakeModelResolver{exists: true})
	svc.SetServiceDeployer(fakeServiceDeployer{serviceID: "svc-1"})
	return svc
}

func TestRegisterAndListDatasets(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, true)

	// AC1: register a dataset.
	resp, err := svc.RegisterFineTuningDataset(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.RegisterFineTuningDatasetRequest{
		Name: "train", Format: "jsonl", ObjectPath: "data/train.jsonl",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.DatasetId)

	// AC1: invalid object path -> 12304.
	_, err = svc.RegisterFineTuningDataset(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.RegisterFineTuningDatasetRequest{
		Name: "bad", Format: "jsonl", ObjectPath: "/data/../x",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeFineTuningDatasetInvalid, apierrors.CodeOf(err))

	// AC1: list returns the dataset.
	list, err := svc.ListFineTuningDatasets(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.ListFineTuningDatasetsRequest{})
	require.NoError(t, err)
	require.Len(t, list.Datasets, 1)
	assert.Equal(t, "train", list.Datasets[0].Name)
}

func TestCreateAndGetJob(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, true)

	// Register a dataset.
	ds, err := svc.RegisterFineTuningDataset(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.RegisterFineTuningDatasetRequest{
		Name: "train", Format: "jsonl", ObjectPath: "data/train.jsonl",
	})
	require.NoError(t, err)

	// AC2: create a job returns state=pending.
	create, err := svc.CreateFineTuningJob(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.CreateFineTuningJobRequest{
		Name: "job-1", BaseModelId: "m1", BaseModelVersion: "v1", DatasetId: ds.DatasetId,
		Hyperparameters: &finetuningv1.FineTuningHyperparameters{Epochs: 3, BatchSize: 4, LearningRate: 0.001},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, create.JobId)
	assert.Equal(t, JobStatePending, create.State)

	// AC3: get the job.
	got, err := svc.GetFineTuningJob(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.GetFineTuningJobRequest{JobId: create.JobId})
	require.NoError(t, err)
	assert.Equal(t, "job-1", got.Job.Summary.Name)
	assert.Equal(t, int32(3), got.Job.Hyperparameters.Epochs)

	// AC3: list returns the job.
	list, err := svc.ListFineTuningJobs(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.ListFineTuningJobsRequest{})
	require.NoError(t, err)
	require.Len(t, list.Jobs, 1)

	// AC3: unknown job -> 12301.
	_, err = svc.GetFineTuningJob(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.GetFineTuningJobRequest{JobId: "missing"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeFineTuningJobNotFound, apierrors.CodeOf(err))
}

func TestCreateJobValidation(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, true)

	// AC2: unknown dataset -> 12303.
	_, err := svc.CreateFineTuningJob(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.CreateFineTuningJobRequest{
		Name: "job-1", BaseModelId: "m1", BaseModelVersion: "v1", DatasetId: "missing",
		Hyperparameters: &finetuningv1.FineTuningHyperparameters{Epochs: 3, BatchSize: 4, LearningRate: 0.001},
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeFineTuningDatasetNotFound, apierrors.CodeOf(err))

	// Register a dataset.
	ds, err := svc.RegisterFineTuningDataset(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.RegisterFineTuningDatasetRequest{
		Name: "train", Format: "jsonl", ObjectPath: "data/train.jsonl",
	})
	require.NoError(t, err)

	// AC2: unknown base model -> 10101.
	svc.SetModelResolver(fakeModelResolver{exists: false})
	_, err = svc.CreateFineTuningJob(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.CreateFineTuningJobRequest{
		Name: "job-1", BaseModelId: "m1", BaseModelVersion: "v1", DatasetId: ds.DatasetId,
		Hyperparameters: &finetuningv1.FineTuningHyperparameters{Epochs: 3, BatchSize: 4, LearningRate: 0.001},
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeModelNotFound, apierrors.CodeOf(err))
	svc.SetModelResolver(fakeModelResolver{exists: true})

	// AC2: invalid hyperparameters -> 12305.
	_, err = svc.CreateFineTuningJob(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.CreateFineTuningJobRequest{
		Name: "job-1", BaseModelId: "m1", BaseModelVersion: "v1", DatasetId: ds.DatasetId,
		Hyperparameters: &finetuningv1.FineTuningHyperparameters{Epochs: 0, BatchSize: 4, LearningRate: 0.001},
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeFineTuningHyperparametersInvalid, apierrors.CodeOf(err))
}

func TestDeployFineTunedModel(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, true)

	// Register a dataset and create a job.
	ds, err := svc.RegisterFineTuningDataset(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.RegisterFineTuningDatasetRequest{
		Name: "train", Format: "jsonl", ObjectPath: "data/train.jsonl",
	})
	require.NoError(t, err)
	create, err := svc.CreateFineTuningJob(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.CreateFineTuningJobRequest{
		Name: "job-1", BaseModelId: "m1", BaseModelVersion: "v1", DatasetId: ds.DatasetId,
		Hyperparameters: &finetuningv1.FineTuningHyperparameters{Epochs: 3, BatchSize: 4, LearningRate: 0.001},
	})
	require.NoError(t, err)

	// AC4: deploy on a pending job -> 12302.
	_, err = svc.DeployFineTunedModel(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.DeployFineTunedModelRequest{
		JobId: create.JobId, ImageId: "img", Accelerator: "nvidia", Replicas: 1,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeFineTuningJobStateInvalid, apierrors.CodeOf(err))

	// Mark the job succeeded.
	_, jobRepo, err := svc.repositories()
	require.NoError(t, err)
	require.NoError(t, jobRepo.UpdateJobState(context.Background(), create.JobId, JobStateSucceeded, "", "ft-model"))

	// AC4: deploy on a succeeded job returns a service_id.
	deploy, err := svc.DeployFineTunedModel(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.DeployFineTunedModelRequest{
		JobId: create.JobId, ImageId: "img", Accelerator: "nvidia", Replicas: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, "svc-1", deploy.ServiceId)
}

func TestRoleDenied(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, false)
	// AC9: role denied -> 10036.
	_, err := svc.ListFineTuningJobs(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.ListFineTuningJobsRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

func TestServiceWiring(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	assert.Equal(t, ServiceName, svc.ServiceName())
	grpcSrv := grpc.NewServer()
	svc.AttachToServer(grpcSrv)
	assert.NotNil(t, svc.GetServiceHandlerRegisterFn())
	assert.NoError(t, svc.Migrate(context.Background()))
	assert.NotNil(t, grpcSrv.GetServiceInfo()["taas.finetuning.v1.FineTuningService"])
}

// TestMigrateSchemaForFVT verifies the FVT schema migration creates the
// tables.
func TestMigrateSchemaForFVT(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, MigrateSchemaForFVT(db))
	assert.True(t, db.Migrator().HasTable(&FineTuningDataset{}))
	assert.True(t, db.Migrator().HasTable(&FineTuningJob{}))
}

// TestTransitionalPath verifies the session-less path resolves the org
// from the X-Organization-Id header.
func TestTransitionalPath(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	resp, err := svc.ListFineTuningDatasets(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.ListFineTuningDatasetsRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Datasets)
}

// TestNoOrg verifies a missing org is unauthorized.
func TestNoOrg(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	_, err := svc.ListFineTuningDatasets(context.Background(), &finetuningv1.ListFineTuningDatasetsRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeUnauthorized, apierrors.CodeOf(err))
}

// TestSetJobImage verifies the job image override.
func TestSetJobImage(t *testing.T) {
	svc := NewForFVT(newTestDB(t))
	svc.SetJobImage("custom/finetune:v1")
	assert.Equal(t, "custom/finetune:v1", svc.jobImage)
}

// fakeComponents provides a DB component for the production wiring path.
type fakeComponents struct {
	db *gorm.DB
}

func (f *fakeComponents) DB() server.DBComponent       { return &fakeDBComponent{db: f.db} }
func (f *fakeComponents) Redis() server.RedisComponent { return nil }
func (f *fakeComponents) MQ() server.MQComponent       { return nil }

type fakeDBComponent struct{ db *gorm.DB }

func (f *fakeDBComponent) GormDB() any { return f.db }

// TestProductionConstructor exercises the production New constructor and
// Migrate through the shared components.
func TestProductionConstructor(t *testing.T) {
	db := newTestDB(t)
	svc := New(&fakeComponents{db: db})
	assert.Equal(t, ServiceName, svc.ServiceName())
	require.NoError(t, svc.Migrate(context.Background()))
	svc.SetPublisher(mq.NewFake())
	assert.NotNil(t, svc.publisher)

	// Exercise the production repositories path (via components).
	svc.datasetRepo = nil
	svc.jobRepo = nil
	_, _, err := svc.repositories()
	require.NoError(t, err)
}

// TestDeployNoDeployer verifies the deploy step fails closed when no
// deployer is wired.
func TestDeployNoDeployer(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, true)
	svc.SetServiceDeployer(nil)

	// Register a dataset and create a job, then mark it succeeded.
	ds, err := svc.RegisterFineTuningDataset(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.RegisterFineTuningDatasetRequest{
		Name: "train", Format: "jsonl", ObjectPath: "data/train.jsonl",
	})
	require.NoError(t, err)
	create, err := svc.CreateFineTuningJob(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.CreateFineTuningJobRequest{
		Name: "job-1", BaseModelId: "m1", BaseModelVersion: "v1", DatasetId: ds.DatasetId,
		Hyperparameters: &finetuningv1.FineTuningHyperparameters{Epochs: 3, BatchSize: 4, LearningRate: 0.001},
	})
	require.NoError(t, err)
	_, jobRepo, err := svc.repositories()
	require.NoError(t, err)
	require.NoError(t, jobRepo.UpdateJobState(context.Background(), create.JobId, JobStateSucceeded, "", "ft-model"))

	_, err = svc.DeployFineTunedModel(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.DeployFineTunedModelRequest{
		JobId: create.JobId, ImageId: "img", Accelerator: "nvidia", Replicas: 1,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInternal, apierrors.CodeOf(err))
}

// TestRegisterDatasetValidation verifies the dataset validation branches.
func TestRegisterDatasetValidation(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, true)
	ctx := ctxWithOrg(context.Background(), "org-1")

	// Empty name -> 12304.
	_, err := svc.RegisterFineTuningDataset(ctx, &finetuningv1.RegisterFineTuningDatasetRequest{
		Name: "", Format: "jsonl", ObjectPath: "data/x",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeFineTuningDatasetInvalid, apierrors.CodeOf(err))

	// Invalid format -> 12304.
	_, err = svc.RegisterFineTuningDataset(ctx, &finetuningv1.RegisterFineTuningDatasetRequest{
		Name: "train", Format: "parquet", ObjectPath: "data/x",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeFineTuningDatasetInvalid, apierrors.CodeOf(err))
}

// TestCreateJobEmptyName verifies the empty-name branch.
func TestCreateJobEmptyName(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, true)
	_, err := svc.CreateFineTuningJob(ctxWithOrg(context.Background(), "org-1"), &finetuningv1.CreateFineTuningJobRequest{
		Name: "", BaseModelId: "m1", BaseModelVersion: "v1", DatasetId: "d1",
		Hyperparameters: &finetuningv1.FineTuningHyperparameters{Epochs: 3, BatchSize: 4, LearningRate: 0.001},
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeFineTuningJobStateInvalid, apierrors.CodeOf(err))
}