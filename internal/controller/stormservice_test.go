package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
)

// stormRolesOf reads the roles out of a built StormService.
func stormRolesOf(t *testing.T, storm *unstructured.Unstructured) []map[string]any {
	t.Helper()
	roles, found, err := unstructured.NestedSlice(storm.Object, "spec", "template", "spec", "roles")
	require.NoError(t, err)
	require.True(t, found, "a storm must carry roles")
	out := make([]map[string]any, 0, len(roles))
	for _, raw := range roles {
		role, ok := raw.(map[string]any)
		require.True(t, ok)
		out = append(out, role)
	}
	return out
}

// roleCommand reads the engine container command out of a role template.
func roleCommand(t *testing.T, role map[string]any) []string {
	t.Helper()
	spec := role["template"].(map[string]any)["spec"].(map[string]any)
	containers := spec["containers"].([]any)
	require.Len(t, containers, 1)
	command := containers[0].(map[string]any)["command"].([]any)
	out := make([]string, 0, len(command))
	for _, raw := range command {
		out = append(out, raw.(string))
	}
	return out
}

// roleGPURequest reads the engine container's accelerator request.
func roleGPURequest(t *testing.T, role map[string]any) string {
	t.Helper()
	spec := role["template"].(map[string]any)["spec"].(map[string]any)
	containers := spec["containers"].([]any)
	resources := containers[0].(map[string]any)["resources"].(map[string]any)
	limits := resources["limits"].(map[string]any)
	value, _ := limits["nvidia.com/gpu"].(string)
	return value
}

// roleLabelOf reads a role template label.
func roleLabelOf(t *testing.T, role map[string]any, key string) string {
	t.Helper()
	labels := role["template"].(map[string]any)["metadata"].(map[string]any)["labels"].(map[string]any)
	value, _ := labels[key].(string)
	return value
}

func TestBuildStormServiceSingleNode(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	storm, err := testReconciler().buildStormService(context.Background(), evt)
	require.NoError(t, err)

	assert.Equal(t, "StormService", storm.GetKind())
	assert.Equal(t, "orchestration.aibrix.ai/v1alpha1", storm.GetAPIVersion())
	assert.Equal(t, "demo", storm.GetName())
	assert.Equal(t, reconcileNamespace, storm.GetNamespace())

	replicas, _, _ := unstructured.NestedInt64(storm.Object, "spec", "replicas")
	assert.Equal(t, int64(2), replicas)

	// The selector is the shared resource label set: the Service, the
	// ModelClaim and the status follower all select on it.
	selector, _, _ := unstructured.NestedStringMap(storm.Object, "spec", "selector", "matchLabels")
	assert.Equal(t, "svc-1", selector[serviceLabelKey])

	roles := stormRolesOf(t, storm)
	require.Len(t, roles, 1, "a single-node storm has one role")
	assert.Equal(t, stormRoleWorker, roles[0]["name"])
	assert.Equal(t, int64(1), roles[0]["podGroupSize"])
	assert.Equal(t, int64(1), roles[0]["replicas"])
	assert.Equal(t, stormRoleWorker, roleLabelOf(t, roles[0], roleLabel))

	// The pod carries the routing metadata the gateway derives the model
	// route from, exactly like the historical Deployment pods did.
	assert.Equal(t, "demo", roleLabelOf(t, roles[0], modelNameLabel))
	command := roleCommand(t, roles[0])
	assert.Contains(t, command, "vllm.entrypoints.openai.api_server")
	assert.NotContains(t, command, "--tensor-parallel-size",
		"a single-node deploy passes no parallelism flag")
	assert.NotContains(t, command, "--kv-transfer-config")
}

