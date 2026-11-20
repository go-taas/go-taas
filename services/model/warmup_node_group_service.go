package model

import (
	"context"
	"encoding/json"
	"path"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/api/resource"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/warmup"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
)

var warmupGroupNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// warmupGroupsFor resolves the requested (or all) platform-managed node
// groups. The group settings travel with the warmup request, so the
// controller never reads the database.
func (s *Service) warmupGroupsFor(ctx context.Context, db *gorm.DB, requested []string) ([]WarmupNodeGroupSettings, error) {
	var rows []WarmupNodeGroup
	query := db.WithContext(ctx)
	if len(requested) > 0 {
		names := make([]string, 0, len(requested))
		seen := map[string]bool{}
		for _, raw := range requested {
			name := strings.TrimSpace(raw)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
		if len(names) == 0 {
			return nil, nil
		}
		query = query.Where("name IN ?", names)
	}
	if err := query.Order("name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(requested) > 0 && len(rows) != len(strings.Fields(strings.Join(requested, " "))) {
		found := make(map[string]bool, len(rows))
		for _, row := range rows {
			found[row.Name] = true
		}
		for _, name := range requested {
			if trimmed := strings.TrimSpace(name); trimmed != "" && !found[trimmed] {
				return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
					"model: unknown warmup node group %q", trimmed)
			}
		}
	}
	out := make([]WarmupNodeGroupSettings, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.settings())
	}
	return out, nil
}

func (g WarmupNodeGroup) settings() WarmupNodeGroupSettings {
	var selector map[string]string
	_ = json.Unmarshal([]byte(g.NodeSelectorJSON), &selector)
	return WarmupNodeGroupSettings{
		Name:         g.Name,
		StorageLimit: g.StorageLimit,
		HostPath:     g.HostPath,
		StorageClass: g.StorageClass,
		NodeSelector: selector,
	}
}

// ListWarmupNodeGroups returns every platform-managed node group.
func (s *Service) ListWarmupNodeGroups(ctx context.Context, _ *modelv1.ListWarmupNodeGroupsRequest) (*modelv1.ListWarmupNodeGroupsResponse, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, err
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	var rows []WarmupNodeGroup
	if err := db.WithContext(ctx).Order("name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	ready, total, err := s.warmupNodeCounts(ctx, db)
	if err != nil {
		return nil, err
	}
	out := make([]*modelv1.WarmupNodeGroup, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.projection(ready[row.Name], total[row.Name]))
	}
	return &modelv1.ListWarmupNodeGroupsResponse{Response: okResponse(), NodeGroups: out}, nil
}

// warmupNodeCounts counts observed LocalModelNodes per group. A node counts
// as ready when it reports at least one downloaded model.
func (s *Service) warmupNodeCounts(ctx context.Context, db *gorm.DB) (ready, total map[string]int32, err error) {
	var nodes []WarmupNode
	if err := db.WithContext(ctx).Find(&nodes).Error; err != nil {
		return nil, nil, err
	}
	ready = map[string]int32{}
	total = map[string]int32{}
	for _, node := range nodes {
		var groups []string
		_ = json.Unmarshal([]byte(node.NodeGroupsJSON), &groups)
		for _, group := range groups {
			total[group]++
			if node.DownloadedCount > 0 {
				ready[group]++
			}
		}
	}
	return ready, total, nil
}

