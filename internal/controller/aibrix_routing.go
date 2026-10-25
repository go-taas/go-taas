package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

// AIBrix serving-gateway routing metadata. The gateway discovers which
// model a workload serves from the workload's own metadata rather than
// from a control-plane API: the per-model HTTPRoute is derived from a
// Deployment's labels/annotations, and the request path picks a backend
// from the pods carrying the same metadata. A workload without it is
// unreachable — the gateway answers "model <name> does not exist" even
// though the engine is running.
const (
	// modelNameLabel carries the served model name: the value a client
	// sends as the `model` field. Read from a label first and from an
	// annotation when the name cannot be a label value (hub ids such as
	// "MiniMax/MiniMax-M3" contain '/').
	modelNameLabel = "model.aibrix.ai/name"
	// modelPortLabel carries the engine's serving port.
	modelPortLabel = "model.aibrix.ai/port"
	// modelServiceAnno carries the name of the Service backing the
	// workload. The gateway derives the HTTPRoute backend from it, and
	// our Service name ("<service>-svc") is never equal to the model name.
	modelServiceAnno = "model.aibrix.ai/service-name"
	// enginePort is the port every supported engine serves on.
	enginePort = 8000
)

// servedModelName returns the name a client addresses a service's model
// by: the catalog model name, or the service name when the event carries
// none (older publishers do not set it).
func servedModelName(evt changeEvent) string {
	if name := strings.TrimSpace(evt.Model.Name); name != "" {
		return name
	}
	return evt.Name
}

// routingLabels returns the AIBrix labels for a served model name and the
// backing service. The name is only labelled when it is a valid label
// value; the annotation carries it in every case.
func routingLabels(modelName string) map[string]string {
	labels := map[string]string{modelPortLabel: strconv.Itoa(enginePort)}
	if len(validation.IsValidLabelValue(modelName)) == 0 {
		labels[modelNameLabel] = modelName
	}
	return labels
}

// routingAnnotations returns the AIBrix annotations for a served model
// name and the backing service.
func routingAnnotations(modelName, service string) map[string]string {
	return map[string]string{
		modelNameLabel:   modelName,
		modelPortLabel:   strconv.Itoa(enginePort),
		modelServiceAnno: service,
	}
}

// mergeLabels returns a new map holding base overlaid with extra, so the
// shared resource labels stay intact and no caller-visible map is mutated.
func mergeLabels(base, extra map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	return merged
}

// AIBrix model HTTPRoute GVRs. The serving gateway resolves a request's
// model to an HTTPRoute named <model>-router in the gateway namespace; a
// missing route is a 503 for every request to that model.
var (
	httpRouteGVR      = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	referenceGrantGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1beta1", Resource: "referencegrants"}
)

// Defaults for the AIBrix install: the control-plane namespace and the
// Gateway object every model route attaches to.
const (
	defaultAIBrixNamespace = "aibrix-system"
	defaultAIBrixGateway   = "aibrix-eg"
	// modelRequestTimeoutSeconds bounds a single request through the
	// gateway; AIBrix uses the same 600s default.
	modelRequestTimeoutSeconds = 600
)

// modelRoutePaths is the request path set a model route matches. It
// mirrors the paths the AIBrix router itself publishes, so a route we
// create serves the same endpoints a route AIBrix creates would.
var modelRoutePaths = []string{
	"/v1/completions",
	"/v1/chat/completions",
	"/v1/responses",
	"/v1/messages",
	"/v1/embeddings",
	"/v1/rerank",
	"/v1/classify",
	"/classify",
	"/v1/decisions",
	"/v1/systemone",
	"/v1/score",
	"/generate",
	"/generatevideo",
	"/v1/images/generations",
	"/v1/images/edits",
	"/v1/video",
	"/v1/videos",
	"/v1/audio/transcriptions",
	"/v1/audio/translations",
	"/v1/audio/speech",
	"/v1/audio/voices",
	"/tokenize",
	"/pooling",
}

// modelRouteName returns the HTTPRoute name the gateway looks up for a
// served model. It matches the AIBrix naming rule so a cluster that also
// runs the AIBrix model router converges on one object instead of two.
func modelRouteName(modelName string) string {
	candidate := modelName + "-router"
	if len(validation.IsDNS1123Subdomain(candidate)) == 0 {
		return candidate
	}
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(modelName) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "model"
	}
	digest := sha256.Sum256([]byte(modelName))
	hash := hex.EncodeToString(digest[:])[:12]
	if maxBase := 253 - len("-router") - 1 - len(hash); len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
	}
	return base + "-" + hash + "-router"
}

