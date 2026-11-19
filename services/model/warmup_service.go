package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/warmup"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
)

// SetWarmupSettings installs the warmup enable flag and the configured node
// groups. The model service copies them so it stays free of the config
// package.
func (s *Service) SetWarmupSettings(enabled bool, groups []WarmupNodeGroupSettings) {
	s.warmupEnabled = enabled
	s.warmupNodeGroups = groups
}

// WarmModel records a warmup request and hands it to the controller, which
// creates the LocalModelNodeGroup and LocalModelCache resources.
func (s *Service) WarmModel(ctx context.Context, req *modelv1.WarmModelRequest) (*modelv1.WarmModelResponse, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, err
	}
	if !s.warmupEnabled {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: model warmup is not enabled")
	}
	modelID := strings.TrimSpace(req.GetModelId())
	if modelID == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: model_id is required")
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetModel(ctx, modelID)
	if err != nil {
		return nil, err
	}
	version, err := s.resolveTransferVersion(ctx, repo, row.ID, strings.TrimSpace(req.GetVersion()))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(version.WeightPath) == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: model version has no weight path")
	}
	cfg := s.storageConfig
	backend, err := cfg.Resolve(version.StorageBackend)
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: %v", err)
	}
	sourceURI, err := backend.URI(version.WeightPath)
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: %v", err)
	}
	groups := s.warmupNodeGroups
	if requested := req.GetNodeGroups(); len(requested) > 0 {
		byName := make(map[string]WarmupNodeGroupSettings, len(s.warmupNodeGroups))
		for _, group := range s.warmupNodeGroups {
			byName[group.Name] = group
		}
		groups = make([]WarmupNodeGroupSettings, 0, len(requested))
		for _, name := range requested {
			group, ok := byName[strings.TrimSpace(name)]
			if !ok {
				return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: unknown warmup node group %q", name)
			}
			groups = append(groups, group)
		}
	}
	if len(groups) == 0 {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: no warmup node group is configured")
	}
	now := time.Now().UTC()
	entry := &ModelCacheEntry{
		ID:         uuid.NewString(),
		ModelID:    row.ID,
		ModelName:  row.Name,
		Version:    version.Version,
		Backend:    backend.Name,
		SourceURI:  sourceURI,
		WeightPath: version.WeightPath,
		State:      warmup.StateQueued,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	if err := db.WithContext(ctx).Create(entry).Error; err != nil {
		return nil, err
	}
	nodes := make([]warmup.NodeGroup, 0, len(groups))
	for _, group := range groups {
		nodes = append(nodes, warmup.NodeGroup{
			Name:         group.Name,
			NodeSelector: group.NodeSelector,
			StorageLimit: group.StorageLimit,
			HostPath:     group.HostPath,
			StorageClass: group.StorageClass,
		})
	}
	payload := &warmup.Request{
		ID:         entry.ID,
		ModelID:    row.ID,
		ModelName:  row.Name,
		Version:    version.Version,
		Backend:    backend.Name,
		WeightPath: version.WeightPath,
		SourceURI:  sourceURI,
		NodeGroups: nodes,
	}
	if err := s.publishTransfer(ctx, mq.DefaultSubjects().ModelWarmups, payload); err != nil {
		// The row exists, so a controller reconnect does not know about it.
		// Fail the row instead of leaving it queued forever.
		entry.State = warmup.StateFailed
		entry.Message = "warmup controller unavailable: " + err.Error()
		_ = db.WithContext(ctx).Model(&ModelCacheEntry{}).Where("id = ?", entry.ID).Updates(map[string]any{
			"state":      entry.State,
			"message":    entry.Message,
			"updated_at": time.Now().UTC(),
		}).Error
		return nil, apierrors.Newf(apierrors.CodeInternal, "model: model warmup controller is unavailable")
	}
	return &modelv1.WarmModelResponse{Response: okResponse(), Cache: cacheProjection(entry)}, nil
}

