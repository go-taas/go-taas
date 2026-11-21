package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/storage"
	"github.com/go-taas/go-taas/pkg/warmup"
)

func testWarmupReconciler(objects ...runtime.Object) (*ModelWarmupReconciler, *dynamicfake.FakeDynamicClient) {
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		nodeGVR:                "NodeList",
		localModelNodeGroupGVR: "LocalModelNodeGroupList",
		localModelCacheGVR:     "LocalModelCacheList",
		localModelNodeGVR:      "LocalModelNodeList",
		jobGVR:                 "JobList",
	}, objects...)
	return NewModelWarmupReconciler(dyn, mq.NewFake(), config.WarmupConfig{
		Enabled: true,
	}, storage.Config{}, "taas-infer", WarmupSeedConfig{}), dyn
}

func TestApplyModelWarmupCreatesGroupAndCache(t *testing.T) {
	reconciler, dyn := testWarmupReconciler()
	body, err := json.Marshal(&warmup.Request{
		ID:         "cache-1",
		ModelID:    "model-1",
		ModelName:  "qwen",
		Version:    "v1",
		SourceURI:  "pvc://model-weights/qwen/v1",
		SizeBytes:  1024,
		NodeGroups: []warmup.NodeGroup{{Name: "worker", StorageLimit: "200Gi", HostPath: "/var/lib/taas/models", NodeSelector: map[string]string{"kserve/localmodel": "worker"}}},
	})
	require.NoError(t, err)
	require.NoError(t, reconciler.ApplyModelWarmup(context.Background(), mq.Message{Body: body}))

	group, err := dyn.Resource(localModelNodeGroupGVR).Get(context.Background(), "worker", metav1.GetOptions{})
	require.NoError(t, err)
	spec := group.Object["spec"].(map[string]any)
	assert.Equal(t, "200Gi", spec["storageLimit"])
	volumeSpec := spec["persistentVolumeSpec"].(map[string]any)
	affinity := volumeSpec["nodeAffinity"].(map[string]any)
	required := affinity["required"].(map[string]any)
	terms := required["nodeSelectorTerms"].([]any)
	require.Len(t, terms, 1)
	expressions := terms[0].(map[string]any)["matchExpressions"].([]any)
	require.Len(t, expressions, 1)
	assert.Equal(t, "kserve/localmodel", expressions[0].(map[string]any)["key"])
	assert.Equal(t, "worker", expressions[0].(map[string]any)["values"].([]any)[0])

	cache, err := dyn.Resource(localModelCacheGVR).Get(context.Background(), "taas-model-cache-1", metav1.GetOptions{})
	require.NoError(t, err)
	cacheSpec := cache.Object["spec"].(map[string]any)
	assert.Equal(t, "pvc://model-weights/qwen/v1", cacheSpec["sourceModelUri"])
	assert.Equal(t, []any{"worker"}, cacheSpec["nodeGroups"])
	assert.Equal(t, "qwen", cache.GetAnnotations()["go-taas.com/model-name"])
	assert.Equal(t, "v1", cache.GetAnnotations()["go-taas.com/model-version"])

	// Applying again updates instead of failing on AlreadyExists.
	require.NoError(t, reconciler.ApplyModelWarmup(context.Background(), mq.Message{Body: body}))
}

func TestApplyModelWarmupCancelDeletesCache(t *testing.T) {
	cache := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelCache",
		"metadata": map[string]any{
			"name":   "taas-model-cache-2",
			"labels": map[string]any{"go-taas.com/model-cache-id": "cache-2"},
		},
	}}
	reconciler, dyn := testWarmupReconciler(cache)
	body, _ := json.Marshal(&warmup.Request{ID: "cache-2"})
	require.NoError(t, reconciler.ApplyModelWarmup(context.Background(), mq.Message{Body: body}))
	_, err := dyn.Resource(localModelCacheGVR).Get(context.Background(), "taas-model-cache-2", metav1.GetOptions{})
	assert.Error(t, err)
}

