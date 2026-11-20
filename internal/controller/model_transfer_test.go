package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/storage"
	"github.com/go-taas/go-taas/pkg/transfer"
)

func testTransferStorage() storage.Config {
	return storage.Config{
		DefaultBackend: "shared",
		Backends: map[string]storage.Backend{
			"shared": {
				Type: storage.TypeFilesystem,
				Filesystem: storage.Filesystem{
					ExistingClaim: "model-weights",
					MountPath:     "/data/weights",
				},
			},
			"archive": {
				Type: storage.TypeObjectStore,
				ObjectStore: storage.ObjectStore{
					Endpoint: "http://minio:9000",
					Bucket:   "models",
					Credentials: storage.Credentials{
						ExistingSecret: "archive-keys",
					},
				},
			},
		},
	}
}

func TestApplyModelTransferCreatesJob(t *testing.T) {
	client := fake.NewSimpleClientset()
	reconciler := NewModelTransferReconciler(client, mq.NewFake(), config.TransferConfig{
		Namespace:      "taas-jobs",
		Image:          "hub.example.com/taas/taas-transfer:test",
		ServiceAccount: "taas-transfer",
		Job: config.TransferJobConfig{
			BackoffLimit:          2,
			ActiveDeadlineSeconds: 600,
			TTLSecondsAfterFinish: 3600,
		},
		Resources: map[string]map[string]string{
			"requests": {"cpu": "100m", "memory": "128Mi"},
			"limits":   {"cpu": "2", "memory": "2Gi"},
		},
	}, testTransferStorage())

	spec := transfer.Spec{
		ID:            "transfer-1",
		Operation:     transfer.OperationCopy,
		SourceBackend: "archive",
		TargetBackend: "shared",
		SourceURI:     "s3://models/qwen/v1",
		TargetPath:    "/data/weights/qwen/v1",
	}
	body, err := json.Marshal(&spec)
	require.NoError(t, err)
	require.NoError(t, reconciler.ApplyModelTransfer(context.Background(), mq.Message{Body: body}))

	job, err := client.BatchV1().Jobs("taas-jobs").Get(context.Background(), "taas-transfer-transfer-1", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, int32(2), *job.Spec.BackoffLimit)
	assert.Equal(t, int64(600), *job.Spec.ActiveDeadlineSeconds)
	assert.Equal(t, int32(3600), *job.Spec.TTLSecondsAfterFinished)
	require.Len(t, job.Spec.Template.Spec.Volumes, 1)
	assert.Equal(t, "model-weights", job.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName)
	require.NotNil(t, job.Spec.Template.Spec.SecurityContext)
	require.NotNil(t, job.Spec.Template.Spec.SecurityContext.RunAsUser)
	assert.Equal(t, int64(0), *job.Spec.Template.Spec.SecurityContext.RunAsUser)
	container := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "/usr/local/bin/taas-transfer", container.Command[0])
	env := map[string]corev1.EnvVar{}
	for _, item := range container.Env {
		env[item.Name] = item
	}
	assert.Equal(t, "transfer-1", mustDecodeSpec(t, env["TRANSFER_SPEC_JSON"].Value).ID)
	assert.Equal(t, "http://minio:9000", env["TRANSFER_S3_ENDPOINT"].Value)
	require.NotNil(t, env["TRANSFER_SOURCE_ACCESS_KEY"].ValueFrom)
	assert.Equal(t, "archive-keys", env["TRANSFER_SOURCE_ACCESS_KEY"].ValueFrom.SecretKeyRef.Name)

	// Re-applying is idempotent.
	require.NoError(t, reconciler.ApplyModelTransfer(context.Background(), mq.Message{Body: body}))
	jobs, err := client.BatchV1().Jobs("taas-jobs").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 1)
}