// ListModelCacheEntries returns one page of cache entries, newest first.
func (s *Service) ListModelCacheEntries(ctx context.Context, req *modelv1.ListModelCacheEntriesRequest) (*modelv1.ListModelCacheEntriesResponse, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, err
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	query := db.WithContext(ctx).Model(&ModelCacheEntry{})
	if modelID := strings.TrimSpace(req.GetModelId()); modelID != "" {
		query = query.Where("model_id = ?", modelID)
	}
	if state := strings.TrimSpace(req.GetState()); state != "" {
		query = query.Where("state = ?", state)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var rows []*ModelCacheEntry
	if err := query.Order("created_at DESC").Order("id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	caches := make([]*modelv1.ModelCacheEntry, 0, len(rows))
	for _, row := range rows {
		caches = append(caches, cacheProjection(row))
	}
	return &modelv1.ListModelCacheEntriesResponse{
		Response: okResponse(),
		Caches:   caches,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampToInt32(limit)},
	}, nil
}

// CancelModelWarmup marks a cache cancelled and asks the controller to
// delete its LocalModelCache.
func (s *Service) CancelModelWarmup(ctx context.Context, req *modelv1.CancelModelWarmupRequest) (*modelv1.CancelModelWarmupResponse, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.GetCacheId())
	if id == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: cache_id is required")
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	var row ModelCacheEntry
	if err := db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelNotFound, "model: cache %s not found", id)
	}
	if warmup.Terminal(row.State) && row.State != warmup.StateReady {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: cache is already %s", row.State)
	}
	now := time.Now().UTC()
	if err := db.WithContext(ctx).Model(&ModelCacheEntry{}).Where("id = ?", id).Updates(map[string]any{
		"state":      warmup.StateCancelled,
		"message":    "cancelled by operator",
		"updated_at": now,
	}).Error; err != nil {
		return nil, err
	}
	_ = s.publishTransfer(ctx, mq.DefaultSubjects().ModelWarmups, &warmup.Request{ID: id, ModelID: row.ModelID, NodeGroups: nil})
	row.State = warmup.StateCancelled
	row.Message = "cancelled by operator"
	row.UpdatedAt = now
	return &modelv1.CancelModelWarmupResponse{Response: okResponse(), Cache: cacheProjection(&row)}, nil
}

// HandleModelWarmupStatus applies a controller cache observation.
func (s *Service) HandleModelWarmupStatus(ctx context.Context, body []byte) error {
	var status warmup.Status
	if err := json.Unmarshal(body, &status); err != nil {
		return err
	}
	if strings.TrimSpace(status.ID) == "" {
		return fmt.Errorf("model: warmup status has no id")
	}
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	warmed, _ := json.Marshal(status.WarmedNodes)
	groups, _ := json.Marshal(status.NodeGroups)
	updates := map[string]any{
		"state":             status.State,
		"message":           status.Message,
		"cache_name":        status.CacheName,
		"node_groups_json":  string(groups),
		"warmed_nodes_json": string(warmed),
		"total_nodes":       status.TotalNodes,
		"size_bytes":        status.SizeBytes,
		"updated_at":        time.Now().UTC(),
	}
	result := db.WithContext(ctx).Model(&ModelCacheEntry{}).
		Where("id = ? AND state <> ?", status.ID, warmup.StateCancelled).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := db.WithContext(ctx).Model(&ModelCacheEntry{}).Where("id = ?", status.ID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("model: cache %s not found", status.ID)
		}
	}
	return nil
}

// cacheProjection maps a cache row to its API message.
func cacheProjection(row *ModelCacheEntry) *modelv1.ModelCacheEntry {
	var nodes []string
	_ = json.Unmarshal([]byte(row.WarmedNodesJSON), &nodes)
	var groups []string
	_ = json.Unmarshal([]byte(row.NodeGroupsJSON), &groups)
	return &modelv1.ModelCacheEntry{
		CacheId:     row.ID,
		ModelId:     row.ModelID,
		ModelName:   row.ModelName,
		Version:     row.Version,
		Backend:     row.Backend,
		SourceUri:   row.SourceURI,
		WeightPath:  row.WeightPath,
		CacheName:   row.CacheName,
		NodeGroups:  groups,
		State:       row.State,
		Message:     row.Message,
		WarmedNodes: nodes,
		TotalNodes:  int32(row.TotalNodes), //nolint:gosec // a node count cannot exceed int32
		SizeBytes:   row.SizeBytes,
		CreatedAt:   row.CreatedAt.Unix(),
		UpdatedAt:   row.UpdatedAt.Unix(),
	}
}
