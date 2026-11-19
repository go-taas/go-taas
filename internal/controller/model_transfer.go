package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/storage"
	"github.com/go-taas/go-taas/pkg/transfer"
)

const (
	transferLabel            = "taas.io/model-transfer"
	transferIDLabel          = "taas.io/model-transfer-id"
	transferContainerName    = "transfer"
	transferSpecEnv          = "TRANSFER_SPEC_JSON"
	transferProgressInterval = 10 * time.Second
)

// ModelTransferReconciler turns model.transfers messages into Kubernetes
// Jobs and reports their terminal state. It holds no database handle: the
// server owns the row, the controller owns the Job.
type ModelTransferReconciler struct {
	clientset   kubernetes.Interface
	client      mq.Client
	namespace   string
	image       string
	saName      string
	jobConfig   config.TransferJobConfig
	nodeSelect  map[string]string
	tolerations []map[string]any
	resources   map[string]map[string]string
	storage     storage.Config
	progress    time.Duration
}

// NewModelTransferReconciler builds the transfer reconciler.
func NewModelTransferReconciler(clientset kubernetes.Interface, client mq.Client, cfg config.TransferConfig, storageCfg storage.Config) *ModelTransferReconciler {
	interval := cfg.ProgressInterval
	if interval <= 0 {
		interval = transferProgressInterval
	}
	return &ModelTransferReconciler{
		clientset:   clientset,
		client:      client,
		namespace:   firstNonEmptyTrim(cfg.Namespace, "taas-jobs"),
		image:       strings.TrimSpace(cfg.Image),
		saName:      firstNonEmptyTrim(cfg.ServiceAccount, "taas-transfer"),
		jobConfig:   cfg.Job,
		nodeSelect:  cfg.NodeSelector,
		tolerations: cfg.Tolerations,
		resources:   cfg.Resources,
		storage:     storageCfg,
		progress:    interval,
	}
}

// ApplyModelTransfer consumes one transfer request. Cancellation deletes
// the Job; a request ensures the Job exists (idempotent by name).
func (r *ModelTransferReconciler) ApplyModelTransfer(ctx context.Context, msg mq.Message) error {
	var spec transfer.Spec
	if err := json.Unmarshal(msg.Body, &spec); err != nil {
		return fmt.Errorf("controller: malformed transfer spec: %w", err)
	}
	if strings.TrimSpace(spec.ID) == "" {
		return fmt.Errorf("controller: transfer spec has no id")
	}
	jobName := transferJobName(spec.ID)
	if spec.Operation == transfer.OperationCancel {
		propagation := metav1.DeletePropagationBackground
		err := r.clientset.BatchV1().Jobs(r.namespace).Delete(ctx, jobName, metav1.DeleteOptions{PropagationPolicy: &propagation})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(r.image) == "" {
		return fmt.Errorf("controller: transfer.image is not configured")
	}
	if _, err := r.clientset.BatchV1().Jobs(r.namespace).Get(ctx, jobName, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	job, err := r.buildJob(&spec, jobName)
	if err != nil {
		return err
	}
	if _, err := r.clientset.BatchV1().Jobs(r.namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	logger.S().Infow("controller: transfer job created", "transfer", spec.ID, "job", jobName, "namespace", r.namespace)
	return nil
}

// Run polls managed transfer Jobs and publishes their state until ctx is
// cancelled.
func (r *ModelTransferReconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.progress)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.reportJobs(ctx); err != nil {
				logger.S().Warnw("controller: transfer job scan failed", "err", err)
			}
		}
	}
}

