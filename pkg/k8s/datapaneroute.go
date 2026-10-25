package k8s

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
)

// DataPlaneRouteName and DataPlaneRouteLabelValue mark the single
// HTTPRoute that publishes the inference gateway.
//
// One owner matters: while both Helm and the server wrote the hostname, a
// Helm upgrade and a console save conflicted on `spec.hostnames`, and which
// value survived depended on which writer ran last.
const (
	DataPlaneRouteName       = "taas-data-plane"
	DataPlaneRouteLabel      = "app.kubernetes.io/component"
	DataPlaneRouteLabelValue = "data-plane"
	// dataPlaneGrantSuffix names the ReferenceGrant that lets the route
	// reference the gateway Service across namespaces.
	dataPlaneGrantSuffix = "-reserved-referencegrant-in-"
)

// Gateway API resources.
var (
	httpRouteGVR      = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	referenceGrantGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1beta1", Resource: "referencegrants"}
)

// DataPlaneRouter owns the data-plane HTTPRoute. The chart does not render
// the route, so the server is its only writer.
type DataPlaneRouter struct {
	dyn dynamic.Interface
}

// NewDataPlaneRouter builds the router over the server's dynamic client.
func NewDataPlaneRouter(dyn dynamic.Interface) *DataPlaneRouter {
	return &DataPlaneRouter{dyn: dyn}
}

// Ensure makes the route serve exactly hostname, using cfg for the
// install-time parts (parent Gateway, backend Service).
func (r *DataPlaneRouter) Ensure(ctx context.Context, hostname string, cfg config.RuntimeDataPlaneConfig) error {
	if r == nil || r.dyn == nil {
		return fmt.Errorf("k8s: no dynamic client, cannot publish the data-plane route")
	}
	route, grant, err := buildDataPlaneObjects(hostname, cfg)
	if err != nil {
		return err
	}
	if grant != nil {
		if err := r.apply(ctx, referenceGrantGVR, grant); err != nil {
			return err
		}
	}
	return r.apply(ctx, httpRouteGVR, route)
}

// ServedHostnames reports the hostnames the route currently serves, so the
// console can tell an operator whether the saved value is in effect.
func (r *DataPlaneRouter) ServedHostnames(ctx context.Context, routeNamespace string) ([]string, error) {
	if r == nil || r.dyn == nil || routeNamespace == "" {
		return nil, nil
	}
	route, err := r.dyn.Resource(httpRouteGVR).Namespace(routeNamespace).
		Get(ctx, DataPlaneRouteName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	hostnames, _, err := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
	if err != nil {
		return nil, err
	}
	return hostnames, nil
}

// apply creates the object, or updates it in place keeping the resource
// version the API server handed out.
func (r *DataPlaneRouter) apply(ctx context.Context, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
	namespace, name := obj.GetNamespace(), obj.GetName()
	existing, err := r.dyn.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := r.dyn.Resource(gvr).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("k8s: create %s %s/%s: %w", gvr.Resource, namespace, name, err)
		}
		logger.S().Infow("k8s: data-plane object created", "kind", gvr.Resource, "namespace", namespace, "name", name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("k8s: read %s %s/%s: %w", gvr.Resource, namespace, name, err)
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	if _, err := r.dyn.Resource(gvr).Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("k8s: update %s %s/%s: %w", gvr.Resource, namespace, name, err)
	}
	logger.S().Infow("k8s: data-plane object updated", "kind", gvr.Resource, "namespace", namespace, "name", name)
	return nil
}

// buildDataPlaneObjects renders the route and, when the backend lives in
// another namespace, the ReferenceGrant it needs. A spec that would produce
// an unusable route fails here, naming the setting to fix, so a console save
// cannot store a hostname that answers 404.
func buildDataPlaneObjects(hostname string, cfg config.RuntimeDataPlaneConfig) (*unstructured.Unstructured, *unstructured.Unstructured, error) {
	routeNamespace := cfg.RouteNamespace
	switch {
	case hostname == "":
		return nil, nil, fmt.Errorf("k8s: data-plane route needs a hostname")
	case routeNamespace == "":
		return nil, nil, fmt.Errorf("k8s: data-plane route needs a namespace (runtime.dataPlane.routeNamespace)")
	case cfg.ParentRefName == "" || cfg.ParentRefNamespace == "":
		return nil, nil, fmt.Errorf("k8s: data-plane route needs the parent Gateway (runtime.dataPlane.parentRefName/parentRefNamespace)")
	case cfg.BackendName == "" || cfg.BackendNamespace == "":
		return nil, nil, fmt.Errorf("k8s: data-plane route needs the inference gateway Service (runtime.dataPlane.backendName/backendNamespace)")
	case cfg.BackendPort <= 0:
		return nil, nil, fmt.Errorf("k8s: data-plane route needs a backend port (runtime.dataPlane.backendPort)")
	}

	parent := map[string]any{
		"group":     "gateway.networking.k8s.io",
		"kind":      "Gateway",
		"name":      cfg.ParentRefName,
		"namespace": cfg.ParentRefNamespace,
	}
	if cfg.ParentRefSection != "" {
		parent["sectionName"] = cfg.ParentRefSection
	}
	route := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata": map[string]any{
			"name":      DataPlaneRouteName,
			"namespace": routeNamespace,
			"labels": map[string]any{
				DataPlaneRouteLabel:      DataPlaneRouteLabelValue,
				"app.kubernetes.io/name": "go-taas",
			},
		},
		"spec": map[string]any{
			"hostnames":  []any{hostname},
			"parentRefs": []any{parent},
			"rules": []any{map[string]any{
				"matches": []any{map[string]any{
					"path": map[string]any{"type": "PathPrefix", "value": "/"},
				}},
				"backendRefs": []any{map[string]any{
					"group":     "",
					"kind":      "Service",
					"name":      cfg.BackendName,
					"namespace": cfg.BackendNamespace,
					"port":      int64(cfg.BackendPort),
					"weight":    int64(1),
				}},
				// Inference answers stream; a short timeout would cut a long
				// generation off mid-stream.
				"timeouts": map[string]any{"request": "600s"},
			}},
		},
	}}
	if routeNamespace == cfg.BackendNamespace {
		return route, nil, nil
	}
	grant := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1beta1",
		"kind":       "ReferenceGrant",
		"metadata": map[string]any{
			"name":      cfg.ParentRefNamespace + dataPlaneGrantSuffix + cfg.BackendNamespace,
			"namespace": cfg.BackendNamespace,
			"labels":    map[string]any{"app.kubernetes.io/name": "go-taas"},
		},
		"spec": map[string]any{
			"from": []any{map[string]any{
				"group":     "gateway.networking.k8s.io",
				"kind":      "HTTPRoute",
				"namespace": routeNamespace,
			}},
			"to": []any{map[string]any{"group": "", "kind": "Service"}},
		},
	}}
	return route, grant, nil
}