func TestBuildStormServiceDistributed(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Topology = topologyDistributed
	evt.TensorParallelSize = 4
	evt.PipelineParallelSize = 2
	evt.Replicas = 3

	storm, err := testReconciler().buildStormService(context.Background(), evt)
	require.NoError(t, err)

	replicas, _, _ := unstructured.NestedInt64(storm.Object, "spec", "replicas")
	assert.Equal(t, int64(3), replicas, "storm replicas are replica groups")

	roles := stormRolesOf(t, storm)
	require.Len(t, roles, 1)
	assert.Equal(t, stormRoleWorker, roles[0]["name"])
	assert.Equal(t, int64(1), roles[0]["podGroupSize"],
		"parallelism shards the model inside one pod, so the group is one pod")
	assert.Equal(t, "8", roleGPURequest(t, roles[0]),
		"the pod asks for one device per shard, so vLLM's world size fits")

	command := roleCommand(t, roles[0])
	assert.Contains(t, command, "--tensor-parallel-size")
	assert.Contains(t, command, "4")
	assert.Contains(t, command, "--pipeline-parallel-size")
	assert.Contains(t, command, "2")
	// The worker role is not a PD role, so no KV transfer config.
	assert.NotContains(t, command, "--kv-transfer-config")
}

func TestBuildStormServicePD(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Topology = topologyPD
	evt.TensorParallelSize = 2
	evt.PipelineParallelSize = 1
	evt.PrefillReplicas = 2
	evt.DecodeReplicas = 3

	storm, err := testReconciler().buildStormService(context.Background(), evt)
	require.NoError(t, err)

	roles := stormRolesOf(t, storm)
	require.Len(t, roles, 2, "PD runs prefill and decode as separate roles")

	prefill, decode := roles[0], roles[1]
	assert.Equal(t, stormRolePrefill, prefill["name"])
	assert.Equal(t, int64(2), prefill["replicas"])
	assert.Equal(t, int64(1), prefill["podGroupSize"])
	assert.Equal(t, stormRolePrefill, roleLabelOf(t, prefill, roleLabel))
	assert.Equal(t, "2", roleGPURequest(t, prefill), "each prefill worker shards across both devices")

	assert.Equal(t, stormRoleDecode, decode["name"])
	assert.Equal(t, int64(3), decode["replicas"])
	assert.Equal(t, int64(1), decode["podGroupSize"])
	assert.Equal(t, stormRoleDecode, roleLabelOf(t, decode, roleLabel))
	assert.Equal(t, "2", roleGPURequest(t, decode))

	// vLLM needs the KV transfer role: prefill produces, decode consumes.
	prefillCmd := strings.Join(roleCommand(t, prefill), " ")
	assert.Contains(t, prefillCmd, "--kv-transfer-config")
	assert.Contains(t, prefillCmd, `kv_producer`)
	decodeCmd := strings.Join(roleCommand(t, decode), " ")
	assert.Contains(t, decodeCmd, `kv_consumer`)

	// Both roles select the same model route.
	assert.Equal(t, "demo", roleLabelOf(t, decode, modelNameLabel))
}

func TestBuildStormServicePDRespectsOperatorKVTransterConfig(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Topology = topologyPD
	evt.EngineArgs = map[string]string{
		"kv-transfer-config": `{"kv_connector":"NixlConnector"}`,
	}

	storm, err := testReconciler().buildStormService(context.Background(), evt)
	require.NoError(t, err)

	roles := stormRolesOf(t, storm)
	require.Len(t, roles, 2)
	for _, role := range roles {
		cmd := strings.Join(roleCommand(t, role), " ")
		assert.Contains(t, cmd, `{"kv_connector":"NixlConnector"}`,
			"an explicit engine argument wins over the platform default")
		assert.NotContains(t, cmd, pdKVConnector)
	}
}

func TestBuildStormServicePDSglangUsesDisaggregationMode(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Image.Engine = "sglang"
	evt.Topology = topologyPD

	storm, err := testReconciler().buildStormService(context.Background(), evt)
	require.NoError(t, err)

	roles := stormRolesOf(t, storm)
	require.Len(t, roles, 2)
	assert.Contains(t, strings.Join(roleCommand(t, roles[0]), " "), "--disaggregation-mode prefill")
	assert.Contains(t, strings.Join(roleCommand(t, roles[1]), " "), "--disaggregation-mode decode")
	assert.NotContains(t, strings.Join(roleCommand(t, roles[1]), " "), "--kv-transfer-config",
		"SGLang uses its own disaggregation flag")
}

func TestBuildStormServiceRolesShareSliceAnnotations(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Topology = topologyPD
	evt.PoolID = "pool-a"
	evt.SliceTemplateID = "small"
	evt.SliceMemoryMiB = 8192
	evt.SliceComputePct = 50

	r := testReconciler()
	r.SetVGPUConfig("hami.io/vgpu-policy", "best-effort", map[string]string{"pool": "a"})
	storm, err := r.buildStormService(context.Background(), evt)
	require.NoError(t, err)

	for _, role := range stormRolesOf(t, storm) {
		annotations := role["template"].(map[string]any)["metadata"].(map[string]any)["annotations"].(map[string]any)
		assert.Equal(t, "best-effort", annotations["hami.io/vgpu-policy"],
			"every role carries the HAMI placement policy")
	}
}

func TestApplyAndDeleteStormService(t *testing.T) {
	dyn := newFakeDynamic()
	ctx := context.Background()
	evt := testChangeEvent("upsert", "demo")

	storm, err := testReconciler().buildStormService(ctx, evt)
	require.NoError(t, err)
	require.NoError(t, applyStormService(ctx, dyn, storm))

	got, err := dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).Get(ctx, "demo", metav1.GetOptions{})
	require.NoError(t, err)
	replicas, _, _ := unstructured.NestedInt64(got.Object, "spec", "replicas")
	assert.Equal(t, int64(2), replicas)

	// Idempotent update: a new replica count lands on the existing object.
	evt.Replicas = 5
	storm2, err := testReconciler().buildStormService(ctx, evt)
	require.NoError(t, err)
	require.NoError(t, applyStormService(ctx, dyn, storm2))
	got, err = dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).Get(ctx, "demo", metav1.GetOptions{})
	require.NoError(t, err)
	replicas, _, _ = unstructured.NestedInt64(got.Object, "spec", "replicas")
	assert.Equal(t, int64(5), replicas)

	require.NoError(t, deleteStormService(ctx, dyn, reconcileNamespace, "demo"))
	_, err = dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).Get(ctx, "demo", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))

	// Deleting a missing object is a no-op (idempotent teardown).
	require.NoError(t, deleteStormService(ctx, dyn, reconcileNamespace, "demo"))
}

func TestPinStormServiceReplicas(t *testing.T) {
	dyn := newFakeDynamic()
	ctx := context.Background()
	r := testReconciler()
	r.dynamic = dyn
	evt := testChangeEvent("upsert", "demo")

	storm, err := r.buildStormService(ctx, evt)
	require.NoError(t, err)
	require.NoError(t, applyStormService(ctx, dyn, storm))

	evt.Replicas = 7
	require.NoError(t, r.pinStormServiceReplicas(ctx, evt))
	got, err := dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).Get(ctx, "demo", metav1.GetOptions{})
	require.NoError(t, err)
	replicas, _, _ := unstructured.NestedInt64(got.Object, "spec", "replicas")
	assert.Equal(t, int64(7), replicas)

	// Pinning a missing storm is a no-op, not an error: the caller may run
	// before the storm exists.
	require.NoError(t, r.pinStormServiceReplicas(ctx, testChangeEvent("upsert", "absent")))
}

func TestPodSpecToMapOmitsEmptyFields(t *testing.T) {
	spec := corev1.PodSpec{
		Containers:   []corev1.Container{{Name: "engine", Image: "img:1"}},
		NodeSelector: map[string]string{"go-taas.com/accelerator": "nvidia"},
	}
	out, err := podSpecToMap(spec)
	require.NoError(t, err)
	containers, ok := out["containers"].([]any)
	require.True(t, ok)
	assert.Len(t, containers, 1)
	assert.Equal(t, "engine", containers[0].(map[string]any)["name"])
	// Empty optional fields are omitted rather than sent as null: the CRD
	// defaults would otherwise be overwritten with nulls.
	_, hasSecurityContext := out["securityContext"]
	assert.False(t, hasSecurityContext)
	_, hasVolumes := out["volumes"]
	assert.False(t, hasVolumes)
}