func TestReportCachesPublishesReadyState(t *testing.T) {
	cache := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelCache",
		"metadata": map[string]any{
			"name":   "taas-model-cache-3",
			"labels": map[string]any{"go-taas.com/model-cache-id": "cache-3"},
		},
		"spec": map[string]any{
			"nodeGroups": []any{"worker"},
			"modelSize":  "2Gi",
		},
		"status": map[string]any{
			"copies": map[string]any{"available": int64(2)},
			"nodeStatus": map[string]any{
				"node-a": "NodeDownloaded",
				"node-b": "NodeDownloaded",
			},
		},
	}}
	reconciler, _ := testWarmupReconciler(cache)
	clientMQ := mq.NewFake()
	reconciler.client = clientMQ
	var got []warmup.Status
	require.NoError(t, clientMQ.Subscribe(context.Background(), mq.DefaultSubjects().ModelWarmupStatus, func(msg mq.Message) error {
		var status warmup.Status
		if err := json.Unmarshal(msg.Body, &status); err != nil {
			return err
		}
		got = append(got, status)
		return nil
	}))
	require.NoError(t, reconciler.reportCaches(context.Background()))
	require.Len(t, got, 1)
	assert.Equal(t, warmup.StateReady, got[0].State)
	assert.ElementsMatch(t, []string{"node-a", "node-b"}, got[0].WarmedNodes)
}

func TestApplyWarmupAffinity(t *testing.T) {
	cache := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelCache",
		"metadata": map[string]any{
			"name":        "taas-model-cache-affinity",
			"annotations": map[string]any{"go-taas.com/model-name": "qwen", "go-taas.com/model-version": "v1"},
		},
	}}
	node := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelNode",
		"metadata":   map[string]any{"name": "node-a"},
		"spec": map[string]any{
			"localModels": []any{map[string]any{"modelName": "taas-model-cache-affinity"}},
		},
		"status": map[string]any{
			"modelStatus": map[string]any{"taas-model-cache-affinity": "ModelDownloaded"},
		},
	}}
	_, dyn := testWarmupReconciler(cache, node)
	reconciler := &k8sReconciler{dynamic: dyn, warmupEnabled: true, warmupMode: "preferred", warmupWeight: 80}
	spec := map[string]any{}
	reconciler.applyWarmupAffinity(context.Background(), spec, "qwen", "v1")
	affinity, ok := spec["affinity"].(map[string]any)
	require.True(t, ok)
	nodeAffinity := affinity["nodeAffinity"].(map[string]any)
	preferred := nodeAffinity["preferredDuringSchedulingIgnoredDuringExecution"].([]any)
	assert.Equal(t, int64(80), preferred[0].(map[string]any)["weight"])

	// A different version is not warmed by this cache.
	otherVersion := map[string]any{}
	reconciler.applyWarmupAffinity(context.Background(), otherVersion, "qwen", "v2")
	assert.Empty(t, otherVersion)

	// An unwarmed model leaves the pod spec untouched.
	untouched := map[string]any{}
	reconciler.applyWarmupAffinity(context.Background(), untouched, "missing", "")
	assert.Empty(t, untouched)
}

