package infer

import (
	"strings"

	"github.com/go-taas/go-taas/pkg/storage"
	"github.com/go-taas/go-taas/services/model"
)

// servingStorage is the concrete storage plan a serving pod runs with. The
// controller turns it into a volume and an engine model directory; the
// control plane resolves it here because it owns the backend configuration.
type servingStorage struct {
	// Backend is the backend name the version lives in.
	Backend string
	// Delivery is mount, download or cache.
	Delivery string
	// URI is the backend-qualified model URI.
	URI string
	// MountPath is the filesystem mount path for a mounted backend.
	MountPath string
	// Claim name is the filesystem PVC for a mounted backend.
	Claim string
	// The fields below configure an object-store download.
	DownloadImage     string
	Endpoint          string
	Region            string
	ForcePathStyle    bool
	CredentialsSecret string
	AccessKeyKey      string
	SecretKeyKey      string
}

// servingStorageFor resolves the delivery plan for a model version. When no
// storage configuration or version is available it returns an empty plan and
// the controller keeps the legacy shared-weights mount.
func (s *Service) servingStorageFor(version *model.Version) servingStorage {
	return servingStorageFor(s.storageConfig, version)
}

// servingStorageFor resolves a version's delivery plan against a storage
// configuration. It is package-level so the convergers, which do not own a
// Service, can rebuild the same complete desired state.
func servingStorageFor(cfg storage.Config, version *model.Version) servingStorage {
	if version == nil || !cfg.Enabled() {
		return servingStorage{}
	}
	backend, err := cfg.Resolve(version.StorageBackend)
	if err != nil {
		return servingStorage{}
	}
	uri, err := backend.URI(version.WeightPath)
	if err != nil {
		return servingStorage{}
	}
	mode := strings.TrimSpace(backend.Delivery.Default)
	if mode == "" {
		mode = storage.ModeMount
	}
	name := backend.Name
	if name == "" {
		// An un-normalized config (unit tests, hand-built values) has no
		// derived backend name yet; keep the plan non-empty so the event
		// carries the delivery.
		name = version.StorageBackend
		if name == "" {
			name = cfg.DefaultBackend
		}
	}
	plan := servingStorage{Backend: name, Delivery: mode, URI: uri}
	if backend.Type == storage.TypeFilesystem {
		plan.MountPath = backend.Filesystem.MountPath
		if plan.MountPath == "" {
			plan.MountPath = storage.DefaultMountPath
		}
		plan.Claim = strings.TrimSpace(backend.Filesystem.ExistingClaim)
		return plan
	}
	// Object stores cannot be mounted without a CSI driver on this platform,
	// so mount falls back to the download initializer when the backend
	// permits it.
	if backend.Type == storage.TypeObjectStore {
		if mode != storage.ModeDownload && mode != storage.ModeCache {
			if backend.Supports(storage.ModeDownload) {
				plan.Delivery = storage.ModeDownload
			}
		}
		plan.DownloadImage = backend.Delivery.Download.Image
		if plan.DownloadImage == "" {
			plan.DownloadImage = storage.DefaultDownloadImage
		}
		plan.Endpoint = backend.ObjectStore.Endpoint
		plan.Region = backend.ObjectStore.Region
		plan.ForcePathStyle = backend.ObjectStore.ForcePathStyle
		plan.CredentialsSecret = backend.ObjectStore.Credentials.ExistingSecret
		plan.AccessKeyKey = backend.ObjectStore.Credentials.AccessKeyKey
		if plan.AccessKeyKey == "" {
			plan.AccessKeyKey = "accessKey"
		}
		plan.SecretKeyKey = backend.ObjectStore.Credentials.SecretKeyKey
		if plan.SecretKeyKey == "" {
			plan.SecretKeyKey = "secretKey"
		}
	}
	return plan
}

// applyServingStorage stamps the resolved storage plan onto a change event.
// A nil version or a plan with no backend leaves the event untouched, which
// is how delete and stop events and legacy unit tests keep working.
func (s *Service) applyServingStorage(evt changeEvent, version *model.Version) changeEvent {
	return applyServingStorage(evt, version, s.storageConfig)
}

// applyServingStorage stamps a delivery plan resolved against cfg onto a
// change event. Keeping it package-level lets every publisher of a service's
// desired state carry the same storage plan.
func applyServingStorage(evt changeEvent, version *model.Version, cfg storage.Config) changeEvent {
	plan := servingStorageFor(cfg, version)
	if plan.Backend == "" {
		return evt
	}
	evt.Model.StorageBackend = plan.Backend
	evt.Model.Delivery = plan.Delivery
	evt.Model.ModelURI = plan.URI
	evt.Model.MountPath = plan.MountPath
	evt.Model.ClaimName = plan.Claim
	evt.Model.DownloadImage = plan.DownloadImage
	evt.Model.ObjectEndpoint = plan.Endpoint
	evt.Model.ObjectRegion = plan.Region
	evt.Model.ObjectForcePathStyle = plan.ForcePathStyle
	evt.Model.CredentialsSecret = plan.CredentialsSecret
	evt.Model.AccessKeyKey = plan.AccessKeyKey
	evt.Model.SecretKeyKey = plan.SecretKeyKey
	return evt
}