// reportJobs lists the Jobs this controller owns and publishes their state.
func (r *ModelTransferReconciler) reportJobs(ctx context.Context) error {
	jobs, err := r.clientset.BatchV1().Jobs(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: transferLabel + "=true"})
	if err != nil {
		return err
	}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		id := job.Labels[transferIDLabel]
		if id == "" {
			continue
		}
		status := transfer.Status{ID: id, JobName: job.Name, UpdatedAt: time.Now().UTC()}
		if progress, err := r.readProgress(ctx, id); err == nil && progress != nil {
			status.Bytes = progress.Bytes
			status.TotalBytes = progress.TotalBytes
			if progress.Message != "" && job.Status.Failed > 0 {
				status.Message = progress.Message
			}
		}
		switch {
		case job.Status.Succeeded > 0:
			status.State = transfer.StateSucceeded
		case job.Status.Failed > 0:
			status.State = transfer.StateFailed
			status.Message = jobFailureMessage(job)
		case job.Status.Active > 0:
			status.State = transfer.StateRunning
		default:
			status.State = transfer.StateQueued
		}
		body, err := json.Marshal(&status)
		if err != nil {
			return err
		}
		if err := r.client.Publish(ctx, mq.DefaultSubjects().ModelTransferStatus, body, nil); err != nil {
			return err
		}
	}
	return nil
}

// readProgress loads the ConfigMap the transfer Job writes. A missing
// ConfigMap only means the Job has not reported its first byte yet.
func (r *ModelTransferReconciler) readProgress(ctx context.Context, id string) (*transfer.Progress, error) {
	cm, err := r.clientset.CoreV1().ConfigMaps(r.namespace).Get(ctx, transferProgressName(id), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	raw := cm.Data["progress.json"]
	if raw == "" {
		return nil, nil
	}
	var progress transfer.Progress
	if err := json.Unmarshal([]byte(raw), &progress); err != nil {
		return nil, err
	}
	return &progress, nil
}

func transferProgressName(id string) string {
	name := "taas-transfer-progress-" + strings.ToLower(id)
	if len(name) > 253 {
		name = name[:253]
	}
	return name
}

// buildJob constructs the transfer Job. Filesystem backends are mounted as
// PVC volumes at their declared mount paths; object-store credentials are
// projected from their existing Secrets.
func (r *ModelTransferReconciler) buildJob(spec *transfer.Spec, name string) (*batchv1.Job, error) {
	env := []corev1.EnvVar{
		{Name: transferSpecEnv, Value: mustJSON(spec)},
		{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
		}},
	}
	volumes := []corev1.Volume{}
	mounts := []corev1.VolumeMount{}
	seenClaim := map[string]string{}
	seenMount := map[string]bool{}
	for role, backendName := range map[string]string{"SOURCE": spec.SourceBackend, "TARGET": spec.TargetBackend} {
		if backendName == "" {
			continue
		}
		backend, err := r.storage.Resolve(backendName)
		if err != nil {
			return nil, err
		}
		if backend.Type != storage.TypeFilesystem {
			env = append(env, objectStoreEnv(backend, role)...)
			continue
		}
		claim := strings.TrimSpace(backend.Filesystem.ExistingClaim)
		if claim == "" {
			return nil, fmt.Errorf("controller: filesystem backend %q has no existing claim", backendName)
		}
		volumeName := volumeNameFor(claim)
		if _, ok := seenClaim[claim]; !ok {
			seenClaim[claim] = volumeName
			volumes = append(volumes, corev1.Volume{
				Name: volumeName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim},
				},
			})
		}
		mountKey := volumeName + "|" + backend.Filesystem.MountPath
		if !seenMount[mountKey] {
			seenMount[mountKey] = true
			mounts = append(mounts, corev1.VolumeMount{Name: volumeName, MountPath: backend.Filesystem.MountPath})
		}
	}
	container := corev1.Container{
		Name:            transferContainerName,
		Image:           r.image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"/usr/local/bin/taas-transfer"},
		Env:             env,
		VolumeMounts:    mounts,
	}
	if spec.Operation == transfer.OperationDownload {
		container.Env = append(container.Env,
			corev1.EnvVar{Name: "TRANSFER_HUB_SOURCE", Value: spec.HubSource},
			corev1.EnvVar{Name: "TRANSFER_HUB_MODEL_ID", Value: spec.HubModelID},
		)
	}
	if resources := resourceRequirements(r.resources); resources != nil {
		container.Resources = *resources
	}
	podSpec := corev1.PodSpec{
		RestartPolicy:      corev1.RestartPolicyNever,
		ServiceAccountName: r.saName,
		Containers:         []corev1.Container{container},
		Volumes:            volumes,
	}
	if len(r.nodeSelect) > 0 {
		podSpec.NodeSelector = r.nodeSelect
	}
	for _, toleration := range r.tolerations {
		podSpec.Tolerations = append(podSpec.Tolerations, toToleration(toleration))
	}
	backoff := r.jobConfig.BackoffLimit
	deadline := r.jobConfig.ActiveDeadlineSeconds
	ttl := r.jobConfig.TTLSecondsAfterFinish
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.namespace,
			Labels: map[string]string{
				transferLabel:            "true",
				transferIDLabel:          spec.ID,
				"app.kubernetes.io/name": "taas-transfer",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &deadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
					transferLabel:   "true",
					transferIDLabel: spec.ID,
				}},
				Spec: podSpec,
			},
		},
	}
	return job, nil
}

