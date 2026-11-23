// Package transfer defines the payloads exchanged between the control plane
// and the taas-transfer Kubernetes Job, and between the controller and the
// server.
package transfer

import (
	"time"

	"github.com/go-taas/go-taas/pkg/storage"
)

// Transfer operations.
const (
	OperationCopy     = "copy"
	OperationDownload = "download"
	OperationDelete   = "delete"
	// OperationMove copies a version to another backend and deletes the
	// source only after the copy succeeds.
	OperationMove = "move"
	// OperationUpload turns a staged upload archive in the upload object
	// store into a registered model: the Job downloads the single composed
	// archive, extracts it into the target backend, and the server registers
	// the version when the Job reports success.
	OperationUpload = "upload"
	OperationCancel = "cancel"
)

// Transfer states.
const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// Terminal reports whether a state can no longer change.
func Terminal(state string) bool {
	switch state {
	case StateSucceeded, StateFailed, StateCancelled:
		return true
	}
	return false
}

// ObjectStoreRef is the resolved object-store access description a Job uses.
type ObjectStoreRef = storage.RemoteRef

// Spec is the idempotent description of one transfer Job.
type Spec struct {
	// ID is the model_transfers primary key (also the idempotency key).
	ID string `json:"id"`
	// Operation is copy, download, delete or move.
	Operation string `json:"operation"`
	// ModelID, ModelName and Version identify the catalog row for logging;
	// they are not needed to move bytes.
	ModelID   string `json:"modelId,omitempty"`
	ModelName string `json:"modelName,omitempty"`
	Version   string `json:"version,omitempty"`
	// SourceBackend and TargetBackend name the storage backends.
	SourceBackend string `json:"sourceBackend,omitempty"`
	TargetBackend string `json:"targetBackend,omitempty"`
	// SourceURI and TargetURI are the storage URIs. A pvc:// URI is
	// resolved by the Job against the mounted volume; an s3:// URI is
	// fetched with the ObjectStoreRef below.
	SourceURI string `json:"sourceUri,omitempty"`
	TargetURI string `json:"targetUri,omitempty"`
	// SourcePath and TargetPath are the in-Job filesystem paths for a
	// filesystem transfer.
	SourcePath string `json:"sourcePath,omitempty"`
	TargetPath string `json:"targetPath,omitempty"`
	// SourceObjectStore and TargetObjectStore configure object-store access.
	SourceObjectStore *ObjectStoreRef `json:"sourceObjectStore,omitempty"`
	TargetObjectStore *ObjectStoreRef `json:"targetObjectStore,omitempty"`
	// HubSource and HubModelID drive a download operation.
	HubSource  string `json:"hubSource,omitempty"`
	HubModelID string `json:"hubModelId,omitempty"`
	// DeleteSource asks the mover to remove the source after a successful
	// copy. A move operation always deletes the source.
	DeleteSource bool `json:"deleteSource,omitempty"`
	// WeightPath is the logical relative key the model version keeps.
	WeightPath string `json:"weightPath,omitempty"`

	// Upload carries the archive-specific inputs of an upload operation.
	// The source object store holds the composed archive under UploadKey;
	// the Job downloads it, unpacks it with UploadFormat, and writes the
	// extracted tree into the target (TargetPath for filesystems, the
	// TargetObjectStore otherwise).
	Upload *UploadSpec `json:"upload,omitempty"`
}

// UploadSpec is the archive description of an upload transfer.
type UploadSpec struct {
	// ObjectKey is the staging object-store key of the composed archive.
	ObjectKey string `json:"objectKey,omitempty"`
	// OriginalName is the uploaded file's name, used to pick the format.
	OriginalName string `json:"originalName,omitempty"`
	// Format is the pre-resolved unpack format ("tar", "tar.gz" or "zip").
	Format string `json:"format,omitempty"`
	// StagingPrefix is the object-store prefix that holds the archive.
	StagingPrefix string `json:"stagingPrefix,omitempty"`
}

// Progress is one progress snapshot a Job reports through its progress
// ConfigMap. Bytes is monotonic while the Job runs.
type Progress struct {
	State      string    `json:"state"`
	Message    string    `json:"message,omitempty"`
	Bytes      int64     `json:"bytes,omitempty"`
	TotalBytes int64     `json:"totalBytes,omitempty"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Status is the controller's report to the server. It is the only way a
// transfer row changes state once a Job owns it.
type Status struct {
	ID         string    `json:"id"`
	State      string    `json:"state"`
	Message    string    `json:"message,omitempty"`
	Bytes      int64     `json:"bytes,omitempty"`
	TotalBytes int64     `json:"totalBytes,omitempty"`
	JobName    string    `json:"jobName,omitempty"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// ResolvePath joins a filesystem backend root with the relative key. It is
// the only place a Job turns a storage URI into a path.
func ResolvePath(root, key string) (string, error) {
	return storage.CleanJoin(root, key)
}