func TestSeedCachesCreatesPVCSeedJobAndMarksNodeDownloaded(t *testing.T) {
	sourceURI := "pvc://taas-model-weights/qwen/v1"
	cache := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelCache",
		"metadata": map[string]any{
			"name":   "taas-model-cache-seed",
			"labels": map[string]any{"go-taas.com/model-cache-id": "cache-seed"},
		},
		"spec": map[string]any{
			"sourceModelUri": sourceURI,
			"nodeGroups":     []any{"worker"},
		},
	}}
	node := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelNode",
		"metadata":   map[string]any{"name": "sd4090"},
		"spec": map[string]any{
			"localModels": []any{map[string]any{
				"modelName":      "taas-model-cache-seed",
				"nodeGroup":      "worker",
				"sourceModelUri": sourceURI,
			}},
		},
	}}
	reconciler, dyn := testWarmupReconciler(cache, node)
	reconciler.seed = WarmupSeedConfig{
		Image:          "registry/taas-transfer:test",
		Namespace:      "taas",
		ServiceAccount: "taas-transfer",
		NodeSelector:   map[string]string{"kubernetes.io/os": "linux"},
	}
	require.NoError(t, reconciler.seedCaches(context.Background()))

	job, err := dyn.Resource(jobGVR).Namespace("taas").Get(context.Background(), seedJobName("taas-model-cache-seed", "sd4090"), metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, seedJobName("taas-model-cache-seed", "sd4090"), job.GetName())
	jobSpec := job.Object["spec"].(map[string]any)
	template := jobSpec["template"].(map[string]any)["spec"].(map[string]any)
	assert.Equal(t, "sd4090", template["nodeName"])
	assert.Equal(t, "taas-transfer", template["serviceAccountName"])
	container := template["containers"].([]any)[0].(map[string]any)
	env := container["env"].([]any)[0].(map[string]any)
	assert.Equal(t, "TRANSFER_SPEC_JSON", env["name"])
	var transferSpec map[string]any
	require.NoError(t, json.Unmarshal([]byte(env["value"].(string)), &transferSpec))
	assert.Equal(t, "/source/qwen/v1", transferSpec["sourcePath"])
	assert.Equal(t, "/target/models/"+storageKeyOf(sourceURI), transferSpec["targetPath"])

	// A successful Job flips the node entry to ModelDownloaded.
	job.Object["status"] = map[string]any{"succeeded": int64(1)}
	_, err = dyn.Resource(jobGVR).Namespace("taas").Update(context.Background(), job, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.NoError(t, reconciler.seedCaches(context.Background()))
	updated, err := dyn.Resource(localModelNodeGVR).Get(context.Background(), "sd4090", metav1.GetOptions{})
	require.NoError(t, err)
	status := updated.Object["status"].(map[string]any)
	modelStatus := status["modelStatus"].(map[string]any)
	assert.Equal(t, "ModelDownloaded", modelStatus["taas-model-cache-seed"])
}

func TestSeedCachesFetchesObjectStoreSource(t *testing.T) {
	sourceURI := "s3://models/weights/qwen/v1"
	cache := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelCache",
		"metadata": map[string]any{
			"name":   "taas-model-cache-s3",
			"labels": map[string]any{"go-taas.com/model-cache-id": "cache-s3"},
			"annotations": map[string]any{
				"go-taas.com/backend": "archive",
			},
		},
		"spec": map[string]any{
			"sourceModelUri": sourceURI,
			"nodeGroups":     []any{"worker"},
		},
	}}
	node := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelNode",
		"metadata":   map[string]any{"name": "sd4090"},
		"spec": map[string]any{
			"localModels": []any{map[string]any{
				"modelName": "taas-model-cache-s3",
				"nodeGroup": "worker",
			}},
		},
	}}
	reconciler, dyn := testWarmupReconciler(cache, node)
	reconciler.storage = storage.Config{
		DefaultBackend: "archive",
		Backends: map[string]storage.Backend{
			"archive": {
				Type: storage.TypeObjectStore,
				ObjectStore: storage.ObjectStore{
					Endpoint:       "http://minio:9000",
					Region:         "us-east-1",
					Bucket:         "models",
					Prefix:         "weights",
					ForcePathStyle: true,
					Credentials: storage.Credentials{
						ExistingSecret: "minio-keys",
						AccessKeyKey:   "accessKey",
						SecretKeyKey:   "secretKey",
					},
				},
				Delivery: storage.Delivery{Default: storage.ModeCache, Allowed: []string{storage.ModeCache}},
			},
		},
	}
	reconciler.seed = WarmupSeedConfig{
		Image:     "registry/taas-transfer:test",
		Namespace: "taas",
	}
	require.NoError(t, reconciler.seedCaches(context.Background()))

	job, err := dyn.Resource(jobGVR).Namespace("taas").Get(context.Background(), seedJobName("taas-model-cache-s3", "sd4090"), metav1.GetOptions{})
	require.NoError(t, err)
	template := job.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	container := template["containers"].([]any)[0].(map[string]any)

	var transferSpec map[string]any
	for _, entry := range container["env"].([]any) {
		env := entry.(map[string]any)
		if env["name"] == "TRANSFER_SPEC_JSON" {
			require.NoError(t, json.Unmarshal([]byte(env["value"].(string)), &transferSpec))
		}
	}
	require.NotNil(t, transferSpec)
	assert.Equal(t, "qwen/v1", transferSpec["weightPath"])
	assert.Empty(t, transferSpec["sourcePath"])
	source := transferSpec["sourceObjectStore"].(map[string]any)
	assert.Equal(t, "models", source["bucket"])
	assert.Equal(t, "weights", source["prefix"])
	assert.Equal(t, "http://minio:9000", source["endpoint"])
	assert.Equal(t, "/target/models/"+storageKeyOf(sourceURI), transferSpec["targetPath"])

	volumes := template["volumes"].([]any)
	require.Len(t, volumes, 1, "an object-store seed needs only the target hostPath")
	assert.Equal(t, "target", volumes[0].(map[string]any)["name"])
	assert.Len(t, template["containers"].([]any)[0].(map[string]any)["volumeMounts"].([]any), 1)

	// The Secret is projected by name; the controller never reads its values.
	envEntries := container["env"].([]any)
	require.Len(t, envEntries, 4)
	access := envEntries[2].(map[string]any)
	assert.Equal(t, "WARMUP_SOURCE_ACCESS_KEY", access["name"])
	ref := access["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)
	assert.Equal(t, "minio-keys", ref["name"])
	assert.Equal(t, "accessKey", ref["key"])
}

