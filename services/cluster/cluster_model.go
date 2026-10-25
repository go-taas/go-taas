// Package cluster implements the cluster registry and the cluster-health
// projection (feature #40). It serves the admin Cluster pages,
// registering/managing inference clusters, viewing cluster health and
// workload placement, and routing deployments across clusters.
package cluster

import (
	"strings"
	"time"
)

// Cluster states (AD3).
const (
	ClusterStateActive   = "active"
	ClusterStateDisabled = "disabled"
)

// Cluster is the GORM model for the clusters table (AD3).
type Cluster struct { //nolint:revive // cluster.Cluster is the documented domain name
	ID            string `gorm:"primaryKey"`
	Name          string `gorm:"size:128;not null;uniqueIndex"`
	Region        string `gorm:"size:64;not null;default:''"`
	KubeconfigRef string `gorm:"size:512;not null"`
	State         string `gorm:"size:16;not null;index"`
	// Local marks the cluster the platform itself runs in (discovered
	// from the in-cluster Kubernetes environment). At most one row is
	// local; the console shows it as "this cluster" instead of a remote
	// registration.
	Local bool `gorm:"not null;default:false;index"`
	// NodeLabels caches the accelerator label keys present on the
	// cluster's nodes (comma-separated), so the console can offer valid
	// image-warmup selectors without a live API call.
	NodeLabels string `gorm:"type:text;not null"`
	CreatedAt  time.Time
}

// TableName overrides the default GORM table name.
func (Cluster) TableName() string { return "clusters" }

// validKubeconfigRef validates a kubeconfig reference (AD3): non-empty,
// <= 512 chars, no ".." segments, no leading "/", no backslashes.
func validKubeconfigRef(ref string) bool {
	if ref == "" || len(ref) > 512 {
		return false
	}
	if strings.HasPrefix(ref, "/") || strings.Contains(ref, "\\") {
		return false
	}
	for _, seg := range strings.Split(ref, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}
