package controller

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/mq"
)

// newClusterCollectorDB seeds a clusters table with the given rows.
func newClusterCollectorDB(t *testing.T, rows []registeredCluster) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cluster.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE clusters (
		id TEXT PRIMARY KEY,
		name TEXT,
		region TEXT,
		kubeconfig_ref TEXT NOT NULL,
		state TEXT NOT NULL,
		local BOOLEAN NOT NULL DEFAULT false,
		node_labels TEXT NOT NULL,
		created_at TIMESTAMP
	)`).Error)
	for _, r := range rows {
		require.NoError(t, db.Exec(
			`INSERT INTO clusters (id, name, region, kubeconfig_ref, state, local, node_labels, created_at)
			 VALUES (?, ?, '', ?, ?, false, '', ?)`,
			r.ID, r.ID, r.KubeconfigRef, r.State, time.Now().UTC(),
		).Error)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func TestClusterCollectorPublishesSnapshot(t *testing.T) {
	db := newClusterCollectorDB(t, []registeredCluster{
		{ID: "cluster-local", KubeconfigRef: localKubeconfigRef, State: "active"},
		{ID: "cluster-disabled", KubeconfigRef: localKubeconfigRef, State: "disabled"},
	})
	client := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1", UID: "uid-1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	})
	bus := mq.NewFake()
	collector := NewClusterCollector(db, client, bus, 10*time.Millisecond, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got []mq.Message
	require.NoError(t, bus.Subscribe(ctx, mq.DefaultSubjects().ClusterHealth, func(msg mq.Message) error {
		got = append(got, msg)
		return nil
	}))

	go collector.Run(ctx)

	require.Eventually(t, func() bool {
		return len(got) > 0
	}, 2*time.Second, 10*time.Millisecond)

	var snap clusterHealthSnapshot
	require.NoError(t, json.Unmarshal(got[0].Body, &snap))
	// Only the active cluster is collected; the disabled one is skipped.
	require.Len(t, snap.Clusters, 1)
	entry := snap.Clusters[0]
	assert.Equal(t, "cluster-local", entry.ClusterID)
	assert.Equal(t, "healthy", entry.Health)
	assert.Equal(t, int64(1), entry.NodeCount)
	require.Len(t, entry.Nodes, 1)
	assert.Equal(t, "node-1", entry.Nodes[0].Name)
	assert.Equal(t, "ready", entry.Nodes[0].Status)
}

func TestClusterCollectorSkipsRemoteWithoutKubeconfigDir(t *testing.T) {
	db := newClusterCollectorDB(t, []registeredCluster{
		{ID: "cluster-remote", KubeconfigRef: "kube/remote", State: "active"},
	})
	client := fake.NewSimpleClientset()
	bus := mq.NewFake()
	collector := NewClusterCollector(db, client, bus, 10*time.Millisecond, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got []mq.Message
	require.NoError(t, bus.Subscribe(ctx, mq.DefaultSubjects().ClusterHealth, func(msg mq.Message) error {
		got = append(got, msg)
		return nil
	}))

	go collector.Run(ctx)

	require.Eventually(t, func() bool {
		return len(got) > 0
	}, 2*time.Second, 10*time.Millisecond)

	var snap clusterHealthSnapshot
	require.NoError(t, json.Unmarshal(got[0].Body, &snap))
	// The remote cluster cannot be reached (no kubeconfig dir), so it is
	// reported with an empty node set rather than dropped.
	require.Len(t, snap.Clusters, 1)
	assert.Equal(t, "cluster-remote", snap.Clusters[0].ClusterID)
	assert.Equal(t, int64(0), snap.Clusters[0].NodeCount)
	assert.Equal(t, "unknown", snap.Clusters[0].Health)
}

func TestNodeStatus(t *testing.T) {
	assert.Equal(t, "ready", nodeStatus(corev1.Node{
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
		}},
	}))
	assert.Equal(t, "not_ready", nodeStatus(corev1.Node{
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
		}},
	}))
	assert.Equal(t, "unknown", nodeStatus(corev1.Node{}))
}