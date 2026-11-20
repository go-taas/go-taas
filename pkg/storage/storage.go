// Package storage models the platform's model-weights storage backends.
//
// A backend answers "where do the bytes live" (filesystem or object store);
// its delivery settings answer "how does a pod read them" (mount, download or
// node cache). Keeping the two independent means one serving path serves
// JuiceFS, NFS, CephFS and S3 alike, and an environment only has to declare
// the storage it already runs in Helm.
package storage

import (
	"fmt"
	"path"
	"strings"
)

// Backend types.
const (
	TypeFilesystem  = "filesystem"
	TypeObjectStore = "objectstore"
)

// Delivery modes.
const (
	ModeMount    = "mount"
	ModeDownload = "download"
	ModeCache    = "cache"
)

// Config is the storage section of the configuration: one default backend
// plus the named backends an operator declares.
type Config struct {
	// DefaultBackend names the backend a model version uses when it names
	// none.
	DefaultBackend string `mapstructure:"defaultBackend" yaml:"defaultBackend"`
	// Backends maps a backend name to its definition.
	Backends map[string]Backend `mapstructure:"backends" yaml:"backends"`
}

// Backend is one declared storage backend.
type Backend struct {
	// Name is filled from the map key during Normalize.
	Name string `mapstructure:"-" yaml:"-"`
	// Type is filesystem or objectstore.
	Type string `mapstructure:"type" yaml:"type"`
	// Filesystem configures a filesystem backend.
	Filesystem Filesystem `mapstructure:"filesystem" yaml:"filesystem"`
	// ObjectStore configures an object-store backend.
	ObjectStore ObjectStore `mapstructure:"objectStore" yaml:"objectStore"`
	// Delivery configures how inference pods read the backend.
	Delivery Delivery `mapstructure:"delivery" yaml:"delivery"`
}

// Filesystem describes a POSIX storage backend (JuiceFS, NFS, CephFS, EFS,
// Lustre, an existing PVC).
type Filesystem struct {
	// ExistingClaim is a PVC that already provides the filesystem. When it
	// is empty and Provision.Enabled is true the chart creates one.
	ExistingClaim string `mapstructure:"existingClaim" yaml:"existingClaim"`
	// Provision creates a PVC for the filesystem.
	Provision Provision `mapstructure:"provision" yaml:"provision"`
	// MountPath is where the backend is mounted inside transfer jobs and
	// inference pods. Defaults to /data/weights.
	MountPath string `mapstructure:"mountPath" yaml:"mountPath"`
}

// Provision requests a chart-created PVC.
type Provision struct {
	Enabled      bool   `mapstructure:"enabled" yaml:"enabled"`
	StorageClass string `mapstructure:"storageClass" yaml:"storageClass"`
	AccessMode   string `mapstructure:"accessMode" yaml:"accessMode"`
	Size         string `mapstructure:"size" yaml:"size"`
}

// ObjectStore describes an S3-compatible object store (S3, MinIO, OSS, COS,
// GCS through its S3 interop).
type ObjectStore struct {
	Provider       string      `mapstructure:"provider" yaml:"provider"`
	Endpoint       string      `mapstructure:"endpoint" yaml:"endpoint"`
	Region         string      `mapstructure:"region" yaml:"region"`
	Bucket         string      `mapstructure:"bucket" yaml:"bucket"`
	Prefix         string      `mapstructure:"prefix" yaml:"prefix"`
	ForcePathStyle bool        `mapstructure:"forcePathStyle" yaml:"forcePathStyle"`
	TLS            ObjectTLS   `mapstructure:"tls" yaml:"tls"`
	Credentials    Credentials `mapstructure:"credentials" yaml:"credentials"`
}

// ObjectTLS carries the optional TLS knobs of an object store.
type ObjectTLS struct {
	InsecureSkipVerify bool   `mapstructure:"insecureSkipVerify" yaml:"insecureSkipVerify"`
	CASecret           string `mapstructure:"caSecret" yaml:"caSecret"`
}

