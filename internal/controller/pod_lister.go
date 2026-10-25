package controller

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
// client.
func NewPodLister(client *k8s.Client, namespace string) resourcemetrics.PodLister {
	return &podLister{client: client, namespace: namespace}
}

// ServicePods implements resourcemetrics.PodLister.
func (l *podLister) ServicePods(ctx context.Context, serviceID string) ([]resourcemetrics.ServicePod, error) {
	pods, err := l.client.Clientset().CoreV1().Pods(l.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: serviceLabelKey + "=" + serviceID,
	})
	if err != nil {
		return nil, fmt.Errorf("controller: list pods for service %s: %w", serviceID, err)
	}
	// The same ordering the log viewer uses, so "replica-2" is one pod on
	// every page.
	sort.Slice(pods.Items, func(i, j int) bool {
		if pods.Items[i].CreationTimestamp.Equal(&pods.Items[j].CreationTimestamp) {
			return pods.Items[i].Name < pods.Items[j].Name
		}
		return pods.Items[i].CreationTimestamp.Before(&pods.Items[j].CreationTimestamp)
	})

	out := make([]resourcemetrics.ServicePod, 0, len(pods.Items))
	for i := range pods.Items {
		pod := &pods.Items[i]
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
