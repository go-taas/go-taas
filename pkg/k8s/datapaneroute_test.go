package k8s

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
)

// testRoute builds an HTTPRoute shaped like the chart renders it.
func testRoute(name, namespace, hostname string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels":    map[string]any{DataPlaneRouteLabel: DataPlaneRouteLabelValue},
		},
		"spec": map[string]any{
			"hostnames": []any{hostname},
			"parentRefs": []any{map[string]any{
				"name": "gw", "namespace": "gw-ns", "sectionName": "https",
			}},
		},
	}}
}

func newRouteFake(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{httpRouteGVR: "HTTPRouteList"}, objects...)
}

// TestDataPlaneRouterSetsTheHostname covers the happy path: the saved
// hostname replaces the route's hostname and the backend is untouched.
func TestDataPlaneRouterSetsTheHostname(t *testing.T) {
	route := testRoute("taas-go-taas-data-plane", "taas", "api.example.com")
	dyn := newRouteFake(route)
	router := NewDataPlaneRouter(dyn, "taas")

	require.NoError(t, router.SetHostname(context.Background(), "llm.example.com"))

	got, err := dyn.Resource(httpRouteGVR).Namespace("taas").
		Get(context.Background(), "taas-go-taas-data-plane", metav1.GetOptions{})
	require.NoError(t, err)
	hostnames, _, err := unstructured.NestedStringSlice(got.Object, "spec", "hostnames")
	require.NoError(t, err)
	assert.Equal(t, []string{"llm.example.com"}, hostnames)

	// The rest of the route survives a hostname change.
	parents, _, _ := unstructured.NestedSlice(got.Object, "spec", "parentRefs")
	require.Len(t, parents, 1)
	assert.Equal(t, "gw", parents[0].(map[string]any)["name"])
}

// TestDataPlaneRouterEmptyHostnameIsANoOp keeps clearing the setting from
// taking the data plane offline.
func TestDataPlaneRouterEmptyHostnameIsANoOp(t *testing.T) {
	route := testRoute("r", "taas", "api.example.com")
	dyn := newRouteFake(route)
	router := NewDataPlaneRouter(dyn, "taas")

	require.NoError(t, router.SetHostname(context.Background(), ""))

	got, err := dyn.Resource(httpRouteGVR).Namespace("taas").Get(context.Background(), "r", metav1.GetOptions{})
	require.NoError(t, err)
	hostnames, _, _ := unstructured.NestedStringSlice(got.Object, "spec", "hostnames")
	assert.Equal(t, []string{"api.example.com"}, hostnames)
}

// TestDataPlaneRouterWithoutARouteFails keeps the console from saving a
// hostname nothing serves: the chart's route has to exist first.
func TestDataPlaneRouterWithoutARouteFails(t *testing.T) {
	router := NewDataPlaneRouter(newRouteFake(), "taas")
	err := router.SetHostname(context.Background(), "llm.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no data-plane route")
	assert.Contains(t, err.Error(), "dataPlane")
}

// TestDataPlaneRouterIgnoresOtherRoutes proves the label selector is what
// picks the route, not the namespace alone.
func TestDataPlaneRouterIgnoresOtherRoutes(t *testing.T) {
	console := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata": map[string]any{
			"name": "taas-go-taas-server", "namespace": "taas",
			"labels": map[string]any{"app.kubernetes.io/component": "server"},
		},
		"spec": map[string]any{"hostnames": []any{"taas.example.com"}},
	}}
	dyn := newRouteFake(console)
	router := NewDataPlaneRouter(dyn, "taas")

	require.Error(t, router.SetHostname(context.Background(), "llm.example.com"))

	got, _ := dyn.Resource(httpRouteGVR).Namespace("taas").Get(context.Background(), "taas-go-taas-server", metav1.GetOptions{})
	hostnames, _, _ := unstructured.NestedStringSlice(got.Object, "spec", "hostnames")
	assert.Equal(t, []string{"taas.example.com"}, hostnames, "the console route is never touched")
}

// TestDataPlaneRouterReportsServedHostnames feeds the console's
// "in effect / not in effect" indicator.
func TestDataPlaneRouterReportsServedHostnames(t *testing.T) {
	dyn := newRouteFake(testRoute("r", "taas", "api.example.com"))
	router := NewDataPlaneRouter(dyn, "taas")

	hostnames, err := router.DataPlaneRouteHostnames(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"api.example.com"}, hostnames)

	// Without a client (a unit test or an RBAC-less deployment) it reports
	// nothing rather than failing.
	empty, err := NewDataPlaneRouter(nil, "taas").DataPlaneRouteHostnames(context.Background())
	require.NoError(t, err)
	assert.Empty(t, empty)
}