// CreateWarmupNodeGroup creates a platform-managed node group.
func (s *Service) CreateWarmupNodeGroup(ctx context.Context, req *modelv1.CreateWarmupNodeGroupRequest) (*modelv1.CreateWarmupNodeGroupResponse, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, err
	}
	name, selectorJSON, limit, hostPath, class, err := validateWarmupNodeGroup(
		req.GetName(), req.GetNodeSelector(), req.GetStorageLimit(), req.GetHostPath(), req.GetStorageClass())
	if err != nil {
		return nil, err
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	var existing int64
	if err := db.WithContext(ctx).Model(&WarmupNodeGroup{}).Where("name = ?", name).Count(&existing).Error; err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, apierrors.Newf(apierrors.CodeModelExists, "model: warmup node group %q already exists", name)
	}
	now := time.Now().UTC()
	row := &WarmupNodeGroup{
		Name:             name,
		NodeSelectorJSON: selectorJSON,
		StorageLimit:     limit,
		HostPath:         hostPath,
		StorageClass:     class,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	s.publishWarmupGroup(ctx, row, false)
	return &modelv1.CreateWarmupNodeGroupResponse{Response: okResponse(), NodeGroup: row.projection(0, 0)}, nil
}

// UpdateWarmupNodeGroup edits a node group's selector and storage settings.
func (s *Service) UpdateWarmupNodeGroup(ctx context.Context, req *modelv1.UpdateWarmupNodeGroupRequest) (*modelv1.UpdateWarmupNodeGroupResponse, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, err
	}
	name, selectorJSON, limit, hostPath, class, err := validateWarmupNodeGroup(
		req.GetName(), req.GetNodeSelector(), req.GetStorageLimit(), req.GetHostPath(), req.GetStorageClass())
	if err != nil {
		return nil, err
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	var row WarmupNodeGroup
	if err := db.WithContext(ctx).Where("name = ?", name).First(&row).Error; err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelNotFound, "model: warmup node group %q not found", name)
	}
	row.NodeSelectorJSON = selectorJSON
	row.StorageLimit = limit
	row.HostPath = hostPath
	row.StorageClass = class
	row.UpdatedAt = time.Now().UTC()
	if err := db.WithContext(ctx).Save(&row).Error; err != nil {
		return nil, err
	}
	s.publishWarmupGroup(ctx, &row, false)
	return &modelv1.UpdateWarmupNodeGroupResponse{Response: okResponse(), NodeGroup: row.projection(0, 0)}, nil
}

// DeleteWarmupNodeGroup deletes a node group once no live warmup depends on
// it. A group still referenced by a queued, warming or ready warmup would
// strand the LocalModelCache, so the deletion is rejected.
func (s *Service) DeleteWarmupNodeGroup(ctx context.Context, req *modelv1.DeleteWarmupNodeGroupRequest) (*modelv1.DeleteWarmupNodeGroupResponse, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: warmup node group name is required")
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	var row WarmupNodeGroup
	if err := db.WithContext(ctx).Where("name = ?", name).First(&row).Error; err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelNotFound, "model: warmup node group %q not found", name)
	}
	var active int64
	if err := db.WithContext(ctx).Model(&ModelCacheEntry{}).
		Where("node_groups_json LIKE ? AND state IN ?", `%`+jsonString(name)+`%`,
			[]string{warmup.StateQueued, warmup.StateWarming, warmup.StateReady}).
		Count(&active).Error; err != nil {
		return nil, err
	}
	if active > 0 {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: node group %q still has %d active warmup(s); cancel them first", name, active)
	}
	if err := db.WithContext(ctx).Delete(&row).Error; err != nil {
		return nil, err
	}
	s.publishWarmupGroup(ctx, &row, true)
	return &modelv1.DeleteWarmupNodeGroupResponse{Response: okResponse()}, nil
}

