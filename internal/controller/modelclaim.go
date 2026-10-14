package controller

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/go-taas/go-taas/pkg/logger"
)

// AIBrix CRD GVRs (feature #47, AD5/AD13). There is no vendored AIBrix Go
// client, so the controller writes these resources through the dynamic
// client.
var (
	modelClaimGVR    = schema.GroupVersionResource{Group: "model.aibrix.ai", Version: "v1alpha1", Resource: "modelclaims"}
	podAutoscalerGVR = schema.GroupVersionResource{Group: "autoscaling.aibrix.ai", Version: "v1alpha1", Resource: "podautoscalers"}
)

// ModelClaim labels (feature #47, §13.4).
const (
	mcLabelManagedBy = "app.kubernetes.io/managed-by"
	mcLabelOrigin    = "taas.go-taas.github.io/origin"
	mcLabelServiceID = "taas.go-taas.github.io/service-id"
	mcLabelModelID   = "taas.go-taas.github.io/model-id"
	mcLabelModel     = "taas.go-taas.github.io/model"

	originService      = "service"
	originRegistration = "registration"

	managedByValue = "taas-controller"
)

// invalidDNSChars matches characters not allowed in a DNS-1123 subdomain.
var invalidDNSChars = regexp.MustCompile(`[^a-z0-9.-]+`)

// modelClaimName sanitizes a model name into a DNS-1123 subdomain and
// prefixes "mc-" so it never collides with an AIBrix-owned name.
func modelClaimName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = invalidDNSChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		s = "model"
	}
	s = "mc-" + s
	if len(s) > 253 {
		s = s[:253]
	}
	return strings.TrimRight(s, "-.")
}

// buildModelClaimFromEvent composes the ModelClaim for an inference
// service change event (feature #47, §13.4). It is derived from the same
// spec the Deployment is built from.
func buildModelClaimFromEvent(evt changeEvent, namespace, mountPath string) *unstructured.Unstructured {
	labels := map[string]any{
		mcLabelManagedBy: managedByValue,
		mcLabelOrigin:    originService,
		mcLabelServiceID: evt.ServiceID,
	}
	if evt.Model.ModelID != "" {
		labels[mcLabelModelID] = evt.Model.ModelID
	}
	claim := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "model.aibrix.ai/v1alpha1",
		"kind":       "ModelClaim",
		"metadata": map[string]any{
			"name":      modelClaimName(evt.Name),
			"namespace": namespace,
			"labels":    labels,
		},
		"spec": map[string]any{
			"modelName":   evt.Name,
			"engine":      evt.Image.Engine,
			"artifactURL": modelDir(mountPath, evt.Model.WeightPath),
			"replicas":    int64(clampReplicas(evt.Replicas)),
			"podSelector": map[string]any{
				"matchLabels": stringMapToAny(resourceLabels(evt)),
			},
		},
	}}
	return claim
}

// buildModelClaimFromChange composes the ModelClaim for a runtime
// registration change (feature #47, §13.4).
func buildModelClaimFromChange(change *runtimeModelChange, namespace string) *unstructured.Unstructured {
	podSelector := change.PodSelector
	if len(podSelector) == 0 {
		podSelector = map[string]string{mcLabelModel: change.ModelName}
	}
	spec := map[string]any{
		"modelName":   change.ModelName,
		"engine":      change.Engine,
		"artifactURL": change.ArtifactURL,
		"replicas":    change.Replicas,
		"podSelector": map[string]any{
			"matchLabels": stringMapToAny(podSelector),
		},
	}
	if len(change.EngineArgs) > 0 {
		spec["engineConfig"] = map[string]any{"args": stringMapToAny(change.EngineArgs)}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "model.aibrix.ai/v1alpha1",
		"kind":       "ModelClaim",
		"metadata": map[string]any{
			"name":      modelClaimName(change.ModelName),
			"namespace": namespace,
			"labels": map[string]any{
				mcLabelManagedBy: managedByValue,
				mcLabelOrigin:    originRegistration,
			},
		},
		"spec": spec,
	}}
}