func TestParsePVCURIAndStorageKey(t *testing.T) {
	claim, path, ok := parsePVCURI("pvc://weights/qwen/v1/")
	assert.True(t, ok)
	assert.Equal(t, "weights", claim)
	assert.Equal(t, "qwen/v1", path)

	_, _, ok = parsePVCURI("s3://bucket/key")
	assert.False(t, ok)
	assert.Equal(t, "471591f9c6a5faf7", storageKeyOf("pvc://taas-model-weights/storage-warmup-test/upload-20261119-170954"))
	assert.LessOrEqual(t, len(seedJobName("taas-model-1e8ff38d-50af-4603-807a-3f08d37ac215", "sd4090")), 63)
}

func TestApplyNodeGroupCreatesAndDeletes(t *testing.T) {
	reconciler, dyn := testWarmupReconciler()
	group := warmup.NodeGroup{
		Name: "gpu", StorageLimit: "500Gi", HostPath: "/models", StorageClass: "local-path",
		NodeSelector: map[string]string{"gpu": "true"},
	}
	body, err := json.Marshal(&warmup.NodeGroupEvent{NodeGroup: group})
	require.NoError(t, err)
	require.NoError(t, reconciler.ApplyNodeGroup(context.Background(), mq.Message{Body: body}))
	created, err := dyn.Resource(localModelNodeGroupGVR).Get(context.Background(), "gpu", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "500Gi", created.Object["spec"].(map[string]any)["storageLimit"])
	assert.Equal(t, "/models", nestedString(created, "spec", "persistentVolumeSpec", "hostPath", "path"))

	deleted, err := json.Marshal(&warmup.NodeGroupEvent{NodeGroup: warmup.NodeGroup{Name: "gpu"}, Deleted: true})
	require.NoError(t, err)
	require.NoError(t, reconciler.ApplyNodeGroup(context.Background(), mq.Message{Body: deleted}))
	_, err = dyn.Resource(localModelNodeGroupGVR).Get(context.Background(), "gpu", metav1.GetOptions{})
	assert.Error(t, err)
}