// ListWarmupNodes returns the last LocalModelNode snapshot.
func (s *Service) ListWarmupNodes(ctx context.Context, _ *modelv1.ListWarmupNodesRequest) (*modelv1.ListWarmupNodesResponse, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, err
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	var rows []WarmupNode
	if err := db.WithContext(ctx).Order("name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	cacheNames := map[string]bool{}
	parsed := make([][]warmup.NodeModel, 0, len(rows))
	for _, row := range rows {
		var models []warmup.NodeModel
		_ = json.Unmarshal([]byte(row.ModelsJSON), &models)
		for _, model := range models {
			if model.CacheName != "" {
				cacheNames[model.CacheName] = true
			}
		}
		parsed = append(parsed, models)
	}
	names := make([]string, 0, len(cacheNames))
	for name := range cacheNames {
		names = append(names, name)
	}
	cacheNamesByResource := map[string]string{}
	if len(names) > 0 {
		var caches []ModelCacheEntry
		if err := db.WithContext(ctx).Where("cache_name IN ?", names).Find(&caches).Error; err != nil {
			return nil, err
		}
		for _, cache := range caches {
			cacheNamesByResource[cache.CacheName] = cache.ModelName
		}
	}
	out := make([]*modelv1.WarmupNode, 0, len(rows))
	for i, row := range rows {
		var groups []string
		_ = json.Unmarshal([]byte(row.NodeGroupsJSON), &groups)
		models := make([]*modelv1.WarmupNodeModel, 0, len(parsed[i]))
		for _, model := range parsed[i] {
			models = append(models, &modelv1.WarmupNodeModel{
				CacheName: model.CacheName,
				ModelName: firstNonEmpty(cacheNamesByResource[model.CacheName], model.ModelName),
				NodeGroup: model.NodeGroup,
				State:     model.State,
			})
		}
		out = append(out, &modelv1.WarmupNode{
			Name:            row.Name,
			NodeGroups:      groups,
			Models:          models,
			ModelCount:      int32(row.ModelCount),      //nolint:gosec // bounded by Kubernetes object counts
			DownloadedCount: int32(row.DownloadedCount), //nolint:gosec // bounded by Kubernetes object counts
			UpdatedAt:       row.ObservedAt.Unix(),
		})
	}
	return &modelv1.ListWarmupNodesResponse{Response: okResponse(), Nodes: out}, nil
}

// HandleWarmupNodeSnapshot replaces the stored LocalModelNode projection with
// the controller's full snapshot, so deleted nodes disappear.
func (s *Service) HandleWarmupNodeSnapshot(ctx context.Context, body []byte) error {
	var snapshot warmup.NodeSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return err
	}
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1 = 1").Delete(&WarmupNode{}).Error; err != nil {
			return err
		}
		for _, node := range snapshot.Nodes {
			name := strings.TrimSpace(node.Name)
			if name == "" {
				continue
			}
			groups, _ := json.Marshal(node.NodeGroups)
			models, _ := json.Marshal(node.Models)
			observed := node.ObservedAt
			if observed.IsZero() {
				observed = now
			}
			row := &WarmupNode{
				Name:            name,
				NodeGroupsJSON:  string(groups),
				ModelsJSON:      string(models),
				ModelCount:      node.ModelCount,
				DownloadedCount: node.DownloadedCount,
				ObservedAt:      observed,
				UpdatedAt:       now,
			}
			if err := tx.Create(row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func validateWarmupNodeGroup(name string, selector map[string]string, limit, hostPath, class string) (string, string, string, string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 63 || !warmupGroupNameRE.MatchString(name) {
		return "", "", "", "", "", apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: node group name must be a DNS label (lowercase letters, digits and '-', at most 63 characters)")
	}
	cleanSelector := make(map[string]string, len(selector))
	for key, value := range selector {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			return "", "", "", "", "", apierrors.Newf(apierrors.CodeModelPathInvalid,
				"model: node selector keys must not be empty")
		}
		if value == "" {
			return "", "", "", "", "", apierrors.Newf(apierrors.CodeModelPathInvalid,
				"model: node selector %q needs a value", key)
		}
		cleanSelector[key] = value
	}
	if len(cleanSelector) == 0 {
		return "", "", "", "", "", apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: at least one node selector label is required")
	}
	limit = strings.TrimSpace(limit)
	if limit == "" {
		limit = "100Gi"
	}
	if _, err := resource.ParseQuantity(limit); err != nil {
		return "", "", "", "", "", apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: storage limit %q is not a Kubernetes quantity such as 100Gi", limit)
	}
	hostPath = strings.TrimSpace(hostPath)
	if hostPath == "" {
		hostPath = "/models"
	}
	if !path.IsAbs(hostPath) || path.Clean(hostPath) != hostPath || hostPath == "/" {
		return "", "", "", "", "", apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: host path must be a clean absolute path such as /models")
	}
	class = strings.TrimSpace(class)
	if class != "" && (len(class) > 63 || !warmupGroupNameRE.MatchString(class)) {
		return "", "", "", "", "", apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: storage class must be a DNS label")
	}
	selectorJSON, err := json.Marshal(cleanSelector)
	if err != nil {
		return "", "", "", "", "", err
	}
	return name, string(selectorJSON), limit, hostPath, class, nil
}

func (s *Service) publishWarmupGroup(ctx context.Context, row *WarmupNodeGroup, deleted bool) {
	event := warmup.NodeGroupEvent{
		NodeGroup: warmup.NodeGroup{
			Name:         row.Name,
			StorageLimit: row.StorageLimit,
			HostPath:     row.HostPath,
			StorageClass: row.StorageClass,
		},
		Deleted: deleted,
	}
	_ = json.Unmarshal([]byte(row.NodeSelectorJSON), &event.NodeGroup.NodeSelector)
	if err := s.publishTransfer(ctx, mq.DefaultSubjects().ModelWarmupGroups, &event); err != nil {
		logger.S().Warnw("model: publish warmup node group failed; the controller sync loop will retry",
			"group", row.Name, "deleted", deleted, "err", err)
	}
}

func (g WarmupNodeGroup) projection(ready, total int32) *modelv1.WarmupNodeGroup {
	var selector map[string]string
	_ = json.Unmarshal([]byte(g.NodeSelectorJSON), &selector)
	return &modelv1.WarmupNodeGroup{
		Name:         g.Name,
		NodeSelector: selector,
		StorageLimit: g.StorageLimit,
		HostPath:     g.HostPath,
		StorageClass: g.StorageClass,
		CreatedAt:    g.CreatedAt.Unix(),
		UpdatedAt:    g.UpdatedAt.Unix(),
		ReadyNodes:   ready,
		TotalNodes:   total,
	}
}

func jsonString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// SyncWarmupNodeGroups publishes every stored group to the controller. It is
// idempotent and covers a controller that restarted after the original
// create/update event.
func (s *Service) SyncWarmupNodeGroups(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	var rows []WarmupNodeGroup
	if err := db.WithContext(ctx).Order("name ASC").Find(&rows).Error; err != nil {
		return err
	}
	event := warmup.NodeGroupEvent{Snapshot: true, NodeGroups: make([]warmup.NodeGroup, 0, len(rows))}
	for i := range rows {
		group := warmup.NodeGroup{
			Name:         rows[i].Name,
			StorageLimit: rows[i].StorageLimit,
			HostPath:     rows[i].HostPath,
			StorageClass: rows[i].StorageClass,
		}
		_ = json.Unmarshal([]byte(rows[i].NodeSelectorJSON), &group.NodeSelector)
		event.NodeGroups = append(event.NodeGroups, group)
	}
	return s.publishTransfer(ctx, mq.DefaultSubjects().ModelWarmupGroups, &event)
}

// RunWarmupNodeGroupSync publishes the node groups once and then on an
// interval; it implements server.Runner through a small adapter.
func (s *Service) RunWarmupNodeGroupSync(ctx context.Context, interval time.Duration) error {
	timer := time.NewTicker(interval)
	defer timer.Stop()
	if err := s.SyncWarmupNodeGroups(ctx); err != nil {
		logger.S().Warnw("model: warmup node group sync failed", "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			if err := s.SyncWarmupNodeGroups(ctx); err != nil {
				logger.S().Warnw("model: warmup node group sync failed", "err", err)
			}
		}
	}
}
