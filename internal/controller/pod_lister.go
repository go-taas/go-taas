package controller

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"

	"github.com/go-taas/go-taas/pkg/k8s"
	"github.com/go-taas/go-taas/services/resourcemetrics"
)

// podLister resolves a service's running pods for the resource-metrics
// read path. Prometheus series are keyed by pod name, so the metrics page
// needs the names plus the masked replica index the console shows.
type podLister struct {
	client    *k8s.Client
	namespace string
}

// NewPodLister builds the metrics pod lister over the server's Kubernetes
// client. An empty namespace uses the shipped inference namespace, the
// same default the log fetcher applies: an empty namespace would list pods
// across the whole cluster and make the PromQL namespace matcher empty.
func NewPodLister(client *k8s.Client, namespace string) resourcemetrics.PodLister {
	if namespace == "" {
		namespace = defaultInferenceNamespace
	}
	return &podLister{client: client, namespace: namespace}
}

// ServicePods implements resourcemetrics.PodLister.
func (l *podLister) ServicePods(ctx context.Context, serviceID string) ([]resourcemetrics.ServicePod, error) {
	pods, err := listServicePods(ctx, l.client.Clientset(), l.client.Dynamic(), l.namespace, serviceID)
	if err != nil {
		return nil, fmt.Errorf("controller: list pods for service %s: %w", serviceID, err)
	}
	// The same ordering the log viewer uses, so "replica-2" is one pod on
	// every page.
	sort.Slice(pods, func(i, j int) bool {
		if pods[i].CreationTimestamp.Equal(&pods[j].CreationTimestamp) {
			return pods[i].Name < pods[j].Name
		}
		return pods[i].CreationTimestamp.Before(&pods[j].CreationTimestamp)
	})

	out := make([]resourcemetrics.ServicePod, 0, len(pods))
	for i := range pods {
		pod := &pods[i]
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		cpuRequestMilli, cpuLimitMilli, memoryLimitBytes := podBudget(pod)
		out = append(out, resourcemetrics.ServicePod{
			Name:             pod.Name,
			ReplicaIndex:     replicaLabel(len(out) + 1),
			CPURequestMilli:  cpuRequestMilli,
			CPULimitMilli:    cpuLimitMilli,
			MemoryLimitBytes: memoryLimitBytes,
		})
	}
	return out, nil
}

// podBudget sums the pod's container CPU requests/limits and memory
// limits. Memory has no request column on the page, so only its limit is
// collected.
func podBudget(pod *corev1.Pod) (cpuRequestMilli, cpuLimitMilli, memoryLimitBytes int64) {
	for _, container := range pod.Spec.Containers {
		if q, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
			cpuRequestMilli += q.MilliValue()
		}
		if q, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
			cpuLimitMilli += q.MilliValue()
		}
		if q, ok := container.Resources.Limits[corev1.ResourceMemory]; ok {
			memoryLimitBytes += q.Value()
		}
	}
	return cpuRequestMilli, cpuLimitMilli, memoryLimitBytes
}
