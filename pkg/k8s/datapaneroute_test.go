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

	"github.com/go-taas/go-taas/pkg/config"
)

func testDataPlaneConfig() config.RuntimeDataPlaneConfig {
	return config.RuntimeDataPlaneConfig{
		Enabled:            true,
		RouteNamespace:     "taas",
		ParentRefName:      "envoy-gateway",
		ParentRefNamespace: "yunzhi",
		ParentRefSection:   "https",
		BackendName:        "envoy-aibrix",
		BackendNamespace:   "yunzhi-system",
		BackendPort:        80,
	}
}

func newRouteFake(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			httpRouteGVR:      "HTTPRouteList",
			referenceGrantGVR: "ReferenceGrantList",
		}, objects...)
}

// TestDataPlaneRouterEnsureCreatesRouteAndGrant covers a fresh install:
// the route is created in its own namespace and the cross-namespace backend
// gets the ReferenceGrant it needs.
func TestDataPlaneRouterEnsureCreatesRouteAndGrant(t *testing.T) {
	dyn := newRouteFake()
	router := NewDataPlaneRouter(dyn)

	require.NoError(t, router.Ensure(context.Background(), "llm.example.com", testDataPlaneConfig()))

	route, err := dyn.Resource(httpRouteGVR).Namespace("taas").Get(context.Background(), DataPlaneRouteName, metav1.GetOptions{})
	require.NoError(t, err)
	hostnames, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
	assert.Equal(t, []string{"llm.example.com"}, hostnames)
	// The label is how the console finds the route again.
	assert.Equal(t, DataPlaneRouteLabelValue, route.GetLabels()[DataPlaneRouteLabel])

	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	require.Len(t, rules, 1)
	backends := rules[0].(map[string]any)["backendRefs"].([]any)
	assert.Equal(t, "envoy-aibrix", backends[0].(map[string]any)["name"])
	assert.Equal(t, int64(80), backends[0].(map[string]any)["port"])
	// Inference streams, so the request timeout must not be short.
	timeouts := rules[0].(map[string]any)["timeouts"].(map[string]any)
	assert.Equal(t, "600s", timeouts["request"])

	grant, err := dyn.Resource(referenceGrantGVR).Namespace("yunzhi-system").
		Get(context.Background(), "yunzhi-reserved-referencegrant-in-yunzhi-system", metav1.GetOptions{})
	require.NoError(t, err)
	from := grant.Object["spec"].(map[string]any)["from"].([]any)
	assert.Equal(t, "taas", from[0].(map[string]any)["namespace"])
}

// TestDataPlaneRouterEnsureFollowsTheHostname is the behaviour an operator
// expects: saving a new hostname replaces the old one on the same route.
func TestDataPlaneRouterEnsureFollowsTheHostname(t *testing.T) {
	dyn := newRouteFake()
	router := NewDataPlaneRouter(dyn)
	require.NoError(t, router.Ensure(context.Background(), "api.example.com", testDataPlaneConfig()))
	require.NoError(t, router.Ensure(context.Background(), "llm.example.com", testDataPlaneConfig()))

	hostnames, err := router.ServedHostnames(context.Background(), "taas")
	require.NoError(t, err)
	assert.Equal(t, []string{"llm.example.com"}, hostnames)

	// Only one route exists, so nothing keeps serving the old hostname.
	list, err := dyn.Resource(httpRouteGVR).Namespace("taas").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, list.Items, 1)
}

// TestDataPlaneRouterSkipsTheGrantInOneNamespace keeps the extra object out
// of a same-namespace install.
func TestDataPlaneRouterSkipsTheGrantInOneNamespace(t *testing.T) {
	dyn := newRouteFake()
	cfg := testDataPlaneConfig()
	cfg.BackendNamespace = cfg.RouteNamespace
	require.NoError(t, NewDataPlaneRouter(dyn).Ensure(context.Background(), "api.example.com", cfg))

	grants, err := dyn.Resource(referenceGrantGVR).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, grants.Items)
}

// TestBuildDataPlaneObjectsRejectsIncompleteConfig keeps a save from
// storing a hostname the platform cannot publish, and names the setting.
func TestBuildDataPlaneObjectsRejectsIncompleteConfig(t *testing.T) {
	cases := []struct {
		name     string
		hostname string
		mutate   func(*config.RuntimeDataPlaneConfig)
		want     string
	}{
		{"no hostname", "", func(*config.RuntimeDataPlaneConfig) {}, "needs a hostname"},
		{"no namespace", "a.example.com", func(c *config.RuntimeDataPlaneConfig) { c.RouteNamespace = "" }, "needs a namespace"},
		{"no parent gateway", "a.example.com", func(c *config.RuntimeDataPlaneConfig) { c.ParentRefName = "" }, "parent Gateway"},
		{"no backend", "a.example.com", func(c *config.RuntimeDataPlaneConfig) { c.BackendName = "" }, "inference gateway Service"},
		{"no port", "a.example.com", func(c *config.RuntimeDataPlaneConfig) { c.BackendPort = 0 }, "backend port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testDataPlaneConfig()
			tc.mutate(&cfg)
			_, _, err := buildDataPlaneObjects(tc.hostname, cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestServedHostnamesWithoutARoute reports nothing rather than failing, so
// the console can say "not configured" on a fresh install.
func TestServedHostnamesWithoutARoute(t *testing.T) {
	hostnames, err := NewDataPlaneRouter(newRouteFake()).ServedHostnames(context.Background(), "taas")
	require.NoError(t, err)
	assert.Empty(t, hostnames)

	// No client at all is also fine.
	hostnames, err = NewDataPlaneRouter(nil).ServedHostnames(context.Background(), "taas")
	require.NoError(t, err)
	assert.Empty(t, hostnames)
}

// TestDataPlaneRouterWithoutClientFails keeps the caller from believing it
// published something.
func TestDataPlaneRouterWithoutClientFails(t *testing.T) {
	err := NewDataPlaneRouter(nil).Ensure(context.Background(), "a.example.com", testDataPlaneConfig())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no dynamic client")
}
