package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/warmup"
)

var (
	localModelNodeGroupGVR = schema.GroupVersionResource{Group: "serving.kserve.io", Version: "v1alpha1", Resource: "localmodelnodegroups"}
	localModelCacheGVR     = schema.GroupVersionResource{Group: "serving.kserve.io", Version: "v1alpha1", Resource: "localmodelcaches"}
	localModelNodeGVR      = schema.GroupVersionResource{Group: "serving.kserve.io", Version: "v1alpha1", Resource: "localmodelnodes"}
)

// ModelWarmupReconciler turns model.warmups messages into KServe
// LocalModelNodeGroup/LocalModelCache resources and mirrors their observed
// state back to the model service.
type ModelWarmupReconciler struct {
	dynamic   dynamic.Interface
	client    mq.Client
	namespace string
	groups    []config.WarmupNodeGroup
	eviction  config.WarmupEviction
	interval  time.Duration
}

// NewModelWarmupReconciler builds the warmup reconciler.
func NewModelWarmupReconciler(dynamicClient dynamic.Interface, client mq.Client, cfg config.WarmupConfig, namespace string) *ModelWarmupReconciler {
	return &ModelWarmupReconciler{
		dynamic:   dynamicClient,
		client:    client,
		namespace: firstNonEmptyTrim(namespace, "taas-infer"),
		groups:    cfg.NodeGroups,
		eviction:  cfg.Eviction,
		interval:  15 * time.Second,
	}
}

// ApplyModelWarmup consumes one warmup request. Cancellation deletes the
// LocalModelCache; a request ensures the node groups and the cache exist.
func (r *ModelWarmupReconciler) ApplyModelWarmup(ctx context.Context, msg mq.Message) error {
	var request warmup.Request
	if err := json.Unmarshal(msg.Body, &request); err != nil {
		return fmt.Errorf("controller: malformed warmup request: %w", err)
	}
	if strings.TrimSpace(request.ID) == "" {
		return fmt.Errorf("controller: warmup request has no id")
	}
	cacheName := warmup.CacheName(request.ID)
	if len(request.NodeGroups) == 0 {
		return r.deleteCache(ctx, cacheName)
	}
	groups := request.NodeGroups
	if len(groups) == 0 {
		groups = r.configuredGroups()
	}
	for _, group := range groups {
		if err := r.ensureNodeGroup(ctx, group); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(groups))
	for _, group := range groups {
		names = append(names, group.Name)
	}
	return r.ensureCache(ctx, &request, cacheName, names)
}

// Run periodically publishes the observed state of every cache this
// controller owns.
func (r *ModelWarmupReconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.reportCaches(ctx); err != nil {
				logger.S().Warnw("controller: model cache scan failed", "err", err)
			}
		}
	}
}

// configuredGroups maps the operator's config onto local node groups.
func (r *ModelWarmupReconciler) configuredGroups() []warmup.NodeGroup {
	out := make([]warmup.NodeGroup, 0, len(r.groups))
	for _, group := range r.groups {
		out = append(out, warmup.NodeGroup{
			Name:         group.Name,
			NodeSelector: group.NodeSelector,
			StorageLimit: group.StorageLimit,
			HostPath:     group.HostPath,
			StorageClass: group.StorageClass,
		})
	}
	return out
}

