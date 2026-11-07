package controller

import (
	"context"
	"encoding/json"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
)

// PodFailureDetector periodically scans the inference pods in the
// namespace and publishes a "failed" status for any service whose pod
// has a terminal failure (ImagePullBackOff, CrashLoopBackOff, OOMKilled,
// Unschedulable). It covers the window after a service is running: the
// initial deploy already fails fast through awaitEngineReady, but a pod
// that crashes later would otherwise leave the service "running" forever.
//
// The detector only reports failures; it never deletes resources, so a
// failed service keeps its StormService and pods for diagnosis.
type PodFailureDetector struct {
	clientset        kubernetes.Interface
	namespace        string
	interval         time.Duration
	statusPublisher  mq.Client
}

// NewPodFailureDetector builds a PodFailureDetector.
func NewPodFailureDetector(clientset kubernetes.Interface, namespace string, interval time.Duration, statusPublisher mq.Client) *PodFailureDetector {
	if namespace == "" {
		namespace = "taas-infer"
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &PodFailureDetector{
		clientset:       clientset,
		namespace:       namespace,
		interval:        interval,
		statusPublisher: statusPublisher,
	}
}

// Run scans on the interval until ctx is cancelled.
func (d *PodFailureDetector) Run(ctx context.Context) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	d.scanOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.scanOnce(ctx)
		}
	}
}

// scanOnce lists the inference pods and publishes a failed status for
// every service with a terminal pod failure. Errors are logged; the loop
// retries on the next tick.
func (d *PodFailureDetector) scanOnce(ctx context.Context) {
	if d.statusPublisher == nil {
		return
	}
	pods, err := d.clientset.CoreV1().Pods(d.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelServiceID,
	})
	if err != nil {
		logger.S().Warnw("controller: pod failure scan failed", "err", err)
		return
	}
	// Group pods by service id.
	byService := map[string][]corev1.Pod{}
	for _, pod := range pods.Items {
		svcID := pod.Labels[labelServiceID]
		if svcID == "" {
			continue
		}
		byService[svcID] = append(byService[svcID], pod)
	}
	for svcID, svcPods := range byService {
		if reason := terminalPodFailureIn(svcPods); reason != "" {
			d.reportFailure(ctx, svcID, reason)
		}
	}
}

// reportFailure publishes a failed status for the service. A publish
// failure is logged; the next scan retries.
func (d *PodFailureDetector) reportFailure(ctx context.Context, serviceID, reason string) {
	if len(reason) > 512 {
		reason = reason[:512]
	}
	if err := d.publishStatus(ctx, serviceID, "failed", nil, &reason); err != nil {
		logger.S().Warnw("controller: pod failure detector publish failed",
			"service_id", serviceID, "err", err)
		return
	}
	logger.S().Warnw("controller: inference service pod failed",
		"service_id", serviceID, "reason", reason)
}

// publishStatus publishes an observed-state report on the status subject.
func (d *PodFailureDetector) publishStatus(ctx context.Context, serviceID, state string, endpoints []string, failureReason *string) error {
	report := statusReport{
		ServiceID:     serviceID,
		State:         state,
		Endpoints:     endpoints,
		FailureReason: failureReason,
		ReportedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return d.statusPublisher.Publish(ctx, mq.DefaultSubjects().InferServiceStatus, body, nil)
}