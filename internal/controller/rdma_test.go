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

// seedDeviceClass writes the DeviceClass the chart would create, so the
// controller's existence check passes.
func seedDeviceClass(t *testing.T, reconciler Reconciler, name string) {
	t.Helper()
	dyn := reconciler.(*k8sReconciler).dynamic
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "resource.k8s.io/v1",
		"kind":       "DeviceClass",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"selectors": []any{map[string]any{"cel": map[string]any{
				"expression": `device.driver == "dra.net" && device.attributes["dra.net"].rdma == true`,
			}}},
		},
	}}
	_, err := dyn.Resource(deviceClassGVR).Create(context.Background(), obj, metav1.CreateOptions{})
	require.NoError(t, err)
}

// TestBuildRdmaClaimTemplate verifies the template asks for the requested
// number of devices from the requested class, and that the labels DRA copies
// into generated claims are present.
func TestBuildRdmaClaimTemplate(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Rdma = &rdmaRequest{Enabled: true, Devices: 4, DeviceClass: "taas-rdma"}

	tpl := buildRdmaClaimTemplate(evt, reconcileNamespace, "taas-rdma")
	assert.Equal(t, "ResourceClaimTemplate", tpl.GetKind())
	assert.Equal(t, "resource.k8s.io/v1", tpl.GetAPIVersion())
	assert.Equal(t, "demo-rdma", tpl.GetName())
	assert.Equal(t, reconcileNamespace, tpl.GetNamespace())
	assert.Equal(t, "svc-1", tpl.GetLabels()[labelServiceID])

	requests, found, err := unstructured.NestedSlice(tpl.Object, "spec", "spec", "devices", "requests")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, requests, 1)
	request := requests[0].(map[string]any)
	assert.Equal(t, rdmaClaimName, request["name"])
	exactly := request["exactly"].(map[string]any)
	assert.Equal(t, "taas-rdma", exactly["deviceClassName"])
	assert.Equal(t, "ExactCount", exactly["allocationMode"])
	assert.Equal(t, int64(4), exactly["count"])

	// Generated claims inherit spec.metadata labels, which is how the teardown
	// sweep finds a claim whose pod was force-deleted.
	labels, found, err := unstructured.NestedStringMap(tpl.Object, "spec", "metadata", "labels")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "svc-1", labels[labelServiceID])
}

// TestBuildRdmaClaimTemplateDefaultsToOneDevice verifies an enabled request
// without an explicit count still asks for one device.
func TestBuildRdmaClaimTemplateDefaultsToOneDevice(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Rdma = &rdmaRequest{Enabled: true}

	tpl := buildRdmaClaimTemplate(evt, reconcileNamespace, "taas-rdma")
	count, _, err := unstructured.NestedInt64(tpl.Object, "spec", "spec", "devices", "requests")
	assert.Error(t, err, "requests is a list, not an int")
	assert.EqualValues(t, 0, count)
	requests, _, _ := unstructured.NestedSlice(tpl.Object, "spec", "spec", "devices", "requests")
	exactly := requests[0].(map[string]any)["exactly"].(map[string]any)
	assert.Equal(t, int64(1), exactly["count"])
}

// TestPodSpecRequestsRdmaClaim verifies the pod both references the claim
// template and lets the container use the claim, for every topology.
func TestPodSpecRequestsRdmaClaim(t *testing.T) {
	for _, topology := range []string{"", topologyDistributed, topologyPD} {
		name := topology
		if name == "" {
			name = "single"
		}
		t.Run(name, func(t *testing.T) {
			evt := testChangeEvent("upsert", "demo")
			evt.Topology = topology
			evt.TensorParallelSize = 2
			evt.PrefillReplicas = 1
			evt.DecodeReplicas = 1
			evt.Rdma = &rdmaRequest{Enabled: true, Devices: 2, DeviceClass: "taas-rdma"}

			storm, err := testReconciler().buildStormService(context.Background(), evt)
			require.NoError(t, err)
			for _, role := range stormRolesOf(t, storm) {
				spec := role["template"].(map[string]any)["spec"].(map[string]any)
				claims, found, err := unstructured.NestedSlice(spec, "resourceClaims")
				require.NoError(t, err)
				require.True(t, found, "the pod must reference the claim template")
				require.Len(t, claims, 1)
				claim := claims[0].(map[string]any)
				assert.Equal(t, rdmaClaimName, claim["name"])
				assert.Equal(t, "demo-rdma", claim["resourceClaimTemplateName"])

				containers := spec["containers"].([]any)
				resources := containers[0].(map[string]any)["resources"].(map[string]any)
				containerClaims, found, err := unstructured.NestedSlice(resources, "claims")
				require.NoError(t, err)
				require.True(t, found, "the container must name the claim")
				require.Len(t, containerClaims, 1)
				assert.Equal(t, rdmaClaimName, containerClaims[0].(map[string]any)["name"])
			}
		})
	}
}

// TestPodSpecWithoutRdmaHasNoClaim verifies a plain service is unchanged.
func TestPodSpecWithoutRdmaHasNoClaim(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	storm, err := testReconciler().buildStormService(context.Background(), evt)
	require.NoError(t, err)
	for _, role := range stormRolesOf(t, storm) {
		spec := role["template"].(map[string]any)["spec"].(map[string]any)
		_, found, _ := unstructured.NestedSlice(spec, "resourceClaims")
		assert.False(t, found, "no RDMA request must leave the pod spec untouched")
	}
}

// TestApplyUpsertWritesRdmaClaimTemplate verifies the whole reconcile path
// creates the claim template before the workload.
func TestApplyUpsertWritesRdmaClaimTemplate(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)
	seedDeviceClass(t, reconciler, defaultRdmaDeviceClass)
	ctx := context.Background()

	evt := testChangeEvent("upsert", "demo")
	evt.Rdma = &rdmaRequest{Enabled: true, Devices: 2}
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(evt)))

	tpl, err := dyn.Resource(resourceClaimTemplateGVR).Namespace(reconcileNamespace).
		Get(ctx, "demo-rdma", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "svc-1", tpl.GetLabels()[labelServiceID])
	requests, _, _ := unstructured.NestedSlice(tpl.Object, "spec", "spec", "devices", "requests")
	exactly := requests[0].(map[string]any)["exactly"].(map[string]any)
	assert.Equal(t, defaultRdmaDeviceClass, exactly["deviceClassName"],
		"a service that names no class gets the platform default")
	assert.Equal(t, int64(2), exactly["count"])

	// The storm references the template.
	storm, err := dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).Get(ctx, "demo", metav1.GetOptions{})
	require.NoError(t, err)
	roles := stormRolesOf(t, storm)
	spec := roles[0]["template"].(map[string]any)["spec"].(map[string]any)
	claims, _, _ := unstructured.NestedSlice(spec, "resourceClaims")
	require.Len(t, claims, 1)
	assert.Equal(t, "demo-rdma", claims[0].(map[string]any)["resourceClaimTemplateName"])
}

// TestApplyUpsertFailsOnMissingDeviceClass verifies a typo in the class name
// fails the deploy with an actionable message instead of leaving the pods
// unschedulable.
func TestApplyUpsertFailsOnMissingDeviceClass(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)
	ctx := context.Background()

	evt := testChangeEvent("upsert", "demo")
	evt.Rdma = &rdmaRequest{Enabled: true, Devices: 1, DeviceClass: "no-such-class"}
	err := reconciler.ApplyInferServiceChange(ctx, changeMessage(evt))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-such-class")

	_, err = dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).Get(ctx, "demo", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "nothing is created when the class is missing")
}

// TestApplyUpsertRemovesClaimWhenRdmaDisabled verifies toggling RDMA off
// deletes the template instead of leaving an unused device request behind.
func TestApplyUpsertRemovesClaimWhenRdmaDisabled(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)
	seedDeviceClass(t, reconciler, defaultRdmaDeviceClass)
	ctx := context.Background()

	evt := testChangeEvent("upsert", "demo")
	evt.Rdma = &rdmaRequest{Enabled: true, Devices: 1}
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(evt)))
	_, err := dyn.Resource(resourceClaimTemplateGVR).Namespace(reconcileNamespace).
		Get(ctx, "demo-rdma", metav1.GetOptions{})
	require.NoError(t, err)

	disabled := testChangeEvent("upsert", "demo")
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(disabled)))
	_, err = dyn.Resource(resourceClaimTemplateGVR).Namespace(reconcileNamespace).
		Get(ctx, "demo-rdma", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "the unused template is removed")
}

// TestTeardownRemovesRdmaClaimTemplate verifies stop/delete removes the
// service's claim template.
func TestTeardownRemovesRdmaClaimTemplate(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)
	seedDeviceClass(t, reconciler, defaultRdmaDeviceClass)
	ctx := context.Background()

	evt := testChangeEvent("upsert", "demo")
	evt.Rdma = &rdmaRequest{Enabled: true, Devices: 1}
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(evt)))
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(testChangeEvent("delete", "demo"))))

	_, err := dyn.Resource(resourceClaimTemplateGVR).Namespace(reconcileNamespace).
		Get(ctx, "demo-rdma", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "the claim template must not outlive the service")
}

// TestRdmaClaimTemplateNotStolenOnNameReuse verifies a late teardown for an old
// service does not delete the replacement's claim template.
func TestRdmaClaimTemplateNotStolenOnNameReuse(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)
	ctx := context.Background()

	replacement := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "resource.k8s.io/v1",
		"kind":       "ResourceClaimTemplate",
		"metadata": map[string]any{
			"name":      "demo-rdma",
			"namespace": reconcileNamespace,
			"labels":    map[string]any{mcLabelManagedBy: managedByValue, labelServiceID: "svc-new"},
		},
	}}
	_, err := dyn.Resource(resourceClaimTemplateGVR).Namespace(reconcileNamespace).
		Create(ctx, replacement, metav1.CreateOptions{})
	require.NoError(t, err)

	// A late delete event for the previous owner (svc-1).
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(testChangeEvent("delete", "demo"))))

	_, err = dyn.Resource(resourceClaimTemplateGVR).Namespace(reconcileNamespace).
		Get(ctx, "demo-rdma", metav1.GetOptions{})
	assert.NoError(t, err, "the new owner's claim template must survive")
}

// TestRdmaDisabledHelper verifies the "no devices" cases.
func TestRdmaDisabledHelper(t *testing.T) {
	assert.True(t, rdmaDisabled(testChangeEvent("upsert", "demo")))
	assert.True(t, rdmaDisabled(changeEvent{Rdma: &rdmaRequest{Enabled: false, Devices: 4}}))
	assert.True(t, rdmaDisabled(changeEvent{Rdma: &rdmaRequest{Enabled: true, Devices: 0}}))
	assert.False(t, rdmaDisabled(changeEvent{Rdma: &rdmaRequest{Enabled: true, Devices: 1}}))
}

// TestRdmaDeviceClassResolution verifies the configured default is used and a
// service's explicit class wins.
func TestRdmaDeviceClassResolution(t *testing.T) {
	r := &k8sReconciler{}
	assert.Equal(t, defaultRdmaDeviceClass, r.rdmaDeviceClass(""))
	r.SetRDMAConfig("custom-class")
	assert.Equal(t, "custom-class", r.rdmaDeviceClass(""))
	assert.Equal(t, "explicit", r.rdmaDeviceClass("explicit"))
}

// TestRdmaClaimsUseCoreV1Types documents that the pod spec is built from the
// typed API: the claim reference survives the JSON round trip the storm
// builder performs.
func TestRdmaClaimsUseCoreV1Types(t *testing.T) {
	spec := corev1.PodSpec{
		ResourceClaims: []corev1.PodResourceClaim{{
			Name:                      rdmaClaimName,
			ResourceClaimTemplateName: ptr("demo-rdma"),
		}},
	}
	asMap, err := podSpecToMap(spec)
	require.NoError(t, err)
	assert.EqualValues(t, "demo-rdma", asMap["resourceClaims"].([]any)[0].(map[string]any)["resourceClaimTemplateName"])
}

// TestLastLogLine verifies the hint keeps the last real line, trims it and
// caps its size.
func TestLastLogLine(t *testing.T) {
	assert.Equal(t, "last engine line", lastLogLine("one\n\n  last engine line  \n"))
	assert.Equal(t, "", lastLogLine("\n  \n"))

	long := strings.Repeat("x", engineLogHintMaxLen+50)
	got := lastLogLine(long)
	assert.Len(t, got, engineLogHintMaxLen+len("…"))
	assert.True(t, strings.HasSuffix(got, "…"))
}

// TestEngineTimeoutReasonIncludesLogHint verifies a Running-but-silent pod
// reports the engine's own last log line instead of only "pod phase Running".
func TestEngineTimeoutReasonIncludesLogHint(t *testing.T) {
	r := &k8sReconciler{
		clientset: fake.NewSimpleClientset(&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "demo-0",
				Namespace: reconcileNamespace,
				Labels:    map[string]string{"app": "demo", labelServiceID: "svc-1"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}),
		namespace: reconcileNamespace,
		logTail: func(context.Context, string) (string, error) {
			return "NCCL WARN Bootstrap : no socket interface found\n", nil
		},
	}
	err := r.engineTimeoutError(context.Background(), testChangeEvent("upsert", "demo"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not answer its health endpoint")
	assert.Contains(t, err.Error(), "NCCL WARN")
	assert.NotContains(t, err.Error(), "pod phase")
}

// TestEngineTimeoutReasonKeepsBlockingReason verifies a real scheduling
// blocker is still reported as-is, without a log hint that would hide it.
func TestEngineTimeoutReasonKeepsBlockingReason(t *testing.T) {
	r := &k8sReconciler{
		clientset: fake.NewSimpleClientset(&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "demo-0",
				Namespace: reconcileNamespace,
				Labels:    map[string]string{"app": "demo", labelServiceID: "svc-1"},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				Conditions: []corev1.PodCondition{{
					Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
					Message: "0/1 nodes are available: 1 Insufficient nvidia.com/gpu.",
				}},
			},
		}),
		namespace: reconcileNamespace,
		logTail:   func(context.Context, string) (string, error) { return "ignored", nil },
	}
	err := r.engineTimeoutError(context.Background(), testChangeEvent("upsert", "demo"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Insufficient nvidia.com/gpu")
	assert.NotContains(t, err.Error(), "last engine log")
}
