package model

import (
	"context"
	"sort"
	"strings"

	"github.com/go-taas/go-taas/pkg/storage"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
)

// ListStorageBackends lists the configured storage backends. It carries no
// credential material: the console only needs the names, types and delivery
// capabilities to populate its selectors.
func (s *Service) ListStorageBackends(ctx context.Context, _ *modelv1.ListStorageBackendsRequest) (*modelv1.ListStorageBackendsResponse, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, err
	}
	cfg := s.storageConfig
	names := make([]string, 0, len(cfg.Backends))
	for name := range cfg.Backends {
		names = append(names, name)
	}
	sort.Strings(names)
	backends := make([]*modelv1.StorageBackend, 0, len(names))
	for _, name := range names {
		backend := cfg.Backends[name]
		allowed := append([]string(nil), backend.Delivery.Allowed...)
		sort.Strings(allowed)
		backends = append(backends, &modelv1.StorageBackend{
			Name:              name,
			Type:              backend.Type,
			DefaultDelivery:   backend.Delivery.Default,
			AllowedDeliveries: allowed,
			IsDefault:         name == cfg.DefaultBackend,
			MountPath:         backend.Filesystem.MountPath,
			Provider:          backend.ObjectStore.Provider,
			Endpoint:          backend.ObjectStore.Endpoint,
			Bucket:            backend.ObjectStore.Bucket,
			WarmupEnabled:     s.warmupEnabled && backend.Supports(storage.ModeCache),
		})
	}
	return &modelv1.ListStorageBackendsResponse{
		Response:       okResponse(),
		Backends:       backends,
		DefaultBackend: strings.TrimSpace(cfg.DefaultBackend),
	}, nil
}