// objectStoreEnv exposes one object-store backend's credentials and TLS
// settings to the Job.
func objectStoreEnv(backend storage.Backend, role string) []corev1.EnvVar {
	store := backend.ObjectStore
	env := []corev1.EnvVar{
		{Name: "TRANSFER_S3_ENDPOINT", Value: store.Endpoint},
		{Name: "TRANSFER_S3_REGION", Value: store.Region},
		{Name: "TRANSFER_S3_BUCKET", Value: store.Bucket},
		{Name: "TRANSFER_S3_PREFIX", Value: store.Prefix},
		{Name: "TRANSFER_S3_FORCE_PATH_STYLE", Value: fmt.Sprintf("%t", store.ForcePathStyle)},
		{Name: "TRANSFER_S3_INSECURE", Value: fmt.Sprintf("%t", store.TLS.InsecureSkipVerify)},
	}
	if store.Credentials.ExistingSecret == "" {
		return env
	}
	accessKey := firstNonEmptyTrim(store.Credentials.AccessKeyKey, "accessKey")
	secretKey := firstNonEmptyTrim(store.Credentials.SecretKeyKey, "secretKey")
	// Source and target can be different stores, so their credentials use
	// role-scoped names instead of the process-wide AWS variables.
	env = append(env,
		corev1.EnvVar{Name: "TRANSFER_" + role + "_ACCESS_KEY", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: store.Credentials.ExistingSecret}, Key: accessKey},
		}},
		corev1.EnvVar{Name: "TRANSFER_" + role + "_SECRET_KEY", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: store.Credentials.ExistingSecret}, Key: secretKey},
		}},
	)
	return env
}

// resourceRequirements converts the configured quantity strings. An empty
// map returns nil so the container keeps the namespace defaults.
func resourceRequirements(spec map[string]map[string]string) *corev1.ResourceRequirements {
	if len(spec) == 0 {
		return nil
	}
	out := &corev1.ResourceRequirements{}
	if values, ok := spec["requests"]; ok {
		out.Requests = corev1.ResourceList{}
		for name, value := range values {
			if q, err := resource.ParseQuantity(value); err == nil {
				out.Requests[corev1.ResourceName(name)] = q
			}
		}
	}
	if values, ok := spec["limits"]; ok {
		out.Limits = corev1.ResourceList{}
		for name, value := range values {
			if q, err := resource.ParseQuantity(value); err == nil {
				out.Limits[corev1.ResourceName(name)] = q
			}
		}
	}
	return out
}

func transferJobName(id string) string {
	name := "taas-transfer-" + strings.ToLower(id)
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, name)
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.Trim(name, "-")
}

func volumeNameFor(claim string) string {
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, strings.ToLower(claim))
	name = strings.Trim(name, "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return "weights-" + name
}

func jobFailureMessage(job *batchv1.Job) string {
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobFailed && condition.Message != "" {
			return condition.Message
		}
	}
	return "transfer job failed"
}

func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func toToleration(raw map[string]any) corev1.Toleration {
	toleration := corev1.Toleration{}
	if v, ok := raw["key"].(string); ok {
		toleration.Key = v
	}
	if v, ok := raw["operator"].(string); ok {
		toleration.Operator = corev1.TolerationOperator(v)
	}
	if v, ok := raw["value"].(string); ok {
		toleration.Value = v
	}
	if v, ok := raw["effect"].(string); ok {
		toleration.Effect = corev1.TaintEffect(v)
	}
	return toleration
}

func firstNonEmptyTrim(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
