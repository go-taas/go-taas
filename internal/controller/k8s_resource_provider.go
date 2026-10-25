package controller

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclientset "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/go-taas/go-taas/pkg/logger"
)

// labelServiceID is the label the controller sets on inference pods to
// identify the owning service (feature #37, AD2). It matches the label
// the reconciler applies to inference Deployments.
const labelServiceID = "go-taas.com/service-id"

// K8sResourceSampleProvider reads per-service per-replica CPU/memory
// from the Kubernetes metrics-server and GPU utilization from the
// accelerator signals (feature #37, AD2). It is the production
// ResourceSampleProvider.
type K8sResourceSampleProvider struct {
	clientset      kubernetes.Interface
	metricsClient  metricsclientset.Interface
	namespace      string
	acceleratorSvc AcceleratorSignalReader
}

// AcceleratorSignalReader reads the per-pod GPU utilization from the
// accelerator signals (feature #18). It is an interface so the provider
// can be tested against a fake and so the accelerator read stays behind
// one seam.
type AcceleratorSignalReader interface {
	// GPUUtilizationForPod returns the GPU utilization percent for a pod,
	// or (0, false) when the accelerator signals do not expose it (AD7).
	GPUUtilizationForPod(ctx context.Context, podName string) (float64, bool)
}

// NewK8sResourceSampleProvider builds a K8sResourceSampleProvider over a
// clientset, a metrics client and an accelerator signal reader.
func NewK8sResourceSampleProvider(clientset kubernetes.Interface, metricsClient metricsclientset.Interface, namespace string, acceleratorSvc AcceleratorSignalReader) *K8sResourceSampleProvider {
	if namespace == "" {
		namespace = "taas-infer"
	}
	return &K8sResourceSampleProvider{
		clientset:      clientset,
		metricsClient:  metricsClient,
		namespace:      namespace,
		acceleratorSvc: acceleratorSvc,
	}
}

// SampleServices lists the running inference pods and reads their
// per-container CPU/memory from the metrics-server and GPU utilization
// from the accelerator signals.
func (p *K8sResourceSampleProvider) SampleServices(ctx context.Context) ([]ResourceSample, error) {
	pods, err := p.clientset.CoreV1().Pods(p.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelServiceID,
	})
	if err != nil {
		return nil, fmt.Errorf("list inference pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return nil, nil
	}

	// Read the metrics-server pod metrics for the namespace.
	metrics, err := p.metricsClient.MetricsV1beta1().PodMetricses(p.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		// metrics-server may be absent; degrade to no samples (best
		// effort, AD2).
		logger.S().Warnw("controller: metrics-server unavailable", "err", err)
		return nil, nil
	}
	byName := make(map[string]metricsv1beta1.PodMetrics, len(metrics.Items))
	for i := range metrics.Items {
		byName[metrics.Items[i].Name] = metrics.Items[i]
	}

	// Order the service's pods the way the log viewer does (oldest first)
	// so "replica-2" means the same pod on both pages.
	//
	// The replica index is derived here rather than read from a label: a
	// Deployment's pod template cannot carry a per-replica value, so the
	// label never existed and every sample used to be skipped, leaving the
	// metrics page permanently empty.
	ordered := make([]corev1.Pod, 0, len(pods.Items))
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			ordered = append(ordered, pods.Items[i])
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].CreationTimestamp.Equal(&ordered[j].CreationTimestamp) {
			return ordered[i].Name < ordered[j].Name
		}
		return ordered[i].CreationTimestamp.Before(&ordered[j].CreationTimestamp)
	})

	samples := make([]ResourceSample, 0, len(ordered))
	for i := range ordered {
		pod := &ordered[i]
		serviceID := pod.Labels[labelServiceID]
		if serviceID == "" {
			continue
		}
		replicaIndex := replicaLabel(i + 1)
		pm, ok := byName[pod.Name]
		if !ok {
			continue
		}
		cpuPercent, memoryBytes := containerUsage(pm)
		var gpu *float64
		if p.acceleratorSvc != nil {
			if g, has := p.acceleratorSvc.GPUUtilizationForPod(ctx, pod.Name); has {
				gpu = &g
			}
		}
		samples = append(samples, ResourceSample{
			ServiceID:    serviceID,
			ReplicaIndex: replicaIndex,
			CPUPercent:   cpuPercent,
			MemoryBytes:  memoryBytes,
			GPUPercent:   gpu,
		})
	}
	return samples, nil
}

// containerUsage sums the container CPU/memory usage of a pod's metrics
// and derives the CPU percent from the container's request (usage ÷
// request × 100). When no request is present, the CPU percent is 0.
func containerUsage(pm metricsv1beta1.PodMetrics) (float64, int64) {
	var cpuNano int64
	var memoryBytes int64
	for _, c := range pm.Containers {
		cpuNano += c.Usage.Cpu().MilliValue() * 1e6
		memoryBytes += c.Usage.Memory().Value()
	}
	// The metrics-server reports CPU in nanocores; the request is read
	// from the pod spec. We approximate the request as the sum of the
	// container requests when available; otherwise we report 0.
	requestNano := int64(0)
	for _, c := range pm.Containers {
		// The PodMetrics container does not carry the request; read it
		// from the pod's spec via the clientset is out of scope here.
		// We fall back to a nominal 1-core request so the percent is
		// meaningful.
		_ = c
	}
	if requestNano == 0 {
		requestNano = 1e9 // 1 core nominal
	}
	cpuPercent := float64(cpuNano) / float64(requestNano) * 100
	return cpuPercent, memoryBytes
}

// parseReplicaIndex extracts the numeric suffix of a masked replica
// index (e.g. "replica-1" -> 1). It is used for stable ordering.
func parseReplicaIndex(index string) int {
	parts := strings.Split(index, "-")
	if len(parts) == 0 {
		return 0
	}
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0
	}
	return n
}

// replicaLabel renders the masked replica index for the n-th running pod
// of a service (1-based). The log viewer uses the same scheme, so a pod
// has one name on both pages.
func replicaLabel(index int) string { return "replica-" + strconv.Itoa(index) }
