package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/go-taas/go-taas/pkg/logger"
)

// DataPlaneRouteLabel and DataPlaneRouteLabelValue select the HTTPRoute
// that publishes the inference gateway under the hostname tenants call.
// The chart labels the route it renders with them, so the console can
// update the hostname without knowing the Helm release name.
const (
	DataPlaneRouteLabel      = "app.kubernetes.io/component"
	DataPlaneRouteLabelValue = "data-plane"
)

// httpRouteGVR is the Gateway API HTTPRoute resource.
var httpRouteGVR = schema.GroupVersionResource{
	Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes",
}

// DataPlaneRouter keeps the data-plane HTTPRoute's hostname in step with
// the hostname an administrator saved in the console. The route itself —
// its parent Gateway, backend Service and timeouts — belongs to the chart,
// because those are install-time facts; only the hostname is a runtime
// setting.
type DataPlaneRouter struct {
	dyn       dynamic.Interface
	namespace string
}

// NewDataPlaneRouter builds the router over the client the server already
// has. namespace is where the data-plane route lives (the release
// namespace).
func NewDataPlaneRouter(dyn dynamic.Interface, namespace string) *DataPlaneRouter {
	return &DataPlaneRouter{dyn: dyn, namespace: namespace}
}

// SetHostname updates the data-plane route's hostname.
//
// An empty hostname is a no-op: clearing the setting must not silently take
// the data plane offline. A missing route is an error rather than a silent
// success, because saving a hostname that no route serves would advertise an
// endpoint that answers 404.
func (r *DataPlaneRouter) SetHostname(ctx context.Context, hostname string) error {
	if hostname == "" || r == nil || r.dyn == nil {
		return nil
	}
	routes, err := r.routes(ctx)
	if err != nil {
		return err
	}
	if len(routes.Items) == 0 {
		return fmt.Errorf("k8s: no data-plane route in namespace %s; enable dataPlane in the chart so the inference gateway is published", r.namespace)
	}

	body, err := json.Marshal(map[string]any{
		"spec": map[string]any{"hostnames": []string{hostname}},
	})
	if err != nil {
		return fmt.Errorf("k8s: encode data-plane route patch: %w", err)
	}
	for i := range routes.Items {
		route := &routes.Items[i]
		_, err := r.dyn.Resource(httpRouteGVR).Namespace(route.GetNamespace()).
			Patch(ctx, route.GetName(), types.MergePatchType, body, metav1.PatchOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("k8s: update data-plane route %s: %w", route.GetName(), err)
		}
		logger.S().Infow("k8s: data-plane route hostname updated",
			"route", route.GetName(), "namespace", route.GetNamespace(), "hostname", hostname)
	}
	return nil
}

// DataPlaneRouteHostnames reports the hostnames the data-plane route
// currently serves, so the console can tell an operator whether the saved
// setting is actually in effect.
func (r *DataPlaneRouter) DataPlaneRouteHostnames(ctx context.Context) ([]string, error) {
	if r == nil || r.dyn == nil {
		return nil, nil
	}
	routes, err := r.routes(ctx)
	if err != nil {
		return nil, err
	}
	hostnames := make([]string, 0, len(routes.Items))
	for i := range routes.Items {
		raw, found, err := unstructured.NestedStringSlice(routes.Items[i].Object, "spec", "hostnames")
		if err != nil || !found {
			continue
		}
		hostnames = append(hostnames, raw...)
	}
	return hostnames, nil
}

// routes lists the data-plane routes in the configured namespace.
func (r *DataPlaneRouter) routes(ctx context.Context) (*unstructured.UnstructuredList, error) {
	return r.dyn.Resource(httpRouteGVR).Namespace(r.namespace).
		List(ctx, metav1.ListOptions{LabelSelector: DataPlaneRouteLabel + "=" + DataPlaneRouteLabelValue})
}
