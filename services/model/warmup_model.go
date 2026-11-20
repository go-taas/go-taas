package model

import "time"

// WarmupNodeGroup is the GORM model of the warmup_node_groups table: one
// platform-managed pool of nodes whose local disk caches model weights. The
// controller materializes it as a KServe LocalModelNodeGroup; the row is the
// source of truth an operator edits in the Operations console, not a Helm
// value.
type WarmupNodeGroup struct { //nolint:revive // the name is part of the model package API
	// Name is the Kubernetes resource name and is immutable.
	Name string `gorm:"primaryKey;size:63"`
	// NodeSelectorJSON is the JSON object of node labels that select the
	// pool's nodes.
	NodeSelectorJSON string `gorm:"type:text;not null;default:''"`
	// StorageLimit is the per-node cache capacity, a Kubernetes quantity.
	StorageLimit string `gorm:"size:32;not null;default:'100Gi'"`
	// HostPath is the node-local directory the cache lives in.
	HostPath string `gorm:"size:512;not null;default:'/models'"`
	// StorageClass is the class of the hostPath PV; empty avoids the
	// cluster default class.
	StorageClass string `gorm:"size:63;not null;default:''"`
	// CreatedAt is the creation time (UTC).
	CreatedAt time.Time `gorm:"index:idx_warmup_node_groups_created,priority:1,sort:DESC"`
	// UpdatedAt is the last edit time (UTC).
	UpdatedAt time.Time
}

// TableName returns the table name of WarmupNodeGroup.
func (WarmupNodeGroup) TableName() string { return "warmup_node_groups" }

// WarmupNode is the GORM model of the warmup_nodes table: the last
// observation of one KServe LocalModelNode. The controller publishes the
// snapshot; the server stores it so the Nodes tab needs no Kubernetes client.
type WarmupNode struct { //nolint:revive // the name is part of the model package API
	// Name is the Kubernetes node name.
	Name string `gorm:"primaryKey;size:253"`
	// NodeGroupsJSON is the JSON array of node groups the node belongs to.
	NodeGroupsJSON string `gorm:"type:text;not null;default:''"`
	// ModelsJSON is the JSON array of model copies on the node.
	ModelsJSON string `gorm:"type:text;not null;default:''"`
	// ModelCount is the number of cache entries on the node.
	ModelCount int `gorm:"not null;default:0"`
	// DownloadedCount is the number of entries already ModelDownloaded.
	DownloadedCount int `gorm:"not null;default:0"`
	// ObservedAt is when the controller last saw the node.
	ObservedAt time.Time `gorm:"index"`
	// UpdatedAt is the row write time (UTC).
	UpdatedAt time.Time
}

// TableName returns the table name of WarmupNode.
func (WarmupNode) TableName() string { return "warmup_nodes" }