// referenceGrantName is the ReferenceGrant that lets the gateway
// namespace's HTTPRoute reference a Service in the workload namespace.
func referenceGrantName(gatewayNamespace, workloadNamespace string) string {
	return gatewayNamespace + "-reserved-referencegrant-in-" + workloadNamespace
}

// buildModelHTTPRoute composes the route that binds one served model to
// its backing Service. The gateway namespace holds the route; the
// backend lives in the workload namespace, which is why the caller also
// applies a ReferenceGrant.
func buildModelHTTPRoute(evt changeEvent, gatewayNamespace, gatewayName, workloadNamespace string) *unstructured.Unstructured {
	modelName := servedModelName(evt)
	matches := make([]any, 0, len(modelRoutePaths))
	for _, path := range modelRoutePaths {
		matches = append(matches, map[string]any{
			"path": map[string]any{"type": "PathPrefix", "value": path},
			"headers": []any{map[string]any{
				"type":  "Exact",
				"name":  "model",
				"value": modelName,
			}},
		})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata": map[string]any{
			"name":      modelRouteName(modelName),
			"namespace": gatewayNamespace,
			"labels": map[string]any{
				mcLabelManagedBy: managedByValue,
				mcLabelServiceID: evt.ServiceID,
			},
		},
		"spec": map[string]any{
			"parentRefs": []any{map[string]any{
				"name":      gatewayName,
				"namespace": gatewayNamespace,
			}},
			"rules": []any{map[string]any{
				"matches": matches,
				"backendRefs": []any{map[string]any{
					"kind":      "Service",
					"name":      serviceName(evt.Name),
					"namespace": workloadNamespace,
					"port":      int64(enginePort),
					"weight":    int64(1),
				}},
				"timeouts": map[string]any{
					"request": strconv.Itoa(modelRequestTimeoutSeconds) + "s",
				},
			}},
		},
	}}
}

// buildModelReferenceGrant composes the ReferenceGrant that permits the
// gateway namespace's HTTPRoutes to use Services in the workload
// namespace.
func buildModelReferenceGrant(gatewayNamespace, workloadNamespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1beta1",
		"kind":       "ReferenceGrant",
		"metadata": map[string]any{
			"name":      referenceGrantName(gatewayNamespace, workloadNamespace),
			"namespace": workloadNamespace,
		},
		"spec": map[string]any{
			"from": []any{map[string]any{
				"group":     "gateway.networking.k8s.io",
				"kind":      "HTTPRoute",
				"namespace": gatewayNamespace,
			}},
			"to": []any{map[string]any{
				"group": "",
				"kind":  "Service",
			}},
		},
	}}
}

// applyUnstructuredObject creates the object, or updates it in place while
// keeping the resource version the API server handed out.
func applyUnstructuredObject(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
	if dyn == nil {
		return fmt.Errorf("controller: dynamic client unavailable")
	}
	namespace, name := obj.GetNamespace(), obj.GetName()
	existing, err := dyn.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = dyn.Resource(gvr).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	_, err = dyn.Resource(gvr).Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{})
	return err
}

// deleteUnstructuredObject deletes an object by name, ignoring not-found.
func deleteUnstructuredObject(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) error {
	if dyn == nil {
		return nil
	}
	err := dyn.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// applyModelRoute writes the model's HTTPRoute and the ReferenceGrant that
// lets the gateway namespace reference the Service. Both are needed before
// the gateway can serve the model.
func (r *k8sReconciler) applyModelRoute(ctx context.Context, evt changeEvent) error {
	gatewayNamespace, gatewayName := r.gatewayRouteTarget()
	if err := applyUnstructuredObject(ctx, r.dynamic, referenceGrantGVR,
		buildModelReferenceGrant(gatewayNamespace, r.namespace)); err != nil {
		return err
	}
	return applyUnstructuredObject(ctx, r.dynamic, httpRouteGVR,
		buildModelHTTPRoute(evt, gatewayNamespace, gatewayName, r.namespace))
}

// deleteModelRoute removes the model's HTTPRoute. The ReferenceGrant stays:
// it is namespace-scoped and shared by every model route of the namespace.
func (r *k8sReconciler) deleteModelRoute(ctx context.Context, evt changeEvent) error {
	gatewayNamespace, _ := r.gatewayRouteTarget()
	return deleteUnstructuredObject(ctx, r.dynamic, httpRouteGVR,
		gatewayNamespace, modelRouteName(servedModelName(evt)))
}
