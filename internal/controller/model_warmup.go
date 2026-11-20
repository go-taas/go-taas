package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
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
	"github.com/go-taas/go-taas/pkg/storage"
	"github.com/go-taas/go-taas/pkg/warmup"
)

var (
	localModelNodeGroupGVR = schema.GroupVersionResource{Group: "serving.kserve.io", Version: "v1alpha1", Resource: "localmodelnodegroups"}
	localModelCacheGVR     = schema.GroupVersionResource{Group: "serving.kserve.io", Version: "v1alpha1", Resource: "localmodelcaches"}
	localModelNodeGVR      = schema.GroupVersionResource{Group: "serving.kserve.io", Version: "v1alpha1", Resource: "localmodelnodes"}
	jobGVR                 = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
)

const (
	// Labels and mount points of the node-local seed Job.
	warmupSeedLabel       = "taas.io/model-warmup-seed"
	warmupSeedCacheLabel  = "taas.io/model-cache-id"
	warmupSeedNodeLabel   = "taas.io/model-warmup-node"
	warmupSeedSourceMount = "/source"
	warmupSeedTargetMount = "/target"
)

// WarmupSeedConfig describes the Job that pre-populates a node-local cache.
// KServe's local-model agent may have no credentials for the platform's
// object store, and its storage initializer cannot read pvc:// at all, so the
// controller seeds both source kinds itself and then marks the
// LocalModelNode entry as downloaded.
type WarmupSeedConfig struct {
	Image          string
	Namespace      string
	ServiceAccount string
	NodeSelector   map[string]string
	Tolerations    []map[string]any
}

// ModelWarmupReconciler turns model.warmups messages into KServe
// LocalModelNodeGroup/LocalModelCache resources and mirrors their observed
// state back to the model service.
type ModelWarmupReconciler struct {
	dynamic   dynamic.Interface
	client    mq.Client
	namespace string
	eviction  config.WarmupEviction
	interval  time.Duration
	seed      WarmupSeedConfig
	storage   storage.Config
}

// NewModelWarmupReconciler builds the warmup reconciler.
func NewModelWarmupReconciler(dynamicClient dynamic.Interface, client mq.Client, cfg config.WarmupConfig, storageCfg storage.Config, namespace string, seed WarmupSeedConfig) *ModelWarmupReconciler {
	return &ModelWarmupReconciler{
		dynamic:   dynamicClient,
		client:    client,
		namespace: firstNonEmptyTrim(namespace, "taas-infer"),
		eviction:  cfg.Eviction,
		interval:  15 * time.Second,
		seed:      seed,
		storage:   storageCfg,
	}
}

// ApplyNodeGroup consumes one platform-managed node-group create/update or
// delete. Node groups are not Helm values: the model service publishes every
// edit, so the KServe LocalModelNodeGroup follows the database row.
func (r *ModelWarmupReconciler) ApplyNodeGroup(ctx context.Context, msg mq.Message) error {
	var event warmup.NodeGroupEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil {
		return fmt.Errorf("controller: malformed warmup node group event: %w", err)
	}
	if event.Snapshot {
		return r.syncNodeGroups(ctx, event.NodeGroups)
	}
	name := strings.TrimSpace(event.NodeGroup.Name)
	if name == "" {
		return fmt.Errorf("controller: warmup node group event has no name")
	}
	if event.Deleted {
		return r.deleteNodeGroup(ctx, name)
	}
	return r.ensureNodeGroup(ctx, event.NodeGroup)
}

// syncNodeGroups converges the managed LocalModelNodeGroups to the desired
// set. It creates or updates every group in the snapshot and deletes managed
// groups the snapshot no longer contains, so a deletion that happened while
// the controller was down still takes effect.
func (r *ModelWarmupReconciler) syncNodeGroups(ctx context.Context, groups []warmup.NodeGroup) error {
	desired := make(map[string]bool, len(groups))
	for _, group := range groups {
		name := strings.TrimSpace(group.Name)
		if name == "" {
			continue
		}
		desired[name] = true
		if err := r.ensureNodeGroup(ctx, group); err != nil {
			return err
		}
	}
	existing, err := r.dynamic.Resource(localModelNodeGroupGVR).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/managed-by=taas",
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, item := range existing.Items {
		if desired[item.GetName()] {
			continue
		}
		if err := r.deleteNodeGroup(ctx, item.GetName()); err != nil {
			return err
		}
	}
	return nil
}

