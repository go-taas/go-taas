package model

import "time"

// ModelTransfer is the GORM model of the model_transfers table: one
// asynchronous storage operation (copy, hub download or delete) executed by
// a taas-transfer Job. The row is the idempotency key and the console's
// progress record; the Job owns the bytes.
type ModelTransfer struct { //nolint:revive // the name is part of the model package API
	// ID is the server-generated UUID v4 exposed as transfer_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// Operation is copy, download or delete.
	Operation string `gorm:"size:16;not null;index"`
	// ModelID is the catalog model the transfer belongs to. Empty for a
	// transfer that only moves bytes.
	ModelID string `gorm:"type:uuid;not null;index"`
	// ModelName is the catalog name denormalized for the console listing.
	ModelName string `gorm:"size:128;not null;default:''"`
	// Version is the model version being moved.
	Version string `gorm:"size:64;not null;default:''"`
	// SourceBackend and TargetBackend name the storage backends.
	SourceBackend string `gorm:"size:63;not null;default:''"`
	TargetBackend string `gorm:"size:63;not null;default:''"`
	// SourceURI and TargetURI are the storage URIs the Job reads/writes.
	SourceURI string `gorm:"size:1024;not null;default:''"`
	TargetURI string `gorm:"size:1024;not null;default:''"`
	// WeightPath is the logical relative key the model version keeps.
	WeightPath string `gorm:"size:512;not null;default:''"`
	// State is queued, running, succeeded, failed or cancelled.
	State string `gorm:"size:16;not null;index"`
	// Message carries the failure reason or the Job name while running.
	Message string `gorm:"size:1024;not null;default:''"`
	// Bytes is the number of bytes moved so far.
	Bytes int64 `gorm:"not null;default:0"`
	// TotalBytes is the expected size when known.
	TotalBytes int64 `gorm:"not null;default:0"`
	// JobName is the Kubernetes Job the controller created.
	JobName string `gorm:"size:255;not null;default:''"`
	// DownloadID links a download transfer to the model_downloads row it
	// completes. Nil for copy/delete transfers, which keeps the uuid
	// column nullable instead of writing an invalid empty string.
	DownloadID *string `gorm:"type:uuid;index"`
	// CreatedAt is the enqueue time (UTC).
	CreatedAt time.Time `gorm:"index:idx_model_transfers_created,priority:1,sort:DESC"`
	// UpdatedAt is the last state/progress update (UTC).
	UpdatedAt time.Time
}

// TableName returns the table name of ModelTransfer.
func (ModelTransfer) TableName() string { return "model_transfers" }

// ModelCacheEntry is the GORM model of the model_cache_entries table: the
// desired and observed state of one model's node-local cache. The controller
// owns the LocalModelCache/LocalModelNodeGroup resources; the row mirrors
// their state so the console and the server can query it without a
// Kubernetes client.
type ModelCacheEntry struct { //nolint:revive // the name is part of the model package API
	// ID is the server-generated UUID v4 exposed as cache_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// ModelID is the catalog model being warmed.
	ModelID string `gorm:"type:uuid;not null;index"`
	// ModelName and Version are denormalized for listing.
	ModelName string `gorm:"size:128;not null;default:''"`
	Version   string `gorm:"size:64;not null;default:''"`
	// Backend is the storage backend the weights are read from.
	Backend string `gorm:"size:63;not null;default:''"`
	// SourceURI is the LocalModelCache sourceModelUri.
	SourceURI string `gorm:"size:1024;not null;default:''"`
	// WeightPath is the logical relative weight key.
	WeightPath string `gorm:"size:512;not null;default:''"`
	// CacheName is the LocalModelCache resource name.
	CacheName string `gorm:"size:255;not null;default:''"`
	// NodeGroupsJSON is the JSON array of node group names.
	NodeGroupsJSON string `gorm:"type:text;not null;default:''"`
	// State is queued, warming, ready, failed or cancelled.
	State string `gorm:"size:16;not null;index"`
	// Message carries the failure reason.
	Message string `gorm:"size:1024;not null;default:''"`
	// WarmedNodesJSON is the JSON array of nodes whose copy is available.
	WarmedNodesJSON string `gorm:"type:text;not null;default:''"`
	// TotalNodes is the number of nodes currently reporting a copy.
	TotalNodes int `gorm:"not null;default:0"`
	// SizeBytes is the model size when known.
	SizeBytes int64 `gorm:"not null;default:0"`
	// CreatedAt is the request time (UTC).
	CreatedAt time.Time `gorm:"index:idx_model_cache_entries_created,priority:1,sort:DESC"`
	// UpdatedAt is the last observed-state update (UTC).
	UpdatedAt time.Time
}

// TableName returns the table name of ModelCacheEntry.
func (ModelCacheEntry) TableName() string { return "model_cache_entries" }
