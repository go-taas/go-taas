package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

// fakeAcceleratorReader returns a fixed GPU utilization for a pod.
type fakeAcceleratorReader struct {
	util float64
	has  bool
}

func (f *fakeAcceleratorReader) GPUUtilizationForPod(_ context.Context, _ string) (float64, bool) {
	return f.util, f.has
}

// newMetricsClient builds a fake metrics client whose PodMetricses list
// returns the given pod metrics (the metrics fake's tracker does not
// register the "pods" list reactor, so a custom reactor is used).
func newMetricsClient(metrics []metricsv1beta1.PodMetrics) *metricsfake.Clientset {
	mc := metricsfake.NewSimpleClientset()
	mc.PrependReactor("list", "pods", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		return true, &metricsv1beta1.PodMetricsList{Items: metrics}, nil
	})
	return mc
}

func TestK8sResourceSampleProvider(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc-1-replica-1-abc",
			Namespace: "taas-infer",
			Labels:    map[string]string{labelServiceID: "svc-1", labelReplicaIndex: "replica-1"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc-1-replica-2-def",
			Namespace: "taas-infer",
			Labels:    map[string]string{labelServiceID: "svc-1", labelReplicaIndex: "replica-2"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	})

	metricsClient := newMetricsClient([]metricsv1beta1.PodMetrics{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "svc-1-replica-1-abc", Namespace: "taas-infer"},
			Containers: []metricsv1beta1.ContainerMetrics{
				{Name: "engine", Usage: corev1.ResourceList{
					corev1.ResourceCPU:    *resource.NewMilliQuantity(500, resource.DecimalSI),
					corev1.ResourceMemory: *resource.NewQuantity(1024, resource.BinarySI),
				}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "svc-1-replica-2-def", Namespace: "taas-infer"},
			Containers: []metricsv1beta1.ContainerMetrics{
				{Name: "engine", Usage: corev1.ResourceList{
					corev1.ResourceCPU:    *resource.NewMilliQuantity(700, resource.DecimalSI),
					corev1.ResourceMemory: *resource.NewQuantity(2048, resource.BinarySI),
				}},
			},
		},
	})

	accel := &fakeAcceleratorReader{util: 42, has: true}
	provider := NewK8sResourceSampleProvider(clientset, metricsClient, "taas-infer", accel)

	samples, err := provider.SampleServices(context.Background())
	require.NoError(t, err)
	require.Len(t, samples, 2)
	assert.Equal(t, "svc-1", samples[0].ServiceID)
	assert.Equal(t, "replica-1", samples[0].ReplicaIndex)
	// 500 millicores / 1 core nominal = 50%.
	assert.InDelta(t, 50, samples[0].CPUPercent, 0.001)
	assert.Equal(t, int64(1024), samples[0].MemoryBytes)
	require.NotNil(t, samples[0].GPUPercent)
	assert.InDelta(t, 42, *samples[0].GPUPercent, 0.001)
}

func TestK8sResourceSampleProviderNoGPU(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc-1-replica-1-abc",
			Namespace: "taas-infer",
			Labels:    map[string]string{labelServiceID: "svc-1", labelReplicaIndex: "replica-1"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	})
	metricsClient := newMetricsClient([]metricsv1beta1.PodMetrics{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "svc-1-replica-1-abc", Namespace: "taas-infer"},
			Containers: []metricsv1beta1.ContainerMetrics{
				{Name: "engine", Usage: corev1.ResourceList{
					corev1.ResourceCPU:    *resource.NewMilliQuantity(500, resource.DecimalSI),
					corev1.ResourceMemory: *resource.NewQuantity(1024, resource.BinarySI),
				}},
			},
		},
	})
	// No accelerator reader -> GPU is nil.
	provider := NewK8sResourceSampleProvider(clientset, metricsClient, "taas-infer", nil)

	samples, err := provider.SampleServices(context.Background())
	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.Nil(t, samples[0].GPUPercent)
}

func TestK8sResourceSampleProviderSkipsNonRunning(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc-1-replica-1-abc",
			Namespace: "taas-infer",
			Labels:    map[string]string{labelServiceID: "svc-1", labelReplicaIndex: "replica-1"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	})
	metricsClient := newMetricsClient(nil)
	provider := NewK8sResourceSampleProvider(clientset, metricsClient, "taas-infer", nil)

	samples, err := provider.SampleServices(context.Background())
	require.NoError(t, err)
	assert.Len(t, samples, 0)
}