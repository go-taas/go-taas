package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// gatewayTestReconciler returns a reconciler whose dynamic client is a fake
// that records the writes the AIBrix routing path makes.
func gatewayTestReconciler(objects ...runtime.Object) (*k8sReconciler, *dynamicfake.FakeDynamicClient) {
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		httpRouteGVR:      "HTTPRouteList",
		referenceGrantGVR: "ReferenceGrantList",
	}, objects...)
	r := testReconciler()
	r.dynamic = dyn
	return r, dyn
}

func TestServedModelNameFallsBackToServiceName(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	assert.Equal(t, "demo", servedModelName(evt))

	evt.Model.Name = "qwen2.5-0.5b"
	assert.Equal(t, "qwen2.5-0.5b", servedModelName(evt))
}

func TestRoutingMetadataUsesAnnotationForUnlabellableNames(t *testing.T) {
	// A hub-style name contains '/', which a label value cannot hold, so it
	// is advertised through the annotation only.
	evt := testChangeEvent("upsert", "demo")
	evt.Model.Name = "MiniMax/MiniMax-M3"
	dep := testReconciler().buildDeployment(context.Background(), evt)

	_, labelled := dep.Spec.Template.Labels[modelNameLabel]
	assert.False(t, labelled)
	assert.Equal(t, "MiniMax/MiniMax-M3", dep.Spec.Template.Annotations[modelNameLabel])
	assert.Equal(t, "8000", dep.Spec.Template.Labels[modelPortLabel])
	require.Len(t, dep.Spec.Template.Spec.Containers, 1)
	assert.Contains(t, dep.Spec.Template.Spec.Containers[0].Command, "MiniMax/MiniMax-M3")
}

func TestModelRouteName(t *testing.T) {
	// DNS-safe names keep the historical form the gateway looks up.
	assert.Equal(t, "qwen2.5-0.5b-router", modelRouteName("qwen2.5-0.5b"))

	// A name that is not a DNS subdomain is normalised and disambiguated.
	got := modelRouteName("MiniMax/MiniMax-M3")
	assert.NotContains(t, got, "/")
	assert.Contains(t, got, "minimax")
	assert.Contains(t, got, "-router")
	assert.NotEqual(t, modelRouteName("MiniMax-MiniMax-M3"), got,
		"names differing only in punctuation must not collide")
}

func TestBuildModelHTTPRoute(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Model.Name = "qwen2.5-0.5b"
	route := buildModelHTTPRoute(evt, "aibrix-system", "aibrix-eg", "taas-infer")

	assert.Equal(t, "qwen2.5-0.5b-router", route.GetName())
	assert.Equal(t, "aibrix-system", route.GetNamespace())

	parentRefs, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	require.Len(t, parentRefs, 1)
	assert.Equal(t, "aibrix-eg", parentRefs[0].(map[string]any)["name"])

	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	require.Len(t, rules, 1)
	rule := rules[0].(map[string]any)

	backends := rule["backendRefs"].([]any)
	require.Len(t, backends, 1)
	backend := backends[0].(map[string]any)
	assert.Equal(t, "demo-svc", backend["name"])
	assert.Equal(t, "taas-infer", backend["namespace"])
	assert.Equal(t, int64(enginePort), backend["port"])

	matches := rule["matches"].([]any)
	require.NotEmpty(t, matches)
	for _, raw := range matches {
		headers := raw.(map[string]any)["headers"].([]any)
		require.Len(t, headers, 1)
		// The gateway resolves the model from this header before routing.
		assert.Equal(t, "qwen2.5-0.5b", headers[0].(map[string]any)["value"])
	}
}

func TestApplyModelRouteCreatesRouteAndGrant(t *testing.T) {
	r, dyn := gatewayTestReconciler()
	evt := testChangeEvent("upsert", "demo")
	evt.Model.Name = "qwen2.5-0.5b"

	require.NoError(t, r.applyModelRoute(context.Background(), evt))

	_, err := dyn.Resource(httpRouteGVR).Namespace("aibrix-system").
		Get(context.Background(), "qwen2.5-0.5b-router", metav1.GetOptions{})
	require.NoError(t, err)

	grant, err := dyn.Resource(referenceGrantGVR).Namespace("taas-infer").
		Get(context.Background(), referenceGrantName("aibrix-system", "taas-infer"), metav1.GetOptions{})
	require.NoError(t, err)
	spec, _, _ := unstructured.NestedMap(grant.Object, "spec")
	from := spec["from"].([]any)
	require.Len(t, from, 1)
	assert.Equal(t, "aibrix-system", from[0].(map[string]any)["namespace"])
}

func TestApplyModelRouteIsIdempotent(t *testing.T) {
	r, dyn := gatewayTestReconciler()
	evt := testChangeEvent("upsert", "demo")

	require.NoError(t, r.applyModelRoute(context.Background(), evt))
	require.NoError(t, r.applyModelRoute(context.Background(), evt))

	creates := 0
	updates := 0
	for _, action := range dyn.Actions() {
		switch action.GetVerb() {
		case "create":
			creates++
		case "update":
			updates++
		}
	}
	assert.Equal(t, 2, creates, "one route and one grant")
	assert.Equal(t, 2, updates, "the second apply updates in place")
}

func TestDeleteModelRoute(t *testing.T) {
	r, dyn := gatewayTestReconciler()
	evt := testChangeEvent("delete", "demo")
	evt.Model.Name = "qwen2.5-0.5b"

	require.NoError(t, r.applyModelRoute(context.Background(), evt))
	require.NoError(t, r.deleteModelRoute(context.Background(), evt))

	_, err := dyn.Resource(httpRouteGVR).Namespace("aibrix-system").
		Get(context.Background(), "qwen2.5-0.5b-router", metav1.GetOptions{})
	assert.Error(t, err)

	// The ReferenceGrant is namespace-scoped and shared, so it stays.
	_, err = dyn.Resource(referenceGrantGVR).Namespace("taas-infer").
		Get(context.Background(), referenceGrantName("aibrix-system", "taas-infer"), metav1.GetOptions{})
	assert.NoError(t, err)
}

func TestGatewayRouteTargetDefaults(t *testing.T) {
	r := &k8sReconciler{}
	namespace, gateway := r.gatewayRouteTarget()
	assert.Equal(t, defaultAIBrixNamespace, namespace)
	assert.Equal(t, defaultAIBrixGateway, gateway)

	r.SetGatewayRouting("custom-ns", "custom-gw")
	namespace, gateway = r.gatewayRouteTarget()
	assert.Equal(t, "custom-ns", namespace)
	assert.Equal(t, "custom-gw", gateway)
}

// TestDeleteModelRouteWithoutDynamicClientIsANoOp covers a controller
// running without a dynamic client: teardown must not fail.
func TestDeleteModelRouteWithoutDynamicClientIsANoOp(t *testing.T) {
	r := testReconciler()
	evt := testChangeEvent("delete", "demo")
	assert.NoError(t, r.deleteModelRoute(context.Background(), evt))
}

// TestApplyModelRouteWithoutDynamicClientFails keeps the failure visible so
// the caller can log it (the deploy path itself continues).
func TestApplyModelRouteWithoutDynamicClientFails(t *testing.T) {
	r := testReconciler()
	evt := testChangeEvent("upsert", "demo")
	assert.Error(t, r.applyModelRoute(context.Background(), evt))
}

var _ = k8stesting.Action(nil)