func TestDeleteLegacyDeploymentRemovesTheOldObject(t *testing.T) {
	// A service that used to run as a Deployment must not keep serving next
	// to its storm: both match the Service selector.
	clientset := fake.NewSimpleClientset()
	r := &k8sReconciler{clientset: clientset, namespace: reconcileNamespace}
	require.NoError(t, r.createOrUpdateDeployment(context.Background(), r.buildDeployment(context.Background(), testChangeEvent("upsert", "demo"))))

	require.NoError(t, r.deleteLegacyDeployment(context.Background(), "demo"))
	_, err := clientset.AppsV1().Deployments(reconcileNamespace).Get(context.Background(), "demo", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))

	// No deployment: still a no-op.
	require.NoError(t, r.deleteLegacyDeployment(context.Background(), "demo"))
}

// TestAnyPodAnswersAcrossAPodGroup verifies readiness follows the first pod
// that actually serves: in a distributed or PD storm only one pod of the group
// exposes the API, so polling one fixed pod forever would report a healthy
// service as failed.
func TestAnyPodAnswersAcrossAPodGroup(t *testing.T) {
	ctx := context.Background()
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "a"}, Status: corev1.PodStatus{PodIP: "10.0.0.1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b"}, Status: corev1.PodStatus{PodIP: "10.0.0.2"}},
	}
	r := &k8sReconciler{namespace: reconcileNamespace}
	r.engineProber = func(_ context.Context, host string, _ int) bool { return host == "10.0.0.2" }
	assert.True(t, r.anyPodAnswers(ctx, pods, 8000), "the answering pod makes the service ready")

	// A terminating pod or one without an address is skipped.
	r.engineProber = func(context.Context, string, int) bool { return true }
	now := metav1.Now()
	assert.False(t, r.anyPodAnswers(ctx, []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "term", DeletionTimestamp: &now}, Status: corev1.PodStatus{PodIP: "10.0.0.3"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "no-ip"}},
	}, 8000))
}

// TestTerminalPodFailureInFailsTheGroup verifies one pod that can never start
// fails the whole deploy: a pod group cannot serve without all of its shards.
func TestTerminalPodFailureInFailsTheGroup(t *testing.T) {
	now := metav1.Now()
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "ok"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		{ObjectMeta: metav1.ObjectMeta{Name: "terminating", DeletionTimestamp: &now}, Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
			}},
		}},
	}
	assert.Empty(t, terminalPodFailureIn(pods), "a terminating pod is not the new spec's problem")

	pods[1] = corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "broken"}, Status: corev1.PodStatus{
		ContainerStatuses: []corev1.ContainerStatus{{
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
		}},
	}}
	assert.Contains(t, terminalPodFailureIn(pods), "ImagePullBackOff")
}

// TestParallelReplicaGetsSharedMemory verifies a tensor/pipeline-parallel
// replica mounts a memory-backed /dev/shm: the engine's parallel workers
// exchange tensors through it, and the Kubernetes default (64 MiB) makes the
// engine die with "Insufficient space in /dev/shm".
func TestParallelReplicaGetsSharedMemory(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Topology = topologyDistributed
	evt.TensorParallelSize = 2

	spec, _ := testReconciler().buildPodTemplate(context.Background(), evt, "")
	var shm *corev1.Volume
	for i := range spec.Volumes {
		if spec.Volumes[i].Name == "shm" {
			shm = &spec.Volumes[i]
		}
	}
	require.NotNil(t, shm, "a parallel replica needs a shared-memory volume")
	require.NotNil(t, shm.EmptyDir)
	assert.Equal(t, corev1.StorageMediumMemory, shm.EmptyDir.Medium)
	mounted := false
	for _, mount := range spec.Containers[0].VolumeMounts {
		if mount.MountPath == "/dev/shm" {
			mounted = true
		}
	}
	assert.True(t, mounted, "/dev/shm is backed by the shared-memory volume")

	// A single-device replica keeps the default shm.
	single, _ := testReconciler().buildPodTemplate(context.Background(), testChangeEvent("upsert", "demo"), "")
	for _, volume := range single.Volumes {
		assert.NotEqual(t, "shm", volume.Name)
	}
}