// deleteNodeGroup removes a LocalModelNodeGroup; a missing group is success.
func (r *ModelWarmupReconciler) deleteNodeGroup(ctx context.Context, name string) error {
	err := r.dynamic.Resource(localModelNodeGroupGVR).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
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
			if err := r.seedCaches(ctx); err != nil {
				logger.S().Warnw("controller: model cache seed failed", "err", err)
			}
			if err := r.reportCaches(ctx); err != nil {
				logger.S().Warnw("controller: model cache scan failed", "err", err)
			}
			if err := r.reportNodes(ctx); err != nil {
				logger.S().Warnw("controller: local model node scan failed", "err", err)
			}
		}
	}
}

// nodeSelectorAffinity converts a label selector into the required node
// affinity term KServe matches nodes against.
func nodeSelectorAffinity(selector map[string]string) map[string]any {
	if len(selector) == 0 {
		return nil
	}
	keys := make([]string, 0, len(selector))
	for key := range selector {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	expressions := make([]any, 0, len(keys))
	for _, key := range keys {
		expressions = append(expressions, map[string]any{
			"key":      key,
			"operator": "In",
			"values":   []any{selector[key]},
		})
	}
	return map[string]any{
		"required": map[string]any{
			"nodeSelectorTerms": []any{map[string]any{"matchExpressions": expressions}},
		},
	}
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
	// The default group is backed by a hostPath PV. Both the PV and the
	// PVC must carry the same class, and the empty class is what stops the
	// cluster default StorageClass from hijacking the claim (a PVC without
	// storageClassName is defaulted to the cluster default and then never
	// matches the classless hostPath PV).
	class := strings.TrimSpace(group.StorageClass)
	volumeSpec := map[string]any{
		"capacity":         map[string]any{"storage": limit},
		"accessModes":      []any{"ReadWriteOnce"},
		"hostPath":         map[string]any{"path": hostPath, "type": "DirectoryOrCreate"},
		"storageClassName": class,
	}
	// KServe decides which nodes belong to a group by matching the
	// persistent volume's node affinity against the node labels. Without
	// it every group is empty and no LocalModelNode is ever created, so the
	// configured selector is translated into a required node affinity.
	if affinity := nodeSelectorAffinity(group.NodeSelector); affinity != nil {
		volumeSpec["nodeAffinity"] = affinity
	}
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
			"storageLimit":         limit,
			"persistentVolumeSpec": volumeSpec,
			"persistentVolumeClaimSpec": map[string]any{
				"accessModes":      []any{"ReadWriteOnce"},
				"storageClassName": class,
				"resources": map[string]any{
					"requests": map[string]any{"storage": limit},
				},
			},
		},
	}}
	resource := r.dynamic.Resource(localModelNodeGroupGVR)
	existing, err := resource.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = resource.Create(ctx, object, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	// An update must carry the resource version of the object it replaces,
	// otherwise the API server rejects it as a stale write.
	object.SetResourceVersion(existing.GetResourceVersion())
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
			"annotations": map[string]any{
				"taas.io/model-name":    request.ModelName,
				"taas.io/model-version": request.Version,
				"taas.io/source-uri":    request.SourceURI,
				"taas.io/backend":       request.Backend,
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
	annotations := existing.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["taas.io/model-name"] = request.ModelName
	annotations["taas.io/model-version"] = request.Version
	annotations["taas.io/source-uri"] = request.SourceURI
	annotations["taas.io/backend"] = request.Backend
	existing.SetAnnotations(annotations)
	_, err = resourceAPI.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// warmupSeedSource is one cache source the controller can seed itself: a
// filesystem claim mount, or an object-store prefix fetched by the transfer
// binary.
type warmupSeedSource struct {
	URI        string
	Claim      string
	Path       string
	WeightPath string
	Remote     *storage.RemoteRef
	Secret     string
	AccessKey  string
	SecretKey  string
}

// seedCaches populates node-local caches from either a filesystem PVC or a
// configured object store. KServe's storage initializer cannot read pvc://,
// and its node agent may not carry the platform's object-store credentials,
// so the controller copies the weights onto the node itself. Once the copy
// succeeds the matching LocalModelNode entry is marked downloaded; from then
// on the KServe agent treats the cache as present on that node.
func (r *ModelWarmupReconciler) seedCaches(ctx context.Context) error {
	if strings.TrimSpace(r.seed.Image) == "" || strings.TrimSpace(r.seed.Namespace) == "" {
		return nil
	}
	caches, err := r.dynamic.Resource(localModelCacheGVR).List(ctx, metav1.ListOptions{LabelSelector: warmupSeedCacheLabel})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if len(caches.Items) == 0 {
		return nil
	}
	nodes, err := r.dynamic.Resource(localModelNodeGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	for i := range caches.Items {
		cache := &caches.Items[i]
		cacheName := cache.GetName()
		spec, _ := cache.Object["spec"].(map[string]any)
		if spec == nil {
			continue
		}
		sourceURI, _ := spec["sourceModelUri"].(string)
		source, ok := r.seedSource(sourceURI, cache.GetAnnotations()["taas.io/backend"])
		if !ok {
			// HTTP sources and sources no configured backend owns are left
			// to KServe's own storage initializer.
			continue
		}
		allowed := map[string]bool{}
		if groups, ok := spec["nodeGroups"].([]any); ok {
			for _, group := range groups {
				if name, ok := group.(string); ok {
					allowed[name] = true
				}
			}
		}
		for j := range nodes.Items {
			node := &nodes.Items[j]
			hostPath, modelName, ok := r.seedTarget(ctx, node, cacheName, allowed)
			if !ok {
				continue
			}
			if nodeHasModel(node, cacheName) {
				continue
			}
			if err := r.ensureSeedJob(ctx, cacheName, modelName, node.GetName(), hostPath, source); err != nil {
				return err
			}
		}
	}
	return nil
}

// seedSource resolves a cache source URI against the configured backends.
// An empty backend annotation falls back to matching the URI against each
// backend's bucket and prefix, so caches created before the annotation was
// added still seed.
func (r *ModelWarmupReconciler) seedSource(sourceURI, backendName string) (warmupSeedSource, bool) {
	if claim, path, ok := parsePVCURI(sourceURI); ok {
		return warmupSeedSource{URI: sourceURI, Claim: claim, Path: path}, true
	}
	backend, ok := r.backendForURI(sourceURI, backendName)
	if !ok || backend.Type != storage.TypeObjectStore {
		return warmupSeedSource{}, false
	}
	bucket := strings.TrimSpace(backend.ObjectStore.Bucket)
	prefix := strings.Trim(strings.TrimSpace(backend.ObjectStore.Prefix), "/")
	rest := strings.TrimPrefix(sourceURI, "s3://"+bucket+"/")
	if rest == sourceURI || strings.TrimSpace(rest) == "" {
		return warmupSeedSource{}, false
	}
	weightPath := strings.TrimPrefix(strings.TrimPrefix(rest, prefix), "/")
	if strings.TrimSpace(weightPath) == "" {
		return warmupSeedSource{}, false
	}
	ref := &storage.RemoteRef{ //nolint:gosec // env names, not credential values
		Provider:       backend.ObjectStore.Provider,
		Endpoint:       backend.ObjectStore.Endpoint,
		Region:         backend.ObjectStore.Region,
		Bucket:         bucket,
		Prefix:         prefix,
		ForcePathStyle: backend.ObjectStore.ForcePathStyle,
		// These are environment variable names, not credential values.
		AccessKeyEnv: "WARMUP_SOURCE_ACCESS_KEY",
		SecretKeyEnv: "WARMUP_SOURCE_SECRET_KEY", //nolint:gosec // env name, not a secret

	}
	creds := backend.ObjectStore.Credentials
	return warmupSeedSource{
		URI:        sourceURI,
		WeightPath: weightPath,
		Remote:     ref,
		Secret:     creds.ExistingSecret,
		AccessKey:  firstNonEmptyTrim(creds.AccessKeyKey, "accessKey"),
		SecretKey:  firstNonEmptyTrim(creds.SecretKeyKey, "secretKey"),
	}, true
}

// backendForURI returns the backend a cache URI belongs to, by name when the
// cache carries it and otherwise by bucket/prefix match.
func (r *ModelWarmupReconciler) backendForURI(sourceURI, backendName string) (storage.Backend, bool) {
	if strings.TrimSpace(backendName) != "" {
		backend, err := r.storage.Resolve(backendName)
		return backend, err == nil
	}
	for name := range r.storage.Backends {
		backend, err := r.storage.Resolve(name)
		if err != nil {
			continue
		}
		if backend.Type != storage.TypeObjectStore {
			continue
		}
		bucket := strings.TrimSpace(backend.ObjectStore.Bucket)
		prefix := strings.Trim(strings.TrimSpace(backend.ObjectStore.Prefix), "/")
		root := "s3://" + bucket
		if prefix != "" {
			root += "/" + prefix + "/"
		} else {
			root += "/"
		}
		if strings.HasPrefix(sourceURI, root) {
			return backend, true
		}
	}
	return storage.Backend{}, false
}

// seedTarget returns the node host path and model identity when a node
// belongs to one of the cache's node groups.
func (r *ModelWarmupReconciler) seedTarget(ctx context.Context, node *unstructured.Unstructured, cacheName string, allowed map[string]bool) (hostPath, modelName string, ok bool) {
	spec, _ := node.Object["spec"].(map[string]any)
	if spec == nil {
		return "", "", false
	}
	localModels, _ := spec["localModels"].([]any)
	for _, entry := range localModels {
		model, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, _ := model["modelName"].(string)
		if name != cacheName {
			continue
		}
		group, _ := model["nodeGroup"].(string)
		if len(allowed) > 0 && !allowed[group] {
			continue
		}
		hostPath = hostPathForGroup(ctx, r.dynamic, group)
		return hostPath, name, true
	}
	return "", "", false
}

// hostPathForGroup reads the host path from a node group's
// LocalModelNodeGroup. The database row is the source of truth, so neither
// the warmup nor the serving path keeps a second copy of the group settings.
func hostPathForGroup(ctx context.Context, dynamicClient dynamic.Interface, name string) string {
	const fallback = "/models"
	if dynamicClient == nil || name == "" {
		return fallback
	}
	group, err := dynamicClient.Resource(localModelNodeGroupGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fallback
	}
	hostPath := nestedString(group, "spec", "persistentVolumeSpec", "hostPath", "path")
	return firstNonEmptyTrim(hostPath, fallback)
}

// nodeHasModel reports whether a LocalModelNode already records the cache as
// downloaded.
func nodeHasModel(node *unstructured.Unstructured, cacheName string) bool {
	status, _ := node.Object["status"].(map[string]any)
	if status == nil {
		return false
	}
	modelStatus, _ := status["modelStatus"].(map[string]any)
	if modelStatus == nil {
		return false
	}
	state, _ := modelStatus[cacheName].(string)
	return state == "ModelDownloaded"
}

// ensureSeedJob creates the one-shot copy Job for a node, or completes it by
// marking the node downloaded once the Job succeeded. A failed Job is
// deleted so the next scan can retry from a clean pod.
func (r *ModelWarmupReconciler) ensureSeedJob(ctx context.Context, cacheName, modelName, nodeName, hostPath string, source warmupSeedSource) error {
	name := seedJobName(cacheName, nodeName)
	resourceAPI := r.dynamic.Resource(jobGVR)
	existing, err := resourceAPI.Namespace(r.seed.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		status, _ := existing.Object["status"].(map[string]any)
		succeeded := nestedInt64(status, "succeeded")
		failed := nestedInt64(status, "failed")
		if succeeded > 0 {
			return r.markNodeDownloaded(ctx, nodeName, cacheName)
		}
		if failed > 0 {
			_ = resourceAPI.Namespace(r.seed.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
			return nil
		}
		// A running Job keeps going; the next scan observes its outcome.
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	sourcePath := strings.Trim(strings.TrimSpace(source.Path), "/")
	sourceMountPath := warmupSeedSourceMount
	if sourcePath != "" {
		sourceMountPath = warmupSeedSourceMount + "/" + sourcePath
	}
	targetPath := warmupSeedTargetMount + "/models/" + storageKeyOf(source.URI)
	spec := map[string]any{
		"id":         fmt.Sprintf("warm-%s-%s", cacheName, nodeName),
		"operation":  "copy",
		"modelId":    modelName,
		"modelName":  modelName,
		"sourceUri":  source.URI,
		"targetUri":  "file://" + targetPath,
		"targetPath": targetPath,
	}
	if source.Remote != nil {
		spec["weightPath"] = source.WeightPath
		spec["sourceObjectStore"] = source.Remote
	} else {
		spec["sourcePath"] = sourceMountPath
	}
	env, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	backoff := int64(1)
	ttl := int64(3600)
	job := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      name,
			"namespace": r.seed.Namespace,
			"labels": map[string]any{
				warmupSeedLabel:      "true",
				warmupSeedCacheLabel: cacheName,
				warmupSeedNodeLabel:  nodeName,
			},
		},
		"spec": map[string]any{
			"backoffLimit":            backoff,
			"ttlSecondsAfterFinished": ttl,
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]any{
						warmupSeedLabel:      "true",
						warmupSeedCacheLabel: cacheName,
						warmupSeedNodeLabel:  nodeName,
					},
				},
				"spec": map[string]any{
					"restartPolicy":      "Never",
					"nodeName":           nodeName,
					"serviceAccountName": r.seed.ServiceAccount,
					// Uploaded weights can be owned by an arbitrary uid and
					// mode 0750; the seed copy therefore runs privileged
					// enough to read them and writes world-readable files.
					"securityContext": map[string]any{
						"runAsUser": int64(0),
					},
					"containers": []any{map[string]any{
						"name":            "warm-seed",
						"image":           r.seed.Image,
						"imagePullPolicy": "IfNotPresent",
						"command":         []any{"/usr/local/bin/taas-transfer"},
						"env": append([]any{
							map[string]any{"name": "TRANSFER_SPEC_JSON", "value": string(env)},
							map[string]any{"name": "POD_NAMESPACE", "value": r.seed.Namespace},
						}, seedCredentialEnv(source)...),
						"volumeMounts": seedVolumeMounts(source),
					}},
					"volumes": seedVolumes(source, hostPath),
				},
			},
		},
	}}
	podSpec, _ := nestedMap(job.Object, "spec", "template", "spec")
	if len(r.seed.NodeSelector) > 0 {
		selector := map[string]any{}
		for key, value := range r.seed.NodeSelector {
			selector[key] = value
		}
		podSpec["nodeSelector"] = selector
	}
	if len(r.seed.Tolerations) > 0 {
		tolerations := make([]any, 0, len(r.seed.Tolerations))
		for _, toleration := range r.seed.Tolerations {
			tolerations = append(tolerations, toAnyMap(toleration))
		}
		podSpec["tolerations"] = tolerations
	}
	_, err = resourceAPI.Namespace(r.seed.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	if err != nil {
		return err
	}
	logger.S().Infow("controller: model warmup seed job created", "cache", cacheName, "node", nodeName, "job", name)
	return nil
}