// ensureNodeGroup creates or updates one LocalModelNodeGroup. The group's
// persistent volume is a hostPath on each node: warmup copies the weights
// into node-local storage, so the group only needs a filesystem the agent
// can write.
func (r *ModelWarmupReconciler) ensureNodeGroup(ctx context.Context, group warmup.NodeGroup) error {
	name := strings.TrimSpace(group.Name)
	if name == "" {
		return fmt.Errorf("controller: warmup node group has no name")
	}
	limit := firstNonEmptyTrim(group.StorageLimit, "100Gi")
	hostPath := firstNonEmptyTrim(group.HostPath, "/var/lib/taas/models")
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelNodeGroup",
		"metadata": map[string]any{
			"name": name,
			"labels": map[string]any{
				"app.kubernetes.io/managed-by": "taas",
			},
		},
		"spec": map[string]any{
			"storageLimit": limit,
			"persistentVolumeSpec": map[string]any{
				"capacity":    map[string]any{"storage": limit},
				"accessModes": []any{"ReadWriteOnce"},
				"hostPath":    map[string]any{"path": hostPath, "type": "DirectoryOrCreate"},
			},
			"persistentVolumeClaimSpec": map[string]any{
				"accessModes": []any{"ReadWriteOnce"},
				"resources": map[string]any{
					"requests": map[string]any{"storage": limit},
				},
			},
		},
	}}
	if class := strings.TrimSpace(group.StorageClass); class != "" {
		object.Object["spec"].(map[string]any)["persistentVolumeClaimSpec"].(map[string]any)["storageClassName"] = class
	}
	resource := r.dynamic.Resource(localModelNodeGroupGVR)
	_, err := resource.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = resource.Create(ctx, object, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	// The storage limit is the only mutable field the platform owns; a
	// changed limit must reach the group, but the volume specs are
	// immutable in Kubernetes.
	_, err = resource.Update(ctx, object, metav1.UpdateOptions{})
	return err
}

