package model

import (
	"path/filepath"
	"strings"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/storage"
)

// resolveStorageBackend resolves the backend a workflow asked for. An
// empty name means the platform default backend; when no storage backend is
// configured at all it resolves to the zero backend so the legacy
// single-directory installation keeps working.
func (s *Service) resolveStorageBackend(name string) (storage.Backend, error) {
	name = strings.TrimSpace(name)
	if !s.storageConfig.Enabled() {
		if name == "" {
			return storage.Backend{}, nil
		}
		return storage.Backend{}, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: storage backend %q is not configured", name)
	}
	backend, err := s.storageConfig.Resolve(name)
	if err != nil {
		return storage.Backend{}, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: %v", err)
	}
	return backend, nil
}

// backendMountPath is the local filesystem root a backend writes into.
// The configured mount path wins; the legacy weights directory is the
// fallback for the default backend of an installation that predates the
// multi-backend schema.
func (s *Service) backendMountPath(backend storage.Backend) string {
	if backend.Type == storage.TypeFilesystem {
		if root := strings.TrimSpace(backend.Filesystem.MountPath); root != "" {
			return root
		}
	}
	return s.weightsDir
}

// inlineBackendSupported reports whether the in-process workflows can
// write straight into the backend. They write to a local directory, so an
// object store needs the transfer Job path instead.
func (s *Service) inlineBackendSupported(backend storage.Backend) bool {
	if backend.Name == "" {
		return true
	}
	if backend.Type != storage.TypeFilesystem {
		return false
	}
	root := strings.TrimSpace(backend.Filesystem.MountPath)
	if root == "" {
		return true
	}
	return filepath.Clean(root) == filepath.Clean(s.weightsDir)
}