// applyModelClaim creates or updates a ModelClaim (idempotent). The write
// goes through the dynamic client; failures are returned so the caller can
// fall back to the plain path (AD13).
func applyModelClaim(ctx context.Context, dyn dynamic.Interface, claim *unstructured.Unstructured) error {
	if dyn == nil {
		return fmt.Errorf("controller: dynamic client unavailable")
	}
	namespace := claim.GetNamespace()
	name := claim.GetName()
	existing, err := dyn.Resource(modelClaimGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = dyn.Resource(modelClaimGVR).Namespace(namespace).Create(ctx, claim, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	claim.SetResourceVersion(existing.GetResourceVersion())
	_, err = dyn.Resource(modelClaimGVR).Namespace(namespace).Update(ctx, claim, metav1.UpdateOptions{})
	return err
}

// deleteModelClaim deletes a ModelClaim by name, ignoring not-found.
func deleteModelClaim(ctx context.Context, dyn dynamic.Interface, namespace, name string) error {
	if dyn == nil {
		return nil
	}
	err := dyn.Resource(modelClaimGVR).Namespace(namespace).Delete(ctx, modelClaimName(name), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// podAutoscalerScalingStrategy is the AIBrix scaling strategy the controller
// requests. The PodAutoscaler CRD requires spec.scalingStrategy to be one of
// "HPA", "KPA", "APA"; an empty value is rejected with a 422. The controller
// drives replica counts from CPU/metric thresholds exactly like the plain
// Kubernetes HPA path, so "HPA" is the strategy that preserves the existing
// autoscaling semantics (feature #47, AD13).
const podAutoscalerScalingStrategy = "HPA"

// podAutoscalerMetricSourceType is the metricsSources entry type the
// controller requests. The CRD enum is pod|resource|custom|external|domain;
// "pod" scopes the metric to the target pods, matching the plain HPA
// path's per-pod concurrency object metric (feature #47, AD13).
const podAutoscalerMetricSourceType = "pod"

// podAutoscalerTargetMetric is the concurrency metric the plain HPA path
// scales on (reconciler buildHPA); the AIBrix PodAutoscaler scales on the
// same metric so both schemes preserve the platform's autoscaling contract.
const podAutoscalerTargetMetric = "taas-infer-concurrency"

// buildPodAutoscaler composes the AIBrix PodAutoscaler for a change event
// with autoscaling enabled (feature #47, §13.5, AD13). The spec must satisfy
// every CRD-required field (scaleTargetRef, maxReplicas, scalingStrategy)
// and the admission webhook's constraint that spec.metricsSources carries at
// least one entry with its required per-item fields (metricSourceType,
// targetMetric, targetValue).
func buildPodAutoscaler(namespace, deployment, name string, minReplicas, maxReplicas int32, targetConcurrency int32) *unstructured.Unstructured {
	if targetConcurrency < 1 {
		targetConcurrency = 32
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "autoscaling.aibrix.ai/v1alpha1",
		"kind":       "PodAutoscaler",
		"metadata": map[string]any{
			"name":      hpaName(name),
			"namespace": namespace,
			"labels":    map[string]any{mcLabelManagedBy: managedByValue},
		},
		"spec": map[string]any{
			"scaleTargetRef": map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"name":       deployment,
			},
			"minReplicas":     int64(minReplicas),
			"maxReplicas":     int64(maxReplicas),
			"scalingStrategy": podAutoscalerScalingStrategy,
			"metricsSources": []any{map[string]any{
				"metricSourceType": podAutoscalerMetricSourceType,
				"targetMetric":     podAutoscalerTargetMetric,
				"targetValue":      strconv.FormatInt(int64(targetConcurrency), 10),
			}},
		},
	}}
}

// applyPodAutoscaler creates or updates the AIBrix PodAutoscaler for a
// change event with autoscaling enabled (feature #47, §13.5).
func applyPodAutoscaler(ctx context.Context, dyn dynamic.Interface, namespace, deployment, name string, minReplicas, maxReplicas, targetConcurrency int32) error {
	if dyn == nil {
		return fmt.Errorf("controller: dynamic client unavailable")
	}
	desired := buildPodAutoscaler(namespace, deployment, name, minReplicas, maxReplicas, targetConcurrency)
	existing, err := dyn.Resource(podAutoscalerGVR).Namespace(namespace).Get(ctx, hpaName(name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = dyn.Resource(podAutoscalerGVR).Namespace(namespace).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.SetResourceVersion(existing.GetResourceVersion())
	_, err = dyn.Resource(podAutoscalerGVR).Namespace(namespace).Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

// deletePodAutoscaler deletes the AIBrix PodAutoscaler by name, ignoring
// not-found.
func deletePodAutoscaler(ctx context.Context, dyn dynamic.Interface, namespace, name string) {
	if dyn == nil {
		return
	}
	err := dyn.Resource(podAutoscalerGVR).Namespace(namespace).Delete(ctx, hpaName(name), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		logger.S().Warnw("controller: delete aibrix podautoscaler failed", "name", name, "err", err)
	}
}

// stringMapToAny converts a string map into the map[string]any shape the
// unstructured API expects.
func stringMapToAny(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