func TestApplyNodeGroupSnapshotPrunesDeletedGroup(t *testing.T) {
	stale := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1", "kind": "LocalModelNodeGroup",
		"metadata": map[string]any{
			"name":   "stale",
			"labels": map[string]any{"app.kubernetes.io/managed-by": "taas"},
		},
	}}
	reconciler, dyn := testWarmupReconciler(stale)
	body, err := json.Marshal(&warmup.NodeGroupEvent{
		Snapshot: true,
		NodeGroups: []warmup.NodeGroup{
			{Name: "gpu", StorageLimit: "50Gi", HostPath: "/models", NodeSelector: map[string]string{"gpu": "true"}},
			{Name: "cpu", StorageLimit: "10Gi", HostPath: "/models"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, reconciler.ApplyNodeGroup(context.Background(), mq.Message{Body: body}))

	for _, name := range []string{"gpu", "cpu"} {
		_, err := dyn.Resource(localModelNodeGroupGVR).Get(context.Background(), name, metav1.GetOptions{})
		require.NoError(t, err)
	}
	_, err = dyn.Resource(localModelNodeGroupGVR).Get(context.Background(), "stale", metav1.GetOptions{})
	assert.Error(t, err)
}

func TestReportNodesPublishesLocalModelNodeSnapshot(t *testing.T) {
	nodeA := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1", "kind": "LocalModelNode",
		"metadata": map[string]any{"name": "node-a"},
		"spec": map[string]any{"localModels": []any{
			map[string]any{"modelName": "cache-1", "nodeGroup": "worker"},
			map[string]any{"modelName": "cache-2", "nodeGroup": "gpu"},
		}},
		"status": map[string]any{"modelStatus": map[string]any{
			"cache-1": "ModelDownloaded", "cache-2": "ModelDownloading",
		}},
	}}
	nodeB := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1", "kind": "LocalModelNode",
		"metadata": map[string]any{"name": "node-b"},
	}}
	reconciler, _ := testWarmupReconciler(nodeA, nodeB)
	clientMQ := mq.NewFake()
	reconciler.client = clientMQ
	var got warmup.NodeSnapshot
	require.NoError(t, clientMQ.Subscribe(context.Background(), mq.DefaultSubjects().ModelWarmupNodes, func(msg mq.Message) error {
		return json.Unmarshal(msg.Body, &got)
	}))
	require.NoError(t, reconciler.reportNodes(context.Background()))
	require.Len(t, got.Nodes, 2)
	byName := map[string]warmup.Node{}
	for _, node := range got.Nodes {
		byName[node.Name] = node
	}
	assert.Equal(t, 2, byName["node-a"].ModelCount)
	assert.Equal(t, 1, byName["node-a"].DownloadedCount)
	assert.Equal(t, []string{"gpu", "worker"}, byName["node-a"].NodeGroups)
	assert.Equal(t, "ModelDownloaded", byName["node-a"].Models[0].State)
	assert.Equal(t, 0, byName["node-b"].ModelCount)
}

func warmupNode(name string, labels map[string]string) *unstructured.Unstructured {
	metadata := map[string]any{"name": name}
	if labels != nil {
		values := make(map[string]any, len(labels))
		for key, value := range labels {
			values[key] = value
		}
		metadata["labels"] = values
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Node", "metadata": metadata,
	}}
}

func TestApplyNodeGroupLabelsMatchingNodes(t *testing.T) {
	matching := warmupNode("node-a", map[string]string{"gpu": "true"})
	other := warmupNode("node-b", map[string]string{"gpu": "false"})
	reconciler, dyn := testWarmupReconciler(matching, other)
	body, err := json.Marshal(&warmup.NodeGroupEvent{NodeGroup: warmup.NodeGroup{
		Name: "gpu", NodeSelector: map[string]string{"gpu": "true"},
	}})
	require.NoError(t, err)
	require.NoError(t, reconciler.ApplyNodeGroup(context.Background(), mq.Message{Body: body}))

	labelled, err := dyn.Resource(nodeGVR).Get(context.Background(), "node-a", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "worker", labelled.GetLabels()["kserve/localmodel"])
	untouched, err := dyn.Resource(nodeGVR).Get(context.Background(), "node-b", metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, untouched.GetLabels(), "kserve/localmodel")
}