// markNodeDownloaded records the cache as present on a node.
func (r *ModelWarmupReconciler) markNodeDownloaded(ctx context.Context, nodeName, cacheName string) error {
	resourceAPI := r.dynamic.Resource(localModelNodeGVR)
	node, err := resourceAPI.Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	status, _ := node.Object["status"].(map[string]any)
	if status == nil {
		status = map[string]any{}
	}
	modelStatus, _ := status["modelStatus"].(map[string]any)
	if modelStatus == nil {
		modelStatus = map[string]any{}
	}
	if modelStatus[cacheName] == "ModelDownloaded" {
		return nil
	}
	modelStatus[cacheName] = "ModelDownloaded"
	status["modelStatus"] = modelStatus
	node.Object["status"] = status
	_, err = resourceAPI.UpdateStatus(ctx, node, metav1.UpdateOptions{})
	return err
}

// seedCredentialEnv projects an object-store Secret into the seed Job. The
// controller never reads the values: kubelet resolves the secretKeyRef when
// the pod starts, so the credentials stay in the inference namespace.
func seedCredentialEnv(source warmupSeedSource) []any {
	if source.Remote == nil || strings.TrimSpace(source.Secret) == "" {
		return nil
	}
	return []any{
		map[string]any{
			"name": source.Remote.AccessKeyEnv,
			"valueFrom": map[string]any{
				"secretKeyRef": map[string]any{"name": source.Secret, "key": source.AccessKey},
			},
		},
		map[string]any{
			"name": source.Remote.SecretKeyEnv,
			"valueFrom": map[string]any{
				"secretKeyRef": map[string]any{"name": source.Secret, "key": source.SecretKey},
			},
		},
	}
}

