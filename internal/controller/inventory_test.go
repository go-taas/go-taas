package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/go-taas/go-taas/pkg/mq"
)

func TestNodeToInventory(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node-1",
			UID:  "uid-1",
			Labels: map[string]string{
				labelAcceleratorVendor:   "nvidia",
				labelDriverVersion:       "535.104.05",
				labelDevicePlugin:        "healthy",
				"kubernetes.io/hostname": "gpu-node-1",
			},
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
			Allocatable: corev1.ResourceList{
				"nvidia.com/gpu": resource.MustParse("8"),
			},
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}},
		},
	}

	inv := nodeToInventory(node, nil)
	assert.Equal(t, "uid-1", inv.NodeID)
	assert.Equal(t, "gpu-node-1", inv.Name)
	assert.Equal(t, "nvidia", inv.Vendor)
	assert.Equal(t, "ready", inv.Readiness)
	assert.Equal(t, "healthy", inv.DevicePluginState)
	assert.Equal(t, "535.104.05", inv.DriverVersion)
	assert.Equal(t, []string{"gpu"}, inv.CardTypes)
	require.Len(t, inv.Resources, 1)
	assert.EqualValues(t, 8, inv.Resources[0].Allocatable)
	assert.EqualValues(t, 8, inv.GPUsFree)
	require.Len(t, inv.Taints, 1)
	assert.Equal(t, "dedicated=gpu:NoSchedule", inv.Taints[0])
	// Only accelerator-relevant labels are kept.
	assert.Equal(t, "nvidia", inv.Labels[labelAcceleratorVendor])
	assert.NotContains(t, inv.Labels, "kubernetes.io/hostname")
}

func TestNodeToInventoryUnspecifiedVendor(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "plain-node",
			UID:  "uid-2",
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
			},
		},
	}
	inv := nodeToInventory(node, nil)
	assert.Equal(t, "unspecified", inv.Vendor)
	assert.Equal(t, "not_ready", inv.Readiness)
	assert.Equal(t, "unknown", inv.DevicePluginState)
	assert.Empty(t, inv.CardTypes)
}

func TestInventoryCollectorPublishesSnapshot(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node-1",
			UID:  "uid-1",
			Labels: map[string]string{
				labelAcceleratorVendor: "nvidia",
				labelDriverVersion:     "535",
				labelDevicePlugin:      "healthy",
			},
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Allocatable: corev1.ResourceList{
				"nvidia.com/gpu": resource.MustParse("8"),
			},
		},
	})

	bus := mq.NewFake()
	collector := NewInventoryCollector(client, bus, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Subscribe first so the publish is delivered.
	var got []mq.Message
	require.NoError(t, bus.Subscribe(ctx, mq.DefaultSubjects().AcceleratorInventory, func(msg mq.Message) error {
		got = append(got, msg)
		return nil
	}))

	go collector.Run(ctx)

	require.Eventually(t, func() bool {
		return len(got) > 0
	}, 2*time.Second, 10*time.Millisecond)

	var snap inventorySnapshot
	require.NoError(t, json.Unmarshal(got[0].Body, &snap))
	require.Len(t, snap.Nodes, 1)
	assert.Equal(t, "gpu-node-1", snap.Nodes[0].Name)
	assert.Equal(t, "nvidia", snap.Nodes[0].Vendor)
	assert.Equal(t, "ready", snap.Nodes[0].Readiness)
	assert.False(t, snap.ReportedAt.IsZero())
}

func TestReadinessFromNodeUnknown(t *testing.T) {
	// No Ready condition -> unknown.
	node := &corev1.Node{}
	assert.Equal(t, "unknown", readinessFromNode(node))

	// Ready condition with unknown status -> unknown.
	node = &corev1.Node{
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionUnknown}},
		},
	}
	assert.Equal(t, "unknown", readinessFromNode(node))
}

func TestDevicePluginFromNodeFallbacks(t *testing.T) {
	// No label but accelerator extended resources -> healthy.
	node := &corev1.Node{
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("8")},
		},
	}
	assert.Equal(t, "healthy", devicePluginFromNode(node, map[string]string{}))

	// No label and no accelerator resources -> unknown.
	node = &corev1.Node{}
	assert.Equal(t, "unknown", devicePluginFromNode(node, map[string]string{}))

	// Unknown label value -> unknown.
	assert.Equal(t, "unknown", devicePluginFromNode(node, map[string]string{labelDevicePlugin: "bogus"}))
}

func TestCardTypeFromResource(t *testing.T) {
	ct, ok := cardTypeFromResource("nvidia.com/gpu")
	assert.True(t, ok)
	assert.Equal(t, "gpu", ct)

	ct, ok = cardTypeFromResource("iluvatar.com/gpu")
	assert.True(t, ok)
	assert.Equal(t, "gpu", ct)

	ct, ok = cardTypeFromResource("metax.com/gpu")
	assert.True(t, ok)
	assert.Equal(t, "gpu", ct)

	// A non-accelerator resource is not a card type.
	_, ok = cardTypeFromResource("cpu")
	assert.False(t, ok)
}