// Credentials names the Secret carrying the object-store keys.
type Credentials struct {
	ExistingSecret string `mapstructure:"existingSecret" yaml:"existingSecret"`
	AccessKeyKey   string `mapstructure:"accessKeyKey" yaml:"accessKeyKey"`
	SecretKeyKey   string `mapstructure:"secretKeyKey" yaml:"secretKeyKey"`
}

// Delivery says how inference pods read a backend.
type Delivery struct {
	// Default is the delivery mode a model gets unless it overrides it.
	Default string `mapstructure:"default" yaml:"default"`
	// Allowed lists the delivery modes the backend permits.
	Allowed []string `mapstructure:"allowed" yaml:"allowed"`
	// Cache configures node-local caching.
	Cache DeliveryCache `mapstructure:"cache" yaml:"cache"`
	// Mount configures mounting object storage through a CSI driver.
	Mount DeliveryMount `mapstructure:"mount" yaml:"mount"`
	// Download enables KServe storage-initializer downloads.
	Download DeliveryDownload `mapstructure:"download" yaml:"download"`
}

// DeliveryCache configures the node cache.
type DeliveryCache struct {
	NodeGroup    string `mapstructure:"nodeGroup" yaml:"nodeGroup"`
	WaitForReady bool   `mapstructure:"waitForReady" yaml:"waitForReady"`
}

// DeliveryMount configures the object-store CSI mount.
type DeliveryMount struct {
	StorageClass string `mapstructure:"storageClass" yaml:"storageClass"`
	Size         string `mapstructure:"size" yaml:"size"`
}

// DeliveryDownload configures the storage initializer that fetches an
// object-store model into the pod.
type DeliveryDownload struct {
	Enabled bool `mapstructure:"enabled" yaml:"enabled"`
	// Image is the storage-initializer image. The default is the image the
	// cluster's KServe installation already runs.
	Image string `mapstructure:"image" yaml:"image"`
}

// DefaultDownloadImage is the storage-initializer image used when a backend
// enables download delivery without naming one.
const DefaultDownloadImage = "kserve/storage-initializer:v0.21.0"

// DefaultMountPath is the in-container path weights are mounted at.
const DefaultMountPath = "/data/weights"

// Enabled reports whether any backend is configured.
func (c Config) Enabled() bool { return len(c.Backends) > 0 }

// Normalize fills the derived fields (backend names and the default mount
// path) and, when no storage section exists, synthesizes a default backend
// from the legacy weights settings. The legacy form is the single RWX
// filesystem the chart used to assume: a PVC named pvcName provisioned from
// storageClass and mounted at mountPath.
func (c Config) Normalize(legacyPVC, legacyStorageClass, legacyMountPath string) Config {
	out := c
	if len(out.Backends) == 0 {
		name := "default"
		pvc := strings.TrimSpace(legacyPVC)
		if pvc == "" {
			pvc = "model-weights"
		}
		mount := strings.TrimSpace(legacyMountPath)
		if mount == "" {
			mount = DefaultMountPath
		}
		out.DefaultBackend = name
		out.Backends = map[string]Backend{
			name: {
				Type:       TypeFilesystem,
				Filesystem: Filesystem{ExistingClaim: pvc, MountPath: mount},
				Delivery:   Delivery{Default: ModeMount, Allowed: []string{ModeMount}},
			},
		}
		if strings.TrimSpace(legacyStorageClass) != "" {
			b := out.Backends[name]
			b.Filesystem.Provision = Provision{
				Enabled:      true,
				StorageClass: legacyStorageClass,
				AccessMode:   "ReadWriteMany",
				Size:         "100Gi",
			}
			out.Backends[name] = b
		}
	}
	if out.DefaultBackend == "" && len(out.Backends) == 1 {
		for name := range out.Backends {
			out.DefaultBackend = name
		}
	}
	for name, backend := range out.Backends {
		backend.Name = name
		if backend.Filesystem.MountPath == "" {
			backend.Filesystem.MountPath = DefaultMountPath
		}
		if backend.Delivery.Download.Image == "" {
			backend.Delivery.Download.Image = DefaultDownloadImage
		}
		if backend.Delivery.Default == "" {
			switch backend.Type {
			case TypeObjectStore:
				backend.Delivery.Default = ModeCache
				if !contains(backend.Delivery.Allowed, ModeCache) {
					backend.Delivery.Allowed = append(backend.Delivery.Allowed, ModeCache)
				}
			default:
				backend.Delivery.Default = ModeMount
				if !contains(backend.Delivery.Allowed, ModeMount) {
					backend.Delivery.Allowed = append(backend.Delivery.Allowed, ModeMount)
				}
			}
		}
		out.Backends[name] = backend
	}
	return out
}

// Resolve returns the named backend, or the default one when name is empty.
func (c Config) Resolve(name string) (Backend, error) {
	if name == "" {
		name = c.DefaultBackend
	}
	if name == "" {
		return Backend{}, fmt.Errorf("storage: no storage backend is configured")
	}
	backend, ok := c.Backends[name]
	if !ok {
		return Backend{}, fmt.Errorf("storage: backend %q is not configured", name)
	}
	return backend, nil
}

// Key validates and cleans a model key. A key is always relative: no leading
// slash, no backslash, no "..", no NUL. It is the same shape as the legacy
// weight path, so old model rows keep resolving.
func Key(key string) (string, error) {
	k := strings.TrimSuffix(strings.TrimSpace(key), "/")
	if k == "" {
		return "", fmt.Errorf("storage: key is required")
	}
	if strings.ContainsRune(k, '\x00') || strings.Contains(k, "\\") {
		return "", fmt.Errorf("storage: key must not contain a NUL or a backslash")
	}
	if strings.HasPrefix(k, "/") {
		return "", fmt.Errorf("storage: key must be relative, not absolute")
	}
	for _, part := range strings.Split(k, "/") {
		if part == ".." {
			return "", fmt.Errorf("storage: key must not contain \"..\"")
		}
		if part == "." || part == "" {
			return "", fmt.Errorf("storage: key must not contain empty or \".\" segments")
		}
	}
	return strings.TrimSuffix(path.Clean(k), "/"), nil
}

// CleanJoin joins a root and a relative key, rejecting a key that escapes the
// root. It is the only way the platform composes a filesystem path.
func CleanJoin(root, key string) (string, error) {
	clean, err := Key(key)
	if err != nil {
		return "", err
	}
	if root == "" {
		return "", fmt.Errorf("storage: root is required")
	}
	return path.Join(strings.TrimRight(root, "/"), clean), nil
}

// URI renders the materialized storage URI for a key. The controller and the
// transfer Job consume it; users never type one.
func (b Backend) URI(key string) (string, error) {
	clean, err := Key(key)
	if err != nil {
		return "", err
	}
	switch b.Type {
	case TypeFilesystem:
		claim := strings.TrimSpace(b.Filesystem.ExistingClaim)
		if claim == "" {
			return "", fmt.Errorf("storage: backend %q has no filesystem claim", b.Name)
		}
		return "pvc://" + claim + "/" + clean, nil
	case TypeObjectStore:
		bucket := strings.TrimSpace(b.ObjectStore.Bucket)
		if bucket == "" {
			return "", fmt.Errorf("storage: backend %q has no object-store bucket", b.Name)
		}
		prefix := strings.Trim(strings.TrimSpace(b.ObjectStore.Prefix), "/")
		if prefix != "" {
			clean = prefix + "/" + clean
		}
		return "s3://" + bucket + "/" + clean, nil
	default:
		return "", fmt.Errorf("storage: backend %q has unknown type %q", b.Name, b.Type)
	}
}

// Validate checks the semantic rules a Helm values file cannot express
// through JSON schema alone.
func (c Config) Validate() error {
	if len(c.Backends) == 0 {
		return nil
	}
	if strings.TrimSpace(c.DefaultBackend) == "" {
		return fmt.Errorf("storage.defaultBackend is required when storage.backends is set")
	}
	if _, ok := c.Backends[c.DefaultBackend]; !ok {
		return fmt.Errorf("storage.defaultBackend %q is not in storage.backends", c.DefaultBackend)
	}
	for name, b := range c.Backends {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("storage.backends: backend name must not be empty")
		}
		if b.Name == "" {
			b.Name = name
		}
		switch b.Type {
		case TypeFilesystem:
			if strings.TrimSpace(b.Filesystem.ExistingClaim) == "" && !b.Filesystem.Provision.Enabled {
				return fmt.Errorf("storage.backends.%s: filesystem needs existingClaim or provision.enabled=true", name)
			}
			if b.Filesystem.Provision.Enabled && strings.TrimSpace(b.Filesystem.Provision.Size) == "" {
				return fmt.Errorf("storage.backends.%s: provision.size is required", name)
			}
		case TypeObjectStore:
			if strings.TrimSpace(b.ObjectStore.Bucket) == "" {
				return fmt.Errorf("storage.backends.%s: objectStore.bucket is required", name)
			}
			if strings.TrimSpace(b.ObjectStore.Endpoint) == "" {
				return fmt.Errorf("storage.backends.%s: objectStore.endpoint is required", name)
			}
			if strings.TrimSpace(b.ObjectStore.Credentials.ExistingSecret) == "" {
				return fmt.Errorf("storage.backends.%s: objectStore.credentials.existingSecret is required", name)
			}
		default:
			return fmt.Errorf("storage.backends.%s: type must be filesystem or objectstore", name)
		}
		if b.Delivery.Default == "" {
			return fmt.Errorf("storage.backends.%s: delivery.default is required", name)
		}
		if !contains(b.Delivery.Allowed, b.Delivery.Default) {
			return fmt.Errorf("storage.backends.%s: delivery.default %q is not in delivery.allowed", name, b.Delivery.Default)
		}
		for _, mode := range b.Delivery.Allowed {
			switch mode {
			case ModeMount, ModeDownload, ModeCache:
			default:
				return fmt.Errorf("storage.backends.%s: unknown delivery %q", name, mode)
			}
		}
		if b.Delivery.Default == ModeMount && b.Type == TypeObjectStore &&
			strings.TrimSpace(b.Delivery.Mount.StorageClass) == "" {
			return fmt.Errorf("storage.backends.%s: objectstore mount delivery needs delivery.mount.storageClass", name)
		}
		if b.Delivery.Default == ModeDownload && !b.Delivery.Download.Enabled {
			return fmt.Errorf("storage.backends.%s: delivery.download.enabled must be true", name)
		}
	}
	return nil
}

// Supports reports whether the backend allows a delivery mode.
func (b Backend) Supports(mode string) bool {
	if mode == "" {
		mode = b.Delivery.Default
	}
	return contains(b.Delivery.Allowed, mode)
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// CredentialEnvNames returns the environment variables that carry one named
// backend's object-store credential. The server reads them when it writes an
// upload directly to object storage, and the chart renders them from the
// backend's existing Secret. The name is upper-cased and every character
// outside [A-Z0-9] becomes an underscore, so the chart and Go agree without a
// shared registry.
func CredentialEnvNames(backendName string) (accessKeyEnv, secretKeyEnv string) {
	var key strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(backendName)) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			key.WriteRune(r)
		default:
			key.WriteByte('_')
		}
	}
	if key.Len() == 0 {
		key.WriteString("DEFAULT")
	}
	return "TAAS_STORAGE_" + key.String() + "_ACCESS_KEY", "TAAS_STORAGE_" + key.String() + "_SECRET_KEY"
}