func seedVolumeMounts(source warmupSeedSource) []any {
	mounts := []any{
		map[string]any{"name": "target", "mountPath": warmupSeedTargetMount},
	}
	if source.Remote == nil {
		mounts = append([]any{
			map[string]any{"name": "source", "mountPath": warmupSeedSourceMount, "readOnly": true},
		}, mounts...)
	}
	return mounts
}

func seedVolumes(source warmupSeedSource, hostPath string) []any {
	volumes := []any{
		map[string]any{"name": "target", "hostPath": map[string]any{"path": hostPath, "type": "DirectoryOrCreate"}},
	}
	if source.Remote == nil {
		volumes = append([]any{
			map[string]any{"name": "source", "persistentVolumeClaim": map[string]any{"claimName": source.Claim}},
		}, volumes...)
	}
	return volumes
}

// parsePVCURI splits pvc://claim/path into its parts.
func parsePVCURI(uri string) (claim, path string, ok bool) {
	const prefix = "pvc://"
	if !strings.HasPrefix(uri, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(uri, prefix)
	parts := strings.SplitN(rest, "/", 2)
	claim = strings.TrimSpace(parts[0])
	if claim == "" {
		return "", "", false
	}
	if len(parts) == 2 {
		path = strings.Trim(parts[1], "/")
	}
	return claim, path, true
}

// storageKeyOf is the KServe local-model storage key: the first 16 hex
// characters of the SHA-256 of the source URI.
func storageKeyOf(sourceURI string) string {
	sum := sha256.Sum256([]byte(sourceURI))
	return hex.EncodeToString(sum[:])[:16]
}

// seedJobName builds a DNS-safe Job name for one cache/node pair. A long
// pair is shortened with a hash suffix so two nodes never share a Job.
func seedJobName(cacheName, nodeName string) string {
	name := "taas-warm-" + strings.ToLower(cacheName+"-"+nodeName)
	replacer := strings.NewReplacer("_", "-", ".", "-")
	name = strings.Trim(replacer.Replace(name), "-")
	if len(name) <= 63 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := hex.EncodeToString(sum[:])[:8]
	prefix := strings.TrimRight(name[:63-len(suffix)-1], "-._")
	return prefix + "-" + suffix
}

func nestedMap(object map[string]any, keys ...string) (map[string]any, bool) {
	current := object
	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func nestedInt64(object map[string]any, key string) int64 {
	if object == nil {
		return 0
	}
	switch value := object[key].(type) {
	case int64:
		return value
	case int32:
		return int64(value)
	case int:
		return int64(value)
	case float64:
		return int64(value)
	}
	return 0
}

func toAnyMap(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
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

// reportNodes publishes the full LocalModelNode snapshot: each node's
// groups and the model copies it records. The server replaces its projection
// with this snapshot, so a deleted node disappears from the console.
func (r *ModelWarmupReconciler) reportNodes(ctx context.Context) error {
	list, err := r.dynamic.Resource(localModelNodeGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	now := time.Now().UTC()
	snapshot := warmup.NodeSnapshot{Nodes: make([]warmup.Node, 0, len(list.Items))}
	for i := range list.Items {
		node := &list.Items[i]
		observed := warmup.Node{Name: node.GetName(), ObservedAt: now}
		seenGroups := map[string]bool{}
		spec, _ := node.Object["spec"].(map[string]any)
		if spec != nil {
			if localModels, ok := spec["localModels"].([]any); ok {
				for _, entry := range localModels {
					model, ok := entry.(map[string]any)
					if !ok {
						continue
					}
					name, _ := model["modelName"].(string)
					group, _ := model["nodeGroup"].(string)
					if name == "" {
						continue
					}
					if group != "" && !seenGroups[group] {
						seenGroups[group] = true
						observed.NodeGroups = append(observed.NodeGroups, group)
					}
					observed.Models = append(observed.Models, warmup.NodeModel{
						CacheName: name, NodeGroup: group,
					})
				}
			}
		}
		status, _ := node.Object["status"].(map[string]any)
		if status != nil {
			if modelStatus, ok := status["modelStatus"].(map[string]any); ok {
				for i := range observed.Models {
					if state, ok := modelStatus[observed.Models[i].CacheName].(string); ok {
						observed.Models[i].State = state
					}
				}
			}
		}
		sort.Strings(observed.NodeGroups)
		observed.ModelCount = len(observed.Models)
		for _, model := range observed.Models {
			if model.State == "ModelDownloaded" {
				observed.DownloadedCount++
			}
		}
		snapshot.Nodes = append(snapshot.Nodes, observed)
	}
	body, err := json.Marshal(&snapshot)
	if err != nil {
		return err
	}
	return r.client.Publish(ctx, mq.DefaultSubjects().ModelWarmupNodes, body, nil)
}

// applyWarmupAffinity injects the model-aware node affinity into a serving
// pod spec. It reads the LocalModelNode index so a pod is placed on a node
// whose local cache already holds the weights.
func (r *k8sReconciler) applyWarmupAffinity(ctx context.Context, spec map[string]any, modelName, modelVersion string) {
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
	cacheNames := r.warmCacheNames(ctx, modelName, modelVersion)
	nodes := warmedNodesForModel(nodeList.Items, modelName, cacheNames)
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

// warmCacheNames maps a catalog model (and optional version) to the
// LocalModelCache resource names that were warmed for it.
func (r *k8sReconciler) warmCacheNames(ctx context.Context, modelName, version string) map[string]bool {
	out := map[string]bool{}
	caches, err := r.dynamic.Resource(localModelCacheGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logger.S().Warnw("controller: list local model caches failed", "err", err)
		}
		return out
	}
	for i := range caches.Items {
		cache := &caches.Items[i]
		annotations := cache.GetAnnotations()
		if annotations["taas.io/model-name"] != modelName {
			continue
		}
		if version != "" && annotations["taas.io/model-version"] != "" && annotations["taas.io/model-version"] != version {
			continue
		}
		out[cache.GetName()] = true
	}
	return out
}

// warmedNodesForModel collects the nodes whose LocalModelNode reports a
// downloaded copy for the model. Cache names are authoritative; the direct
// model-name match keeps manually created LocalModelNodes working.
func warmedNodesForModel(nodes []unstructured.Unstructured, modelName string, cacheNames map[string]bool) []string {
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
			if name == modelName || cacheNames[name] {
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
					if value == "ModelDownloaded" && (key == modelName || cacheNames[key]) {
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
