package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// schedulingReconciler is a reconciler with Volcano scheduling configured, so
// the gang and priority paths are exercised.
func schedulingReconciler() *k8sReconciler {
	r := testReconciler()
	// Gang scheduling is only on when the controller can write PodGroups,
	// which is what the dynamic client is for.
	r.dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		k8sruntime.NewScheme(),
		map[schema.GroupVersionResource]string{podGroupGVR: "PodGroupList"},
	)
	r.SetScheduling(SchedulerVolcano, "high-priority", "normal-priority", "low-priority", "")
	return r
}

// TestPodSchedulingSetsSchedulerAndPriority proves the pod asks for Volcano and
// carries the PriorityClass its service priority maps onto.
func TestPodSchedulingSetsSchedulerAndPriority(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Priority = "high"
	spec, _ := schedulingReconciler().buildPodTemplate(context.Background(), evt, stormRole{Name: stormRoleWorker, PodGroupSize: 1})
	assert.Equal(t, SchedulerVolcano, spec.SchedulerName)
	assert.Equal(t, "high-priority", spec.PriorityClassName)

	evt.Priority = "low"
	spec, _ = schedulingReconciler().buildPodTemplate(context.Background(), evt, stormRole{Name: stormRoleWorker, PodGroupSize: 1})
	assert.Equal(t, "low-priority", spec.PriorityClassName)

	// An empty priority is normal, not "no class".
	evt.Priority = ""
	spec, _ = schedulingReconciler().buildPodTemplate(context.Background(), evt, stormRole{Name: stormRoleWorker, PodGroupSize: 1})
	assert.Equal(t, "normal-priority", spec.PriorityClassName)

	// Without a configured scheduler the pod keeps the cluster default.
	plain, _ := testReconciler().buildPodTemplate(context.Background(), evt, stormRole{Name: stormRoleWorker, PodGroupSize: 1})
	assert.Empty(t, plain.SchedulerName)
	assert.Empty(t, plain.PriorityClassName)
}

// TestPodResourcesRequestsCardsCPUAndMemory covers the per-pod shape.
func TestPodResourcesRequestsCardsCPUAndMemory(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.GPUPerPod = 2
	evt.CPUMilli = 4500
	evt.MemoryMiB = 16384

	spec, _ := testReconciler().buildPodTemplate(context.Background(), evt, stormRole{Name: stormRoleWorker, PodGroupSize: 1})
	res := spec.Containers[0].Resources
	assert.Equal(t, resource.MustParse("2"), res.Limits[corev1.ResourceName("nvidia.com/gpu")])
	assert.True(t, res.Requests[corev1.ResourceCPU].Equal(resource.MustParse("4500m")))
	assert.True(t, res.Requests[corev1.ResourceMemory].Equal(resource.MustParse("16384Mi")))
	// CPU and memory are requests only: a serving engine must be able to
	// burst while loading weights.
	_, hasCPULimit := res.Limits[corev1.ResourceCPU]
	assert.False(t, hasCPULimit)

	// Zero CPU/memory means "no request", which is the historical shape.
	plain := testChangeEvent("upsert", "demo")
	spec, _ = testReconciler().buildPodTemplate(context.Background(), plain, stormRole{Name: stormRoleWorker, PodGroupSize: 1})
	res = spec.Containers[0].Resources
	_, hasCPU := res.Requests[corev1.ResourceCPU]
	assert.False(t, hasCPU)
	assert.Equal(t, resource.MustParse("1"), res.Limits[corev1.ResourceName("nvidia.com/gpu")])
}

// TestWorkerPodsSplitsByGPUPerPod covers the pod count a multi-pod replica
// spans when each pod takes several devices.
func TestWorkerPodsSplitsByGPUPerPod(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Topology = topologyMultiNode
	evt.TensorParallelSize = 8
	evt.PipelineParallelSize = 1

	assert.Equal(t, int32(8), workerPods(evt, topologyMultiNode))
	evt.GPUPerPod = 2
	assert.Equal(t, int32(4), workerPods(evt, topologyMultiNode))
	evt.GPUPerPod = 8
	assert.Equal(t, int32(1), workerPods(evt, topologyMultiNode))

	// A single-node replica is always one pod, whatever it asks for.
	evt.Topology = "single"
	assert.Equal(t, int32(1), workerPods(evt, "single"))
}