func TestDeleteNodeGroupRemovesKServeLabel(t *testing.T) {
	matching := warmupNode("node-a", map[string]string{
		"gpu": "true", "kserve/localmodel": "worker",
	})
	group := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1", "kind": "LocalModelNodeGroup",
		"metadata": map[string]any{
			"name":   "gpu",
			"labels": map[string]any{"app.kubernetes.io/managed-by": "taas"},
		},
		"spec": map[string]any{
			"persistentVolumeSpec": map[string]any{
				"nodeAffinity": map[string]any{
					"required": map[string]any{
						"nodeSelectorTerms": []any{map[string]any{
							"matchExpressions": []any{map[string]any{
								"key": "gpu", "operator": "In", "values": []any{"true"},
							}},
						}},
					},
				},
			},
		},
	}}
	reconciler, dyn := testWarmupReconciler(matching, group)
	body, err := json.Marshal(&warmup.NodeGroupEvent{
		NodeGroup: warmup.NodeGroup{Name: "gpu"}, Deleted: true,
	})
	require.NoError(t, err)
	require.NoError(t, reconciler.ApplyNodeGroup(context.Background(), mq.Message{Body: body}))

	updated, err := dyn.Resource(nodeGVR).Get(context.Background(), "node-a", metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, updated.GetLabels(), "kserve/localmodel")
}

func TestDeleteNodeGroupKeepsLabelNeededByAnotherGroup(t *testing.T) {
	node := warmupNode("node-a", map[string]string{
		"gpu": "true", "kserve/localmodel": "worker",
	})
	group := func(name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "serving.kserve.io/v1alpha1", "kind": "LocalModelNodeGroup",
			"metadata": map[string]any{
				"name":   name,
				"labels": map[string]any{"app.kubernetes.io/managed-by": "taas"},
			},
			"spec": map[string]any{
				"persistentVolumeSpec": map[string]any{
					"nodeAffinity": nodeSelectorAffinity(map[string]string{"gpu": "true"}),
				},
			},
		}}
	}
	reconciler, dyn := testWarmupReconciler(node, group("gpu"), group("shared"))
	body, err := json.Marshal(&warmup.NodeGroupEvent{
		NodeGroup: warmup.NodeGroup{Name: "gpu"}, Deleted: true,
	})
	require.NoError(t, err)
	require.NoError(t, reconciler.ApplyNodeGroup(context.Background(), mq.Message{Body: body}))

	updated, err := dyn.Resource(nodeGVR).Get(context.Background(), "node-a", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "worker", updated.GetLabels()["kserve/localmodel"])
}

func TestApplyNodeGroupKeepsExistingKServeLabel(t *testing.T) {
	node := warmupNode("node-a", map[string]string{
		"gpu": "true", "kserve/localmodel": "worker",
	})
	reconciler, dyn := testWarmupReconciler(node)
	group := warmup.NodeGroup{Name: "gpu", NodeSelector: map[string]string{"gpu": "true"}}
	for i := 0; i < 2; i++ {
		body, err := json.Marshal(&warmup.NodeGroupEvent{NodeGroup: group})
		require.NoError(t, err)
		require.NoError(t, reconciler.ApplyNodeGroup(context.Background(), mq.Message{Body: body}))
	}
	updated, err := dyn.Resource(nodeGVR).Get(context.Background(), "node-a", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "worker", updated.GetLabels()["kserve/localmodel"])
}

func TestDeleteMissingNodeGroupUnlabelsKnownSelector(t *testing.T) {
	node := warmupNode("node-a", map[string]string{
		"gpu": "true", "kserve/localmodel": "worker",
	})
	reconciler, dyn := testWarmupReconciler(node)
	body, err := json.Marshal(&warmup.NodeGroupEvent{
		NodeGroup: warmup.NodeGroup{Name: "gone", NodeSelector: map[string]string{"gpu": "true"}},
		Deleted:   true,
	})
	require.NoError(t, err)
	require.NoError(t, reconciler.ApplyNodeGroup(context.Background(), mq.Message{Body: body}))

	updated, err := dyn.Resource(nodeGVR).Get(context.Background(), "node-a", metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, updated.GetLabels(), "kserve/localmodel")
}

func TestDeleteNodeGroupReturnsLookupError(t *testing.T) {
	reconciler, dyn := testWarmupReconciler()
	dyn.PrependReactor("get", "localmodelnodegroups", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("api unavailable")
	})
	body, err := json.Marshal(&warmup.NodeGroupEvent{
		NodeGroup: warmup.NodeGroup{Name: "gpu"}, Deleted: true,
	})
	require.NoError(t, err)
	assert.Error(t, reconciler.ApplyNodeGroup(context.Background(), mq.Message{Body: body}))
}
