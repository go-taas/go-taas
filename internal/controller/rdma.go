// RDMA devices for inference pods (Kubernetes DRA).
//
// A serving pod talks to its peers over RDMA when a model is sharded across
// machines or split into prefill and decode roles: NCCL moves activations and
// KV cache between them, and over TCP that link becomes the bottleneck. The
// device is allocated through Dynamic Resource Allocation rather than an
// extended resource, because a node's RDMA devices carry attributes (link
// state, PCI address, NUMA node, card model) an operator needs to select on,
// and because a claim gives a pod devices without exposing every device on the
// node.
//
// Ownership:
//
//   - the DeviceClass is cluster infrastructure: the helm chart creates it,
//     because which devices a fleet offers is a property of the cluster;
//   - the ResourceClaimTemplate is workload output: the controller writes one
//     per service so the device count is a service property, and deletes it
//     with the service. Every pod of the service is created from that
//     template, so each pod receives its own allocation instead of sharing
//     one.

package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/go-taas/go-taas/pkg/logger"
)

// DRA resources. resource.k8s.io/v1 is the GA API (Kubernetes 1.34+), which
// every supported cluster runs; the driver is whatever the fleet installed
// (dra.net here, the NVIDIA DRA network driver).
var (
	deviceClassGVR           = schema.GroupVersionResource{Group: "resource.k8s.io", Version: "v1", Resource: "deviceclasses"}
	resourceClaimTemplateGVR = schema.GroupVersionResource{Group: "resource.k8s.io", Version: "v1", Resource: "resourceclaimtemplates"}
	resourceClaimGVR         = schema.GroupVersionResource{Group: "resource.k8s.io", Version: "v1", Resource: "resourceclaims"}
)

const (
	// rdmaClaimName is the claim's name inside the pod spec: the container
	// references devices by this name, and the template request uses the
	// same name so the two never drift.
	rdmaClaimName = "rdma"
	// defaultRdmaDeviceClass is the DeviceClass the chart creates and the
	// controller requests when a service names none.
	defaultRdmaDeviceClass = "taas-rdma"
	// rdmaClaimTemplateSuffix names a service's claim template. The service
	// name is platform-unique, so the template name is too.
	rdmaClaimTemplateSuffix = "-rdma"
)

// rdmaClaimTemplateName is the ResourceClaimTemplate a service's pods are
// created from.
func rdmaClaimTemplateName(evt changeEvent) string { return evt.Name + rdmaClaimTemplateSuffix }

// rdmaDeviceClass resolves the DeviceClass a request asks for, falling back to
// the platform default.
func (r *k8sReconciler) rdmaDeviceClass(requested string) string {
	if requested != "" {
		return requested
	}
	if r.rdmaClass != "" {
		return r.rdmaClass
	}
	return defaultRdmaDeviceClass
}

// buildRdmaClaimTemplate composes the ResourceClaimTemplate for a service: one
// device request per pod, for the requested number of devices.
//
// The labels are also written to spec.metadata so each claim DRA generates
// carries them: that is what makes a stale claim findable by the teardown
// sweep when its pod was deleted without it.
func buildRdmaClaimTemplate(evt changeEvent, namespace, deviceClass string) *unstructured.Unstructured {
	labels := resourceLabels(evt)
	devices := evt.Rdma.Devices
	if devices < 1 {
		devices = 1
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "resource.k8s.io/v1",
		"kind":       "ResourceClaimTemplate",
		"metadata": map[string]any{
			"name":      rdmaClaimTemplateName(evt),
			"namespace": namespace,
			"labels":    stringMapToAny(labels),
		},
		"spec": map[string]any{
			// Claims generated from this template inherit these labels.
			"metadata": map[string]any{"labels": stringMapToAny(labels)},
			"spec": map[string]any{
				"devices": map[string]any{
					"requests": []any{map[string]any{
						"name": rdmaClaimName,
						"exactly": map[string]any{
							"deviceClassName": deviceClass,
							// ExactCount with count>1 asks for N distinct
							// devices from the same class, which is what a
							// multi-NIC (rail-optimized) pod needs.
							"allocationMode": "ExactCount",
							"count":          int64(devices),
						},
					}},
				},
			},
		},
	}}
}

// ensureRdmaClaimTemplate creates or updates the service's claim template and
// verifies the DeviceClass exists, so a typo fails the deploy with an
// actionable message instead of leaving the pods unschedulable.
func (r *k8sReconciler) ensureRdmaClaimTemplate(ctx context.Context, evt changeEvent) error {
	if r.dynamic == nil {
		return fmt.Errorf("controller: RDMA needs a dynamic client (DRA), none is configured")
	}
	class := r.rdmaDeviceClass(evt.Rdma.DeviceClass)
	if _, err := r.dynamic.Resource(deviceClassGVR).Get(ctx, class, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("controller: RDMA device class %q does not exist; install the chart's DeviceClass or pass rdma.device_class", class)
		}
		if meta.IsNoMatchError(err) {
			return fmt.Errorf("controller: the cluster has no DRA support (resource.k8s.io DeviceClass), so RDMA cannot be requested")
		}
		return fmt.Errorf("controller: read RDMA device class %q: %w", class, err)
	}
	return applyUnstructuredObject(ctx, r.dynamic, resourceClaimTemplateGVR,
		buildRdmaClaimTemplate(evt, r.namespace, class))
}

// deleteRdmaClaimTemplate removes a service's claim template, ignoring
// not-found. A failure is returned so teardown can retry it.
func (r *k8sReconciler) deleteRdmaClaimTemplate(ctx context.Context, evt changeEvent) error {
	return deleteUnstructuredOwnedBy(ctx, r.dynamic, resourceClaimTemplateGVR, r.namespace,
		rdmaClaimTemplateName(evt), evt.ServiceID)
}

// rdmaDisabled reports whether the event asks for no RDMA device.
func rdmaDisabled(evt changeEvent) bool {
	return evt.Rdma == nil || !evt.Rdma.Enabled || evt.Rdma.Devices < 1
}

// applyRdmaClaims reconciles the RDMA state of one service: the claim template
// exists while the service wants devices, and is removed when it does not.
// Called on every upsert so toggling RDMA off cleans up rather than leaving an
// unused template behind.
func (r *k8sReconciler) applyRdmaClaims(ctx context.Context, evt changeEvent) error {
	if rdmaDisabled(evt) {
		if err := deleteUnstructuredObject(ctx, r.dynamic, resourceClaimTemplateGVR, r.namespace,
			rdmaClaimTemplateName(evt)); err != nil && !apierrors.IsNotFound(err) {
			logger.S().Warnw("controller: delete unused rdma claim template failed",
				"service_id", evt.ServiceID, "name", evt.Name, "err", err)
		}
		return nil
	}
	return r.ensureRdmaClaimTemplate(ctx, evt)
}