// TestGangPodsJoinTheirPodGroup proves the pods carry the annotation Volcano
// needs to bind them into their group.
func TestGangPodsJoinTheirPodGroup(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	storm, err := schedulingReconciler().buildStormService(context.Background(), evt)
	require.NoError(t, err)

	roles := stormRolesOf(t, storm)
	require.NotEmpty(t, roles)
	annotations := roles[0]["template"].(map[string]any)["metadata"].(map[string]any)["annotations"].(map[string]any)
	assert.Equal(t, "demo", annotations[podGroupAnnotation])

	// Without Volcano the annotation would point at a group nobody reads.
	plain, err := testReconciler().buildStormService(context.Background(), evt)
	require.NoError(t, err)
	roles = stormRolesOf(t, plain)
	if ann, ok := roles[0]["template"].(map[string]any)["metadata"].(map[string]any)["annotations"].(map[string]any); ok {
		_, present := ann[podGroupAnnotation]
		assert.False(t, present)
	}
}

// TestBuildPodGroup covers the gang definition: one replica's pods, its
// priority and the resources it reserves.
func TestBuildPodGroup(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Topology = topologyMultiNode
	evt.TensorParallelSize = 4
	evt.PipelineParallelSize = 1
	evt.GPUPerPod = 2
	evt.CPUMilli = 4000
	evt.MemoryMiB = 8192
	evt.Priority = "high"

	group := schedulingReconciler().buildPodGroup(evt)
	assert.Equal(t, "PodGroup", group.GetKind())
	assert.Equal(t, "scheduling.volcano.sh/v1beta1", group.GetAPIVersion())
	assert.Equal(t, "demo", group.GetName())
	assert.Equal(t, reconcileNamespace, group.GetNamespace())

	spec, found, err := unstructured.NestedMap(group.Object, "spec")
	require.NoError(t, err)
	require.True(t, found)
	assert.EqualValues(t, 2, spec["minMember"], "4 devices in pods of 2 is 2 pods")
	assert.Equal(t, "high-priority", spec["priorityClassName"])

	minResources, found, err := unstructured.NestedMap(spec, "minResources")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "4", minResources["nvidia.com/gpu"], "2 cards x 2 pods")
	assert.Equal(t, "8", minResources["cpu"], "4 cores x 2 pods")
	assert.Equal(t, "16Gi", minResources["memory"], "8 GiB x 2 pods")
}

// TestBuildPodGroupPDCountsBothRoles proves a PD service's gang is prefill plus
// decode: half a PD pair serves nothing.
func TestBuildPodGroupPDCountsBothRoles(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Topology = topologyPD
	evt.TensorParallelSize = 2
	evt.PipelineParallelSize = 1
	evt.PrefillReplicas = 1
	evt.DecodeReplicas = 1

	group := schedulingReconciler().buildPodGroup(evt)
	spec, _, err := unstructured.NestedMap(group.Object, "spec")
	require.NoError(t, err)
	assert.EqualValues(t, 4, spec["minMember"], "2 prefill pods + 2 decode pods")
}

// TestPriorityClassForUnknownPriority keeps a pod from naming a class the
// cluster does not have.
func TestPriorityClassForUnknownPriority(t *testing.T) {
	r := testReconciler()
	r.SetScheduling(SchedulerVolcano, "high-priority", "", "", "")
	assert.Equal(t, "high-priority", r.priorityClassFor("high"))
	assert.Empty(t, r.priorityClassFor("normal"), "an unmapped class leaves the cluster default")
	assert.Empty(t, r.priorityClassFor("nonsense"))
}
