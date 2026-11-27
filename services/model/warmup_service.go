package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/storage"
	"github.com/go-taas/go-taas/pkg/transfer"
	"github.com/go-taas/go-taas/pkg/warmup"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
)

// SetWarmupSettings installs the warmup enable flag. Node groups are
// platform-managed rows, not configuration: they are created and edited in
// the Operations console and read from the database on every request.
func (s *Service) SetWarmupSettings(enabled bool) {
	s.warmupEnabled = enabled
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
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	sizeBytes := s.resolveWeightSize(ctx, db, row.ID, backend.Name, version.WeightPath)
	groups, err := s.warmupGroupsFor(ctx, db, req.GetNodeGroups())
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: no warmup node group is configured; create one in Operations > Warmup node groups")
	}
	now := time.Now().UTC()
	entry := &ModelCacheEntry{
		ID:             uuid.NewString(),
		ModelID:        row.ID,
		ModelName:      row.Name,
		Version:        version.Version,
		Backend:        backend.Name,
		SourceURI:      sourceURI,
		WeightPath:     version.WeightPath,
		SizeBytes:      sizeBytes,
		NodeGroupsJSON: mustJSON(groupNames(groups)),
		State:          warmup.StateQueued,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	// A model version can only have one live cache: KServe rejects a second
	// LocalModelCache with the same source URI. Its node groups may still
	// grow, though: re-warming an already queued/warming/ready version on a
	// new pool must reach the controller (which updates the existing cache's
	// nodeGroups), not be swallowed by the existing row.
	var existing ModelCacheEntry
	err = db.WithContext(ctx).
		Where("model_id = ? AND version = ? AND backend = ? AND source_uri = ? AND state NOT IN ?",
			row.ID, version.Version, backend.Name, sourceURI,
			[]string{warmup.StateFailed, warmup.StateCancelled}).
		Order("created_at DESC").
		First(&existing).Error
	if err == nil {
		return s.extendWarmupGroups(ctx, db, &existing, groups, sizeBytes)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
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
		SizeBytes:  sizeBytes,
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

// extendWarmupGroups adds node groups to a live cache entry. Warming the
// same version again on another pool must extend the LocalModelCache's node
// groups (the controller replaces the spec list with what the request
// carries), so the request is republished with the union. An unchanged set is
// an idempotent no-op that returns the existing row.
func (s *Service) extendWarmupGroups(ctx context.Context, db *gorm.DB, entry *ModelCacheEntry, requested []WarmupNodeGroupSettings, sizeBytes int64) (*modelv1.WarmModelResponse, error) {
	requestedNames := groupNames(requested)
	current := decodeStringList(entry.NodeGroupsJSON)
	all, err := s.warmupGroupsFor(ctx, db, nil)
	if err != nil {
		return nil, err
	}
	available := make(map[string]WarmupNodeGroupSettings, len(all))
	for _, g := range all {
		available[g.Name] = g
	}
	// A group deleted since the last warmup must not block the request.
	kept := make([]string, 0, len(current))
	for _, name := range current {
		if _, ok := available[name]; ok {
			kept = append(kept, name)
		}
	}
	merged := mergeStringList(kept, requestedNames)
	if sameStringSet(current, merged) {
		return &modelv1.WarmModelResponse{Response: okResponse(), Cache: cacheProjection(entry)}, nil
	}
	nodes := make([]warmup.NodeGroup, 0, len(merged))
	for _, name := range merged {
		g := available[name]
		nodes = append(nodes, warmup.NodeGroup{
			Name:         g.Name,
			NodeSelector: g.NodeSelector,
			StorageLimit: g.StorageLimit,
			HostPath:     g.HostPath,
			StorageClass: g.StorageClass,
		})
	}
	groupsJSON := mustJSON(merged)
	updates := map[string]any{
		"node_groups_json": groupsJSON,
		"state":            warmup.StateQueued,
		"message":          "",
		"updated_at":       time.Now().UTC(),
	}
	if err := db.WithContext(ctx).Model(&ModelCacheEntry{}).Where("id = ?", entry.ID).Updates(updates).Error; err != nil {
		return nil, err
	}
	entry.NodeGroupsJSON = groupsJSON
	entry.State = warmup.StateQueued
	entry.Message = ""
	entry.UpdatedAt = time.Now().UTC()
	size := sizeBytes
	if size <= 0 {
		size = entry.SizeBytes
	}
	payload := &warmup.Request{
		ID:         entry.ID,
		ModelID:    entry.ModelID,
		ModelName:  entry.ModelName,
		Version:    entry.Version,
		Backend:    entry.Backend,
		WeightPath: entry.WeightPath,
		SourceURI:  entry.SourceURI,
		SizeBytes:  size,
		NodeGroups: nodes,
	}
	if err := s.publishTransfer(ctx, mq.DefaultSubjects().ModelWarmups, payload); err != nil {
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

// groupNames returns the names of resolved warmup group settings.
func groupNames(groups []WarmupNodeGroupSettings) []string {
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		names = append(names, g.Name)
	}
	return names
}

// mustJSON marshals a plain value into the JSON stored on a row. The only
// caller marshals a []string, which cannot fail; an empty string is a safe
// fallback for the not-null column.
func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

// decodeStringList parses a JSON string array, returning nil on anything
// unexpected so a legacy row never blocks a warmup.
func decodeStringList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// mergeStringList returns a's names followed by the names of b not already
// present, trimming and dropping empties.
func mergeStringList(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, raw := range list {
			name := strings.TrimSpace(raw)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// sameStringSet reports whether two name lists hold the same set of names.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, name := range a {
		seen[strings.TrimSpace(name)]++
	}
	for _, name := range b {
		key := strings.TrimSpace(name)
		if seen[key] == 0 {
			return false
		}
		seen[key]--
	}
	return true
}

// ListModelWarmups returns one page of warmup entries, newest first. It is
// the current name of ListModelCacheEntries; both share one query so the
// legacy console path and the renamed endpoint cannot disagree.
func (s *Service) ListModelWarmups(ctx context.Context, req *modelv1.ListModelWarmupsRequest) (*modelv1.ListModelWarmupsResponse, error) {
	caches, pageMeta, err := s.listWarmups(ctx, req.GetPage(), req.GetModelId(), req.GetState())
	if err != nil {
		return nil, err
	}
	return &modelv1.ListModelWarmupsResponse{Response: okResponse(), Warmups: caches, PageMeta: pageMeta}, nil
}

// ListModelCacheEntries returns one page of cache entries, newest first.
func (s *Service) ListModelCacheEntries(ctx context.Context, req *modelv1.ListModelCacheEntriesRequest) (*modelv1.ListModelCacheEntriesResponse, error) {
	caches, pageMeta, err := s.listWarmups(ctx, req.GetPage(), req.GetModelId(), req.GetState())
	if err != nil {
		return nil, err
	}
	return &modelv1.ListModelCacheEntriesResponse{Response: okResponse(), Caches: caches, PageMeta: pageMeta}, nil
}

func (s *Service) listWarmups(ctx context.Context, page *commonv1.PageRequest, modelID, state string) ([]*modelv1.ModelCacheEntry, *commonv1.PageMeta, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, nil, err
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, nil, err
	}
	offset, limit := normalizePagination(page)
	query := db.WithContext(ctx).Model(&ModelCacheEntry{})
	if modelID = strings.TrimSpace(modelID); modelID != "" {
		query = query.Where("model_id = ?", modelID)
	}
	if state = strings.TrimSpace(state); state != "" {
		query = query.Where("state = ?", state)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, nil, err
	}
	var rows []*ModelCacheEntry
	// Ready warmups float to the top so the console lists usable caches first;
	// every other state keeps the newest-first order.
	if err := query.
		Order(clause.OrderBy{Expression: clause.Expr{
			SQL:  "CASE WHEN state = ? THEN 0 ELSE 1 END, created_at DESC, id DESC",
			Vars: []interface{}{warmup.StateReady},
		}}).
		Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	s.backfillWarmupSizes(ctx, db, rows)
	caches := make([]*modelv1.ModelCacheEntry, 0, len(rows))
	for _, row := range rows {
		caches = append(caches, cacheProjection(row))
	}
	return caches, &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampToInt32(limit)}, nil
}

// backfillWarmupSizes repairs rows created before the server resolved weight
// sizes. Those rows carry the 1-byte placeholder the controller used when the
// request had no size, which the console rendered as "1 B".
func (s *Service) backfillWarmupSizes(ctx context.Context, db *gorm.DB, rows []*ModelCacheEntry) {
	for _, row := range rows {
		if row.SizeBytes > 1 {
			continue
		}
		size := s.resolveWeightSize(ctx, db, row.ModelID, row.Backend, row.WeightPath)
		if size <= 1 {
			continue
		}
		row.SizeBytes = size
		// UpdateColumn skips the automatic updated_at touch: repairing a
		// cached size is not a state change the console should show.
		_ = db.WithContext(ctx).Model(&ModelCacheEntry{}).
			Where("id = ? AND size_bytes <= ?", row.ID, int64(1)).
			UpdateColumn("size_bytes", size).Error
	}
}

// resolveWeightSize returns the byte size of one version's weights. A mounted
// filesystem backend is authoritative because it reflects what warmup copies;
// otherwise the newest successful download/transfer record is the only size
// the stateless server knows.
func (s *Service) resolveWeightSize(ctx context.Context, db *gorm.DB, modelID, backendName, weightPath string) int64 {
	if strings.TrimSpace(weightPath) != "" {
		if backend, err := s.storageConfig.Resolve(backendName); err == nil && backend.Type == storage.TypeFilesystem {
			if root := s.backendMountPath(backend); strings.TrimSpace(root) != "" {
				if dir, joinErr := storage.CleanJoin(root, weightPath); joinErr == nil {
					if size := storage.DirSize(dir); size > 1 {
						return size
					}
				}
			}
		}
	}
	var download DownloadJob
	if err := db.WithContext(ctx).
		Where("model_id = ? AND state = ? AND total_bytes > 0", modelID, DownloadStateSucceeded).
		Order("created_at DESC").Limit(1).Find(&download).Error; err == nil && download.TotalBytes > 0 {
		return download.TotalBytes
	}
	var moved ModelTransfer
	if err := db.WithContext(ctx).
		Where("model_id = ? AND state = ? AND (total_bytes > 0 OR bytes > 0)", modelID, transfer.StateSucceeded).
		Order("created_at DESC").Limit(1).Find(&moved).Error; err == nil {
		if moved.TotalBytes > 1 {
			return moved.TotalBytes
		}
		if moved.Bytes > 1 {
			return moved.Bytes
		}
	}
	return 0
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
		"updated_at":        time.Now().UTC(),
	}
	// A controller that could not resolve a size sends 0 or the legacy
	// 1-byte placeholder; keep the resolved value already on the row.
	if status.SizeBytes > 1 {
		updates["size_bytes"] = status.SizeBytes
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
