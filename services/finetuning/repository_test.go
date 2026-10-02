package finetuning

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&FineTuningDataset{}, &FineTuningJob{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func TestDatasetRepository(t *testing.T) {
	db := newTestDB(t)
	repo := NewDatasetRepository(db)
	datasetID := "11111111-1111-1111-1111-111111111111"

	// AC1: CreateDataset + ListDatasets.
	d := &FineTuningDataset{ID: datasetID, Name: "train", Format: "jsonl", ObjectPath: "data/train.jsonl"}
	require.NoError(t, repo.CreateDataset(context.Background(), d))
	rows, err := repo.ListDatasets(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "train", rows[0].Name)

	// AC1: FindDatasetByID.
	found, err := repo.FindDatasetByID(context.Background(), datasetID)
	require.NoError(t, err)
	assert.Equal(t, "train", found.Name)

	// AC1: unknown dataset -> 12303.
	_, err = repo.FindDatasetByID(context.Background(), "missing")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeFineTuningDatasetNotFound, apierrors.CodeOf(err))
}

func TestJobRepository(t *testing.T) {
	db := newTestDB(t)
	repo := NewJobRepository(db)
	jobID := "11111111-1111-1111-1111-111111111111"

	// AC2: CreateJob.
	job := &FineTuningJob{ID: jobID, Name: "job-1", BaseModelID: "m1", BaseModelVersion: "v1", DatasetID: "d1", Hyperparameters: `{"epochs":3,"batch_size":4,"learning_rate":0.001}`, State: JobStatePending}
	require.NoError(t, repo.CreateJob(context.Background(), job))

	// AC3: FindJobByID + ListJobs.
	found, err := repo.FindJobByID(context.Background(), jobID)
	require.NoError(t, err)
	assert.Equal(t, "job-1", found.Name)
	rows, err := repo.ListJobs(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// AC3: unknown job -> 12301.
	_, err = repo.FindJobByID(context.Background(), "missing")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeFineTuningJobNotFound, apierrors.CodeOf(err))

	// AC4: UpdateJobState.
	require.NoError(t, repo.UpdateJobState(context.Background(), jobID, JobStateSucceeded, "", "ft-model"))
	found, err = repo.FindJobByID(context.Background(), jobID)
	require.NoError(t, err)
	assert.Equal(t, JobStateSucceeded, found.State)
	assert.Equal(t, "ft-model", found.FineTunedModelID)
}

func TestValidObjectPath(t *testing.T) {
	assert.True(t, validObjectPath("data/train.jsonl"))
	assert.False(t, validObjectPath(""))
	assert.False(t, validObjectPath("/data/train.jsonl"))
	assert.False(t, validObjectPath("data/../train.jsonl"))
	assert.False(t, validObjectPath("data\\train.jsonl"))
}

func TestValidDatasetFormat(t *testing.T) {
	assert.True(t, validDatasetFormat("jsonl"))
	assert.True(t, validDatasetFormat("csv"))
	assert.False(t, validDatasetFormat("parquet"))
}

func TestValidHyperparameters(t *testing.T) {
	assert.True(t, validHyperparameters(Hyperparameters{Epochs: 3, BatchSize: 4, LearningRate: 0.001}))
	assert.False(t, validHyperparameters(Hyperparameters{Epochs: 0, BatchSize: 4, LearningRate: 0.001}))
	assert.False(t, validHyperparameters(Hyperparameters{Epochs: 3, BatchSize: 0, LearningRate: 0.001}))
	assert.False(t, validHyperparameters(Hyperparameters{Epochs: 3, BatchSize: 4, LearningRate: 0}))
}

func TestHyperparametersEncodeDecode(t *testing.T) {
	raw, err := encodeHyperparameters(Hyperparameters{Epochs: 3, BatchSize: 4, LearningRate: 0.001})
	require.NoError(t, err)
	decoded, err := decodeHyperparameters(raw)
	require.NoError(t, err)
	assert.Equal(t, int32(3), decoded.Epochs)
	assert.Equal(t, int32(4), decoded.BatchSize)
	assert.InDelta(t, 0.001, decoded.LearningRate, 0.0001)
}