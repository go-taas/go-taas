package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// TestSlicePodShape pins the contract a shared-GPU pod owes HAMi: the
// device resource (without it no node advertises the request), the memory
// and compute resources HAMi slices on, the pool's node selector, the
// pool's placement policy, and HAMi's scheduler (the only scheduler that
// picks the physical card and writes the allocation annotation).
func TestSlicePodShape(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.PoolID = "pool-a"
	evt.SliceTemplateID = "small"
	evt.SliceMemoryMiB = 8192
	evt.SliceComputePct = 50
	evt.SlicePoolLabelKey = "go-taas.com/vgpu-pool"
	evt.SlicePoolLabelValue = "bench"
	evt.SlicePlacementPolicy = "spread"
	evt.CPUMilli = 500

	r := testReconciler()
	r.SetVGPUConfig("hami.io/gpu-scheduler-policy", "binpack", "hami-scheduler")

	spec, annotations := r.buildPodTemplate(context.Background(), evt, serveRole{Name: serveRoleWorker, PodGroupSize: 1})
	require.Len(t, spec.Containers, 1)
	res := spec.Containers[0].Resources

	qty := func(list map[corev1.ResourceName]resource.Quantity, name string) resource.Quantity {
		return list[corev1.ResourceName(name)]
	}
	gpu := qty(res.Limits, "nvidia.com/gpu")
	mem := qty(res.Limits, "nvidia.com/gpumem")
	cores := qty(res.Limits, "nvidia.com/gpucores")
	cpu := qty(res.Requests, "cpu")
	gpuReq := qty(res.Requests, "nvidia.com/gpu")
	assert.Equal(t, int64(1), gpu.Value(), "one vGPU unit")
	// HAMi reads the memory resource as a plain MiB count.
	assert.Equal(t, int64(8192), mem.Value())
	assert.Equal(t, int64(50), cores.Value())
	assert.Equal(t, int64(500), cpu.MilliValue(), "the base requests survive slicing")
	assert.Equal(t, int64(1), gpuReq.Value())

	assert.Equal(t, "hami-scheduler", spec.SchedulerName)
	assert.Equal(t, "spread", annotations["hami.io/gpu-scheduler-policy"])
	assert.Equal(t, "bench", spec.NodeSelector["go-taas.com/vgpu-pool"])
}

// TestWholeCardPodStaysOnPlatformScheduler keeps the historical shape when no
// HAMi scheduler is configured: a whole-card service asks for the device only,
// keeps the platform scheduler, and carries no HAMi annotation.
func TestWholeCardPodStaysOnPlatformScheduler(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")

	r := testReconciler()
	r.SetScheduling(SchedulerVolcano, "", "", "", "")

	spec, annotations := r.buildPodTemplate(context.Background(), evt, serveRole{Name: serveRoleWorker, PodGroupSize: 1})
	res := spec.Containers[0].Resources
	q := res.Limits["nvidia.com/gpu"]
	assert.Equal(t, "1", q.String())
	_, hasMem := res.Limits["nvidia.com/gpumem"]
	assert.False(t, hasMem)
	assert.Empty(t, annotations["hami.io/gpu-scheduler-policy"])
	assert.Equal(t, "volcano", spec.SchedulerName)
}

// TestWholeCardPodUsesHAMiSchedulerWhenHAMiOwnsTheDevice pins the other half
// of the contract: once HAMi's device plugin publishes the accelerator
// resource, a whole-card pod must be placed by HAMi's scheduler too, or the
// allocation annotation is missing and Allocate() fails.
func TestWholeCardPodUsesHAMiSchedulerWhenHAMiOwnsTheDevice(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")

	r := testReconciler()
	r.SetScheduling(SchedulerVolcano, "", "", "", "")
	r.SetVGPUConfig("hami.io/gpu-scheduler-policy", "binpack", "hami-scheduler")

	spec, annotations := r.buildPodTemplate(context.Background(), evt, serveRole{Name: serveRoleWorker, PodGroupSize: 1})
	assert.Equal(t, "hami-scheduler", spec.SchedulerName)
	assert.Empty(t, annotations["hami.io/gpu-scheduler-policy"], "a whole card has no placement policy")
}

// TestGangSchedulingNeedsVolcanoAndNoHAMi keeps the pre-HAMi gang path and
// pins that a HAMi-managed accelerator turns it off.
func TestGangSchedulingNeedsVolcanoAndNoHAMi(t *testing.T) {
	r := testReconciler()
	r.dynamic = newFakeDynamic()
	r.SetScheduling(SchedulerVolcano, "", "", "", "")
	assert.True(t, r.gangScheduling())

	r.SetVGPUConfig("hami.io/gpu-scheduler-policy", "binpack", "hami-scheduler")
	assert.False(t, r.gangScheduling(), "no PodGroup for pods Volcano will never place")
}

// TestSlicePodReservesEngineHeadroom pins the gpu_memory_utilization a
// shared-GPU vLLM service is started with: the device it sees is already
// the slice, so the default 0.92 leaves less headroom than the engine's
// own activation and CUDA-graph memory need on a small slice.
func TestSlicePodReservesEngineHeadroom(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.PoolID = "pool-a"
	evt.SliceTemplateID = "quarter"
	evt.SliceMemoryMiB = 6144
	evt.SliceComputePct = 25

	r := testReconciler()
	r.SetVGPUConfig("hami.io/gpu-scheduler-policy", "binpack", "hami-scheduler")

	spec, _ := r.buildPodTemplate(context.Background(), evt, serveRole{Name: serveRoleWorker, PodGroupSize: 1})
	assert.Contains(t, strings.Join(spec.Containers[0].Command, " "), "--gpu-memory-utilization 0.88")

	evt.EngineArgs = map[string]string{"gpu-memory-utilization": "0.70"}
	spec, _ = r.buildPodTemplate(context.Background(), evt, serveRole{Name: serveRoleWorker, PodGroupSize: 1})
	cmd := strings.Join(spec.Containers[0].Command, " ")
	assert.Contains(t, cmd, "--gpu-memory-utilization 0.70")
	assert.NotContains(t, cmd, "0.88")
}