func TestApplyModelTransferDownloadSkipsHubSourceBackend(t *testing.T) {
	client := fake.NewSimpleClientset()
	reconciler := NewModelTransferReconciler(client, mq.NewFake(), config.TransferConfig{
		Namespace: "taas-jobs",
		Image:     "transfer:test",
	}, testTransferStorage())

	spec := transfer.Spec{
		ID:            "transfer-hub",
		Operation:     transfer.OperationDownload,
		SourceBackend: "hub:modelscope",
		TargetBackend: "archive",
		SourceURI:     "hub:modelscope",
		TargetURI:     "s3://models/qwen",
		HubSource:     "modelscope",
		HubModelID:    "Qwen/Qwen2.5-0.5B-Instruct-GPTQ-Int4",
		WeightPath:    "Qwen__Qwen2.5-0.5B-Instruct-GPTQ-Int4/",
	}
	body, err := json.Marshal(&spec)
	require.NoError(t, err)
	require.NoError(t, reconciler.ApplyModelTransfer(context.Background(), mq.Message{Body: body}))

	job, err := client.BatchV1().Jobs("taas-jobs").Get(context.Background(), "taas-transfer-transfer-hub", metav1.GetOptions{})
	require.NoError(t, err)
	env := map[string]corev1.EnvVar{}
	for _, item := range job.Spec.Template.Spec.Containers[0].Env {
		env[item.Name] = item
	}
	assert.Equal(t, "modelscope", env["TRANSFER_HUB_SOURCE"].Value)
	assert.Equal(t, "Qwen/Qwen2.5-0.5B-Instruct-GPTQ-Int4", env["TRANSFER_HUB_MODEL_ID"].Value)
	require.NotNil(t, env["TRANSFER_TARGET_ACCESS_KEY"].ValueFrom)
	assert.Equal(t, "archive-keys", env["TRANSFER_TARGET_ACCESS_KEY"].ValueFrom.SecretKeyRef.Name)
}

func mustDecodeSpec(t *testing.T, raw string) transfer.Spec {
	t.Helper()
	var spec transfer.Spec
	require.NoError(t, json.Unmarshal([]byte(raw), &spec))
	return spec
}

func TestApplyModelTransferCancelDeletesJob(t *testing.T) {
	client := fake.NewSimpleClientset(&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      "taas-transfer-transfer-2",
		Namespace: "taas-jobs",
	}})
	reconciler := NewModelTransferReconciler(client, mq.NewFake(), config.TransferConfig{
		Namespace: "taas-jobs",
		Image:     "transfer:test",
	}, testTransferStorage())
	body, _ := json.Marshal(&transfer.Spec{ID: "transfer-2", Operation: transfer.OperationCancel})
	require.NoError(t, reconciler.ApplyModelTransfer(context.Background(), mq.Message{Body: body}))
	_, err := client.BatchV1().Jobs("taas-jobs").Get(context.Background(), "taas-transfer-transfer-2", metav1.GetOptions{})
	assert.Error(t, err)
}

func TestReportJobsPublishesProgressAndTerminalState(t *testing.T) {
	client := fake.NewSimpleClientset(
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "taas-transfer-transfer-3",
				Namespace: "taas-jobs",
				Labels:    map[string]string{transferLabel: "true", transferIDLabel: "transfer-3"},
			},
			Status: batchv1.JobStatus{Succeeded: 1},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "taas-transfer-progress-transfer-3", Namespace: "taas-jobs"},
			Data:       map[string]string{"progress.json": `{"state":"running","bytes":42,"totalBytes":100}`},
		},
	)
	clientMQ := mq.NewFake()
	var got []transfer.Status
	require.NoError(t, clientMQ.Subscribe(context.Background(), mq.DefaultSubjects().ModelTransferStatus, func(msg mq.Message) error {
		var status transfer.Status
		if err := json.Unmarshal(msg.Body, &status); err != nil {
			return err
		}
		got = append(got, status)
		return nil
	}))
	reconciler := NewModelTransferReconciler(client, clientMQ, config.TransferConfig{Namespace: "taas-jobs", Image: "transfer:test"}, testTransferStorage())
	require.NoError(t, reconciler.reportJobs(context.Background()))
	require.Len(t, got, 1)
	assert.Equal(t, transfer.StateSucceeded, got[0].State)
	assert.Equal(t, int64(42), got[0].Bytes)
	assert.Equal(t, int64(100), got[0].TotalBytes)
}