func TestNewInventoryCollectorDefaultInterval(t *testing.T) {
	bus := mq.NewFake()
	collector := NewInventoryCollector(fake.NewSimpleClientset(), bus, 0)
	assert.Equal(t, 30*time.Second, collector.interval)
}

func TestInventoryCollectorSkipsOnListError(t *testing.T) {
	// A failing node list causes the collector to log and skip the
	// publish (best-effort telemetry).
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "nodes", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("kube api down")
	})
	bus := mq.NewFake()
	collector := NewInventoryCollector(client, bus, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got []mq.Message
	require.NoError(t, bus.Subscribe(ctx, mq.DefaultSubjects().AcceleratorInventory, func(msg mq.Message) error {
		got = append(got, msg)
		return nil
	}))

	collector.collectAndPublish(context.Background())
	assert.Empty(t, got, "no snapshot must be published when the node list fails")
}

// A node that advertises the NVIDIA device-plugin signals but has no
// go-taas.com/accelerator label is classified as nvidia, not unspecified
// (the sd4090 scenario).
func TestNodeToInventoryInfersVendorFromDevicePlugin(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		alloc  corev1.ResourceList
		want   string
	}{
		{
			name: "nvidia gpu product label",
			labels: map[string]string{
				"nvidia.com/gpu.product": "NVIDIA-GeForce-RTX-4090",
			},
			want: "nvidia",
		},
		{
			name:   "nvidia extended resource",
			labels: map[string]string{},
			alloc: corev1.ResourceList{
				corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("2"),
			},
			want: "nvidia",
		},
		{
			name: "iluvatar label",
			labels: map[string]string{
				"iluvatar.ai/gpu.present": "true",
			},
			want: "iluvatar",
		},
		{
			name: "metax resource",
			alloc: corev1.ResourceList{
				corev1.ResourceName("metax-tech.com/gpu"): resource.MustParse("1"),
			},
			want: "metax",
		},
		{
			name:   "explicit label wins",
			labels: map[string]string{labelAcceleratorVendor: "metax", "nvidia.com/gpu.product": "x"},
			want:   "metax",
		},
		{
			name:   "no accelerator signal",
			labels: map[string]string{"kubernetes.io/hostname": "plain"},
			want:   "unspecified",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "n", UID: "u", Labels: c.labels},
				Status:     corev1.NodeStatus{Allocatable: c.alloc},
			}
			assert.Equal(t, c.want, nodeToInventory(node, nil).Vendor)
		})
	}
}

// The sd4090 node's allocatable nvidia.com/gpu counts as a gpu card type.
func TestCardTypeFromResourceVendors(t *testing.T) {
	for _, name := range []string{
		"nvidia.com/gpu", "iluvatar.ai/gpu", "iluvatar.com/gpu",
		"metax-tech.com/gpu", "metax.com/gpu",
	} {
		card, ok := cardTypeFromResource(corev1.ResourceName(name))
		assert.True(t, ok, name)
		assert.Equal(t, "gpu", card, name)
	}
	_, ok := cardTypeFromResource("example.com/other")
	assert.False(t, ok)
}

// TestNodeToInventoryCardModel verifies the reported card type is the card
// model (the vendor's product label), not the bare device-plugin resource
// family: a compatibility matrix keyed by "gpu" cannot distinguish an A800
// from a 4090, which is the whole point of the dimension.
func TestNodeToInventoryCardModel(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sd4090",
			UID:  "uid-4090",
			Labels: map[string]string{
				"nvidia.com/gpu.product": "NVIDIA-GeForce-RTX-4090",
				"gpu-vendor":             "nvidia",
			},
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Allocatable: corev1.ResourceList{
				"nvidia.com/gpu": resource.MustParse("2"),
			},
		},
	}
	inv := nodeToInventory(node, nil)
	assert.Equal(t, "nvidia", inv.Vendor)
	assert.Equal(t, []string{"NVIDIA-GeForce-RTX-4090"}, inv.CardTypes)
	require.Len(t, inv.Resources, 1)
	assert.Equal(t, "NVIDIA-GeForce-RTX-4090", inv.Resources[0].CardType,
		"the resource row is keyed by the card model too")
	assert.EqualValues(t, 2, inv.Resources[0].Allocatable)
}

// TestNodeToInventoryCardTypeFallsBack verifies a node whose vendor
// publishes no product label keeps the resource family as its card type
// rather than an empty dimension.
func TestNodeToInventoryCardTypeFallsBack(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "metax-1", UID: "uid-metax",
			Labels: map[string]string{"metax-tech.com/gpu.present": "true"},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{"metax-tech.com/gpu": resource.MustParse("1")},
		},
	}
	inv := nodeToInventory(node, nil)
	assert.Equal(t, "metax", inv.Vendor)
	assert.Equal(t, []string{"gpu"}, inv.CardTypes)
}

