package infer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	infersv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
)

// TestNormalizePodSchedulingDefaults covers the shape a request that says
// nothing gets: one device per pod, no CPU/memory request, normal priority.
func TestNormalizePodSchedulingDefaults(t *testing.T) {
	spec, err := NormalizePodScheduling(nil)
	require.NoError(t, err)
	assert.Equal(t, 1, spec.GPUPerPod)
	assert.Zero(t, spec.CPUMilli)
	assert.Zero(t, spec.MemoryMiB)
	assert.Equal(t, PriorityNormal, spec.Priority)

	empty, err := NormalizePodScheduling(&infersv1.PodScheduling{})
	require.NoError(t, err)
	assert.Equal(t, spec, empty, "an empty message means the same as no message")
}

// TestNormalizePodSchedulingAcceptsValidShape covers the values the console
// sends.
func TestNormalizePodSchedulingAcceptsValidShape(t *testing.T) {
	spec, err := NormalizePodScheduling(&infersv1.PodScheduling{
		GpuPerPod: 2,
		CpuCores:  4.5,
		MemoryMib: 16384,
		Priority:  PriorityHigh,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, spec.GPUPerPod)
	assert.Equal(t, int64(4500), spec.CPUMilli, "cores are stored as milli-cores")
	assert.Equal(t, int64(16384), spec.MemoryMiB)
	assert.Equal(t, PriorityHigh, spec.Priority)
}

// TestNormalizePodSchedulingRejectsBadValues keeps a typo out of the pod spec.
func TestNormalizePodSchedulingRejectsBadValues(t *testing.T) {
	cases := map[string]*infersv1.PodScheduling{
		"too many cards":   {GpuPerPod: maxGPUPerPod + 1},
		"negative cards":   {GpuPerPod: -1},
		"too much cpu":     {CpuCores: maxCPUPerPod},
		"negative cpu":     {CpuCores: -1},
		"too much memory":  {MemoryMib: maxMemoryMiB + 1},
		"negative memory":  {MemoryMib: -1},
		"unknown priority": {Priority: "urgent"},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizePodScheduling(req)
			require.Error(t, err)
		})
	}
}

// TestValidatePodsForTopology covers the divisibility rule: a multi-pod
// replica's devices must split evenly into pods.
func TestValidatePodsForTopology(t *testing.T) {
	multi := TopologySpec{Topology: TopologyMultiNode, TensorParallelSize: 4, PipelineParallelSize: 1}
	require.NoError(t, validatePodsForTopology(multi, PodScheduling{GPUPerPod: 2}))
	require.NoError(t, validatePodsForTopology(multi, PodScheduling{GPUPerPod: 1}))
	require.NoError(t, validatePodsForTopology(multi, PodScheduling{GPUPerPod: 4}))
	require.Error(t, validatePodsForTopology(multi, PodScheduling{GPUPerPod: 3}),
		"4 devices do not split into pods of 3")
	require.Error(t, validatePodsForTopology(multi, PodScheduling{GPUPerPod: 8}),
		"a pod cannot take more devices than the replica has")

	// A single-node replica takes whatever the pod is given: one pod, no
	// divisibility question.
	single := TopologySpec{Topology: TopologySingle, TensorParallelSize: 1, PipelineParallelSize: 1}
	require.NoError(t, validatePodsForTopology(single, PodScheduling{GPUPerPod: 4}))
}

// TestPodGroupSizeFor covers the pod count a replica spans.
func TestPodGroupSizeFor(t *testing.T) {
	multi := TopologySpec{Topology: TopologyMultiNode, TensorParallelSize: 8, PipelineParallelSize: 2}
	assert.Equal(t, 16, multi.PodGroupSizeFor(1))
	assert.Equal(t, 8, multi.PodGroupSizeFor(2))
	assert.Equal(t, 4, multi.PodGroupSizeFor(4))

	single := TopologySpec{Topology: TopologySingle, TensorParallelSize: 1, PipelineParallelSize: 1}
	assert.Equal(t, 1, single.PodGroupSizeFor(4))
}

// TestSchedulingOfDefaultsOlderRows proves a row written before the columns
// existed reads the way it actually deployed.
func TestSchedulingOfDefaultsOlderRows(t *testing.T) {
	spec := SchedulingOf(&InferenceService{})
	assert.Equal(t, 1, spec.GPUPerPod)
	assert.Equal(t, PriorityNormal, spec.Priority)

	spec = SchedulingOf(&InferenceService{GPUPerPod: 4, CPUMilli: 8000, MemoryMiB: 32768, Priority: PriorityLow})
	assert.Equal(t, 4, spec.GPUPerPod)
	assert.Equal(t, int64(8000), spec.CPUMilli)
	assert.Equal(t, PriorityLow, spec.Priority)
}