// ensureCache creates or updates the LocalModelCache for a request.
func (r *ModelWarmupReconciler) ensureCache(ctx context.Context, request *warmup.Request, name string, groups []string) error {
	size := request.SizeBytes
	if size <= 0 {
		size = 1
	}
	groupValues := make([]any, 0, len(groups))
	for _, group := range groups {
		groupValues = append(groupValues, group)
	}
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelCache",
		"metadata": map[string]any{
			"name": name,
			"labels": map[string]any{
				"app.kubernetes.io/managed-by": "taas",
				"taas.io/model-cache-id":       request.ID,
			},
		},
		"spec": map[string]any{
			"sourceModelUri":     request.SourceURI,
			"modelSize":          resource.NewQuantity(size, resource.BinarySI).String(),
			"nodeGroups":         groupValues,
			"serviceAccountName": "default",
		},
	}}
	resourceAPI := r.dynamic.Resource(localModelCacheGVR)
	existing, err := resourceAPI.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = resourceAPI.Create(ctx, object, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	// sourceModelUri is immutable, and nodeGroups may change when the
	// operator warms the model on a different pool.
	spec, _ := existing.Object["spec"].(map[string]any)
	if spec == nil {
		spec = map[string]any{}
	}
	spec["nodeGroups"] = groupValues
	existing.Object["spec"] = spec
	_, err = resourceAPI.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// deleteCache removes one LocalModelCache; a missing cache is success.
func (r *ModelWarmupReconciler) deleteCache(ctx context.Context, name string) error {
	err := r.dynamic.Resource(localModelCacheGVR).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// reportCaches publishes one status per owned LocalModelCache.
func (r *ModelWarmupReconciler) reportCaches(ctx context.Context) error {
	list, err := r.dynamic.Resource(localModelCacheGVR).List(ctx, metav1.ListOptions{LabelSelector: "taas.io/model-cache-id"})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	for i := range list.Items {
		cache := &list.Items[i]
		id := ""
		if labels := cache.GetLabels(); labels != nil {
			id = labels["taas.io/model-cache-id"]
		}
		if id == "" {
			continue
		}
		status := warmup.Status{
			ID:        id,
			CacheName: cache.GetName(),
			State:     warmup.StateWarming,
			UpdatedAt: time.Now().UTC(),
		}
		spec, _ := cache.Object["spec"].(map[string]any)
		if spec != nil {
			if groups, ok := spec["nodeGroups"].([]any); ok {
				for _, group := range groups {
					if name, ok := group.(string); ok {
						status.NodeGroups = append(status.NodeGroups, name)
					}
				}
			}
			if size, ok := spec["modelSize"].(string); ok {
				if q, err := resource.ParseQuantity(size); err == nil {
					status.SizeBytes = q.Value()
				}
			}
		}
		cacheStatus, _ := cache.Object["status"].(map[string]any)
		if cacheStatus != nil {
			if nodeStatus, ok := cacheStatus["nodeStatus"].(map[string]any); ok {
				for node, state := range nodeStatus {
					if value, ok := state.(string); ok && value == "NodeDownloaded" {
						status.WarmedNodes = append(status.WarmedNodes, node)
					}
				}
				status.TotalNodes = len(nodeStatus)
			}
			if copies, ok := cacheStatus["copies"].(map[string]any); ok {
				if available, ok := copies["available"].(int64); ok && available > 0 {
					status.State = warmup.StateReady
				}
			}
		}
		if status.State != warmup.StateReady && len(status.WarmedNodes) > 0 {
			status.State = warmup.StateReady
		}
		if status.State == warmup.StateReady {
			status.Message = ""
		}
		body, err := json.Marshal(&status)
		if err != nil {
			return err
		}
		if err := r.client.Publish(ctx, mq.DefaultSubjects().ModelWarmupStatus, body, nil); err != nil {
			return err
		}
	}
	return nil
}

// applyWarmupAffinity injects the model-aware node affinity into a serving
// pod spec. It reads the LocalModelNode index so a pod is placed on a node
// whose local cache already holds the weights.
func (r *k8sReconciler) applyWarmupAffinity(ctx context.Context, spec map[string]any, modelName string) {
	if !r.warmupEnabled || r.dynamic == nil || r.warmupMode == "disabled" || strings.TrimSpace(modelName) == "" {
		return
	}
	nodeList, err := r.dynamic.Resource(localModelNodeGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logger.S().Warnw("controller: list local model nodes failed", "err", err)
		}
		return
	}
	nodes := warmedNodesForModel(nodeList.Items, modelName)
	if len(nodes) == 0 {
		return
	}
	terms := []any{map[string]any{
		"matchExpressions": []any{map[string]any{
			"key":      "kubernetes.io/hostname",
			"operator": "In",
			"values":   stringSliceToAny(nodes),
		}},
	}}
	var affinity map[string]any
	if existing, ok := spec["affinity"].(map[string]any); ok {
		affinity = existing
	} else {
		affinity = map[string]any{}
	}
	if r.warmupMode == "required" {
		affinity["nodeAffinity"] = map[string]any{
			"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{"nodeSelectorTerms": terms},
		}
	} else {
		affinity["nodeAffinity"] = map[string]any{
			"preferredDuringSchedulingIgnoredDuringExecution": []any{map[string]any{
				"weight":     int64(r.warmupWeight),
				"preference": map[string]any{"matchExpressions": terms[0].(map[string]any)["matchExpressions"]},
			}},
		}
	}
	spec["affinity"] = affinity
}

// warmedNodesForModel collects the nodes whose LocalModelNode reports a
// downloaded copy for the model.
func warmedNodesForModel(nodes []unstructured.Unstructured, modelName string) []string {
	out := make([]string, 0)
	for i := range nodes {
		node := &nodes[i]
		spec, _ := node.Object["spec"].(map[string]any)
		if spec == nil {
			continue
		}
		localModels, _ := spec["localModels"].([]any)
		found := false
		for _, entry := range localModels {
			model, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			name, _ := model["modelName"].(string)
			if name == modelName {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		status, _ := node.Object["status"].(map[string]any)
		if status != nil {
			if modelStatus, ok := status["modelStatus"].(map[string]any); ok {
				for key, value := range modelStatus {
					if strings.Contains(key, modelName) && value == "ModelDownloaded" {
						out = append(out, node.GetName())
						break
					}
				}
			}
		}
	}
	return out
}

func stringSliceToAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