// TestDriverVersionFromNode covers the label conventions a real cluster
// carries.
//
// Only `go-taas.com/driver-version` was read at first, and no cluster sets it;
// NVIDIA's GPU Feature Discovery publishes the version under
// `nvidia.com/cuda.driver-version.full` instead. The version therefore stayed
// empty, and the health rule — which required it — reported "degraded" on
// every healthy node.
func TestDriverVersionFromNode(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{
			name:   "platform label wins",
			labels: map[string]string{labelDriverVersion: "535.104.05", "nvidia.com/cuda.driver-version.full": "595.91.07"},
			want:   "535.104.05",
		},
		{
			name:   "gpu feature discovery full version",
			labels: map[string]string{"nvidia.com/cuda.driver-version.full": "595.91.07"},
			want:   "595.91.07",
		},
		{
			name: "assembled from the parts",
			labels: map[string]string{
				"nvidia.com/cuda.driver-version.major":    "595",
				"nvidia.com/cuda.driver-version.minor":    "91",
				"nvidia.com/cuda.driver-version.revision": "07",
			},
			want: "595.91.07",
		},
		{
			name: "assembled without a revision",
			labels: map[string]string{
				"nvidia.com/cuda.driver-version.major": "595",
				"nvidia.com/cuda.driver-version.minor": "91",
			},
			want: "595.91",
		},
		{
			name:   "nothing published",
			labels: map[string]string{"nvidia.com/gpu.product": "NVIDIA-GeForce-RTX-4090"},
			want:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, driverVersionFromNode(tc.labels))
		})
	}
}

// TestAllocatedByNodeSumming covers the allocation count that used to be a
// hardcoded zero, which made a fleet with busy cards report every card idle.
func TestAllocatedByNodeSumming(t *testing.T) {
	pod := func(name, node string, phase corev1.PodPhase, gpus string) *corev1.Pod {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: corev1.PodSpec{
				NodeName: node,
				Containers: []corev1.Container{{
					Name: "c",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse(gpus)},
					},
				}},
			},
			Status: corev1.PodStatus{Phase: phase},
		}
		return p
	}
	client := fake.NewSimpleClientset(
		pod("running-1", "gpu-node-1", corev1.PodRunning, "1"),
		pod("running-2", "gpu-node-1", corev1.PodRunning, "1"),
		pod("done", "gpu-node-1", corev1.PodSucceeded, "1"),
		pod("elsewhere", "gpu-node-2", corev1.PodRunning, "2"),
		pod("unscheduled", "", corev1.PodPending, "1"),
	)
	collector := &InventoryCollector{clientset: client}

	got, err := collector.allocatedByNode(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 2, got["gpu-node-1"]["nvidia.com/gpu"],
		"finished pods release their devices; unscheduled pods reserve nothing")
	assert.EqualValues(t, 2, got["gpu-node-2"]["nvidia.com/gpu"])
	_, ok := got[""]
	assert.False(t, ok, "an unscheduled pod must not be attributed to a node")
}

// TestNodeToInventoryReportsRealAllocation proves the numbers the console
// shows: allocated from the pods, free as the remainder.
func TestNodeToInventoryReportsRealAllocation(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "sd4090",
			Labels: map[string]string{labelAcceleratorVendor: "nvidia", "nvidia.com/gpu.product": "NVIDIA-GeForce-RTX-4090"},
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Allocatable: corev1.ResourceList{
				"nvidia.com/gpu": resource.MustParse("2"),
			},
		},
	}

	inv := nodeToInventory(node, map[corev1.ResourceName]int64{"nvidia.com/gpu": 1})
	assert.EqualValues(t, 1, inv.GPUsAllocated)
	assert.EqualValues(t, 1, inv.GPUsFree)
	require.Len(t, inv.Resources, 1)
	assert.Equal(t, "NVIDIA-GeForce-RTX-4090", inv.Resources[0].CardType)
	assert.EqualValues(t, 1, inv.Resources[0].Allocated)
}

// TestAllocatedByNodeCountsARequestWithoutALimit covers the Kubernetes
// convention for extended resources: a limit implies a request.
func TestRequestedAcceleratorsReadsBothFields(t *testing.T) {
	onlyRequest := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("3")},
	}
	assert.EqualValues(t, 3, requestedAccelerators(onlyRequest)["nvidia.com/gpu"])

	onlyLimit := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("2")},
	}
	assert.EqualValues(t, 2, requestedAccelerators(onlyLimit)["nvidia.com/gpu"])

	cpuOnly := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{"cpu": resource.MustParse("2")},
	}
	assert.Empty(t, requestedAccelerators(cpuOnly), "CPU is not an accelerator")
}
