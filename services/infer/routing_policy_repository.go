package infer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// ErrRoutingRevisionConflict marks a stale expected_revision (10313).
var ErrRoutingRevisionConflict = apierrors.New(apierrors.CodeRoutingPolicyRevisionConflict)

// RoutingPolicyFilter carries the list filters (feature #45, §5).
type RoutingPolicyFilter struct {
	Search      string
	State       string
	Accelerator string
	Sort        string
	Offset      int
	Limit       int
}

// RoutingPolicyRepository persists routing policies, revisions and the
// publication outbox (feature #45, §4.1). All writes join the
// transaction opened by CompareAndSwapUpdate through the context.
type RoutingPolicyRepository struct {
	*database.BaseRepository[RoutingPolicy]
	db *database.Manager
}

// NewRoutingPolicyRepository constructs a RoutingPolicyRepository.
func NewRoutingPolicyRepository(db *gorm.DB) *RoutingPolicyRepository {
	mgr := database.NewManager(db)
	return &RoutingPolicyRepository{
		BaseRepository: database.NewBaseRepository[RoutingPolicy](mgr),
		db:             mgr,
	}
}

// clusterNameRow is a minimal read projection of the clusters table.
// The infer module cannot import the cluster package (the cluster
// module imports infer for its workload provider), so the health
// projection reads the name directly.
type clusterNameRow struct {
	ID   string `gorm:"primaryKey"`
	Name string
}

func (clusterNameRow) TableName() string { return "clusters" }

// GetCurrent returns the stored policy for a model, or nil when no row
// exists (default routing, feature #45, §4.2).
func (r *RoutingPolicyRepository) GetCurrent(ctx context.Context, modelID string) (*RoutingPolicy, error) {
	if _, err := uuid.Parse(modelID); err != nil {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}
	var row RoutingPolicy
	err := r.DB(ctx).Where("model_id = ?", modelID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListSummaries returns one page of model/policy summaries joined from
// the catalog, the policy table and the live service inventory
// (feature #45, §5). Models without a policy row report DEFAULT.
func (r *RoutingPolicyRepository) ListSummaries(ctx context.Context, filter RoutingPolicyFilter) ([]RoutingPolicySummaryRow, int64, error) {
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 20
	}
	// The ready-count subquery expression; reused by the state filter
	// and the row projection. The sort variant inlines the state
	// literal because ORDER BY cannot bind parameters portably.
	readyExpr := `(SELECT COUNT(*) FROM inference_services s
		JOIN model_versions v2 ON v2.model_id = m.id AND v2.is_active = true
		WHERE s.model_id = m.id AND s.model_version = v2.version
		  AND s.state = ?)`
	readySortExpr := `(SELECT COUNT(*) FROM inference_services s
		JOIN model_versions v2 ON v2.model_id = m.id AND v2.is_active = true
		WHERE s.model_id = m.id AND s.model_version = v2.version
		  AND s.state = '` + StateRunning + `')`

	sort := "m.name ASC"
	switch filter.Sort {
	case "ready", "-ready":
		sort = readySortExpr + " DESC, m.name ASC"
	case "updated":
		sort = "p.updated_at ASC NULLS FIRST, m.name ASC"
	case "-updated":
		// DESC puts NULLs first in Postgres, which would bury every
		// recently-updated policy under the 100+ never-configured
		// models; NULLS LAST keeps "most recently updated" on top.
		sort = "p.updated_at DESC NULLS LAST, m.name ASC"
	case "model", "-model", "":
	default:
		return nil, 0, apierrors.New(apierrors.CodeRoutingPolicyInvalid)
	}

	type joinRow struct {
		ModelID    string     `gorm:"column:model_id"`
		ModelName  string     `gorm:"column:model_name"`
		Version    string     `gorm:"column:version"`
		Enabled    bool       `gorm:"column:enabled"`
		ServiceIDs string     `gorm:"column:service_ids"`
		MaxAttempt int        `gorm:"column:max_attempts"`
		UpdatedAt  *time.Time `gorm:"column:policy_updated_at"`
	}

	query := r.DB(ctx).Table("models AS m").
		Select(`m.id AS model_id, m.name AS model_name,
			COALESCE(v.version, '') AS version,
			COALESCE(p.enabled, false) AS enabled,
			COALESCE(p.service_ids, '[]') AS service_ids,
			COALESCE(p.max_attempts, 1) AS max_attempts,
			p.updated_at AS policy_updated_at`).
		Joins("LEFT JOIN model_versions v ON v.model_id = m.id AND v.is_active = true").
		Joins("LEFT JOIN routing_policies p ON p.model_id = m.id")

	if filter.Search != "" {
		like := "%" + filter.Search + "%"
		query = query.Where("m.name LIKE ?", like)
	}
	if filter.Accelerator != "" {
		query = query.Where(
			"EXISTS (SELECT 1 FROM inference_services s WHERE s.model_id = m.id AND s.accelerator = ? AND s.state != ?)",
			filter.Accelerator, StateTerminated)
	}

	// State filter needs the ready count; the subquery counts the
	// running services of the model's active version.
	switch filter.State {
	case "":
	case RoutingPolicyStateDefault:
		query = query.Where("p.model_id IS NULL OR p.enabled = false")
	case RoutingPolicyStateEnabled:
		query = query.Where("p.enabled = true AND "+readyExpr+" > 0", StateRunning)
	case RoutingPolicyStateUnavailable:
		query = query.Where("p.enabled = true AND "+readyExpr+" = 0", StateRunning)
	default:
		return nil, 0, apierrors.New(apierrors.CodeRoutingPolicyInvalid)
	}

	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// The ready count is also needed for the ready sort and the row
	// projection; wrap the base query once.
	base := query.Session(&gorm.Session{})
	var rows []joinRow
	if err := base.
		Select(`m.id AS model_id, m.name AS model_name,
			COALESCE(v.version, '') AS version,
			COALESCE(p.enabled, false) AS enabled,
			COALESCE(p.service_ids, '[]') AS service_ids,
			COALESCE(p.max_attempts, 1) AS max_attempts,
			p.updated_at AS policy_updated_at,
			(`+readyExpr+`) AS ready_count`, StateRunning).
		Order(sort).
		Offset(filter.Offset).Limit(filter.Limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]RoutingPolicySummaryRow, 0, len(rows))
	for _, row := range rows {
		var ids []string
		_ = json.Unmarshal([]byte(row.ServiceIDs), &ids)
		summary := RoutingPolicySummaryRow{
			ModelID:      row.ModelID,
			ModelName:    row.ModelName,
			ModelVersion: row.Version,
			Enabled:      row.Enabled,
			TargetCount:  len(ids),
			ReadyCount:   0,
			MaxAttempts:  row.MaxAttempt,
			UpdatedAt:    row.UpdatedAt,
		}
		// Ready count: running services of the active version.
		var ready int64
		if err := r.DB(ctx).Table("inference_services AS s").
			Joins("JOIN model_versions AS v2 ON v2.model_id = s.model_id AND v2.is_active = ?", true).
			Where("s.model_id = ? AND s.model_version = v2.version AND s.state = ?", row.ModelID, StateRunning).
			Count(&ready).Error; err != nil {
			return nil, 0, err
		}
		summary.ReadyCount = int(ready)
		out = append(out, summary)
	}
	return out, total, nil
}

// RoutingPolicySummaryRow is the repository-level list projection.
type RoutingPolicySummaryRow struct {
	ModelID      string
	ModelName    string
	ModelVersion string
	Enabled      bool
	TargetCount  int
	ReadyCount   int
	MaxAttempts  int
	UpdatedAt    *time.Time
}

// EligibleService is one active service of the exact model+version
// (feature #45, §5.2).
type EligibleService struct {
	InferenceService
}

// ListEligibleServices returns the non-terminated services serving the
// exact model_id + model_version (feature #45, §5.2). The caller
// resolves the pinned version from the catalog.
func (r *RoutingPolicyRepository) ListEligibleServices(ctx context.Context, modelID, modelVersion string) ([]InferenceService, error) {
	var rows []InferenceService
	err := r.DB(ctx).
		Where("model_id = ? AND model_version = ? AND state != ?", modelID, modelVersion, StateTerminated).
		Order("created_at ASC").
		Find(&rows).Error
	return rows, err
}

// ListRevisions returns the newest revisions of a model's policy
// history, newest first (feature #45, §5.1).
func (r *RoutingPolicyRepository) ListRevisions(ctx context.Context, modelID string, offset, limit int) ([]RoutingPolicyRevision, int64, error) {
	if _, err := uuid.Parse(modelID); err != nil {
		return nil, 0, apierrors.New(apierrors.CodeModelNotFound)
	}
	if limit <= 0 || limit > 100 {
		limit = RoutingRevisionHistory
	}
	query := r.DB(ctx).Model(&RoutingPolicyRevision{}).Where("model_id = ?", modelID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []RoutingPolicyRevision
	err := query.Order("revision DESC").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, total, err
}

// RoutingPolicyChange is the validated new policy snapshot for
// CompareAndSwapUpdate.
type RoutingPolicyChange struct {
	ModelID      string
	ModelVersion string
	Enabled      bool
	ServiceIDs   []string
	MaxAttempts  int
	RetryOn      []string
	ExpectedRev  int64
	Actor        string
	Summary      string
	AuditEvent   func(ctx context.Context) any
}

// CompareAndSwapUpdate validates expected_revision and atomically
// writes the active row, the immutable revision row, the audit event
// and the outbox row in ONE transaction (feature #45, AD5). The
// auditFn runs inside the transaction so the audit event commits or
// rolls back with the revision.
func (r *RoutingPolicyRepository) CompareAndSwapUpdate(ctx context.Context, change RoutingPolicyChange, auditFn func(ctx context.Context) error) (*RoutingPolicy, *RoutingPolicyRevision, error) {
	var stored *RoutingPolicy
	var revision *RoutingPolicyRevision
	err := r.db.WithinTx(ctx, func(ctx context.Context) error {
		var current RoutingPolicy
		err := r.DB(ctx).Where("model_id = ?", change.ModelID).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			current = RoutingPolicy{ModelID: change.ModelID, Revision: 0}
		} else if err != nil {
			return err
		}
		if current.Revision != change.ExpectedRev {
			return ErrRoutingRevisionConflict
		}

		idsJSON, err := json.Marshal(change.ServiceIDs)
		if err != nil {
			return err
		}
		retryJSON, err := json.Marshal(change.RetryOn)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		newRev := current.Revision + 1

		// Before/after snapshots for the immutable revision row.
		before := routingSnapshotJSON(current)
		after := map[string]any{
			"model_id":      change.ModelID,
			"model_version": change.ModelVersion,
			"enabled":       change.Enabled,
			"service_ids":   change.ServiceIDs,
			"max_attempts":  change.MaxAttempts,
			"retry_on":      change.RetryOn,
			"revision":      newRev,
		}
		afterJSON, err := json.Marshal(after)
		if err != nil {
			return err
		}

		active := RoutingPolicy{
			ModelID:      change.ModelID,
			ModelVersion: change.ModelVersion,
			Enabled:      change.Enabled,
			ServiceIDs:   string(idsJSON),
			MaxAttempts:  change.MaxAttempts,
			RetryOn:      string(retryJSON),
			Revision:     newRev,
			UpdatedBy:    change.Actor,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if current.Revision > 0 {
			active.CreatedAt = current.CreatedAt
			if err := r.DB(ctx).Save(&active).Error; err != nil {
				return err
			}
		} else if err := r.DB(ctx).Create(&active).Error; err != nil {
			return err
		}

		revision = &RoutingPolicyRevision{
			ModelID:      change.ModelID,
			Revision:     newRev,
			ModelVersion: change.ModelVersion,
			Enabled:      change.Enabled,
			ServiceIDs:   string(idsJSON),
			MaxAttempts:  change.MaxAttempts,
			RetryOn:      string(retryJSON),
			Actor:        change.Actor,
			Summary:      change.Summary,
			BeforeJSON:   before,
			AfterJSON:    string(afterJSON),
			CreatedAt:    now,
		}
		if err := r.DB(ctx).Create(revision).Error; err != nil {
			return err
		}

		// The audit event joins the same transaction (feature #45,
		// §13.1): the audit repository resolves the tx from the ctx.
		if auditFn != nil {
			if err := auditFn(ctx); err != nil {
				return err
			}
		}

		// Durable publication intent; the publisher retries until the
		// MQ publish succeeds (feature #45, AD5).
		snapshot := map[string]any{
			"model_id":      change.ModelID,
			"model_version": change.ModelVersion,
			"enabled":       change.Enabled,
			"service_ids":   change.ServiceIDs,
			"max_attempts":  change.MaxAttempts,
			"retry_on":      change.RetryOn,
			"revision":      newRev,
			"published_at":  now.Format(time.RFC3339Nano),
		}
		snapshotJSON, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		outbox := &RoutingPolicyOutbox{
			ID:        uuid.NewString(),
			ModelID:   change.ModelID,
			Revision:  newRev,
			Snapshot:  string(snapshotJSON),
			CreatedAt: now,
		}
		if err := r.DB(ctx).Create(outbox).Error; err != nil {
			return err
		}

		// Keep only the newest RoutingRevisionHistory revisions
		// (feature #45, §4.1): trim the older rows in the same
		// transaction.
		if err := r.DB(ctx).
			Where("model_id = ? AND revision <= ?", change.ModelID, newRev-RoutingRevisionHistory).
			Delete(&RoutingPolicyRevision{}).Error; err != nil {
			return err
		}

		stored = &active
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return stored, revision, nil
}

// routingSnapshotJSON renders a stored policy row as the revision
// snapshot JSON.
func routingSnapshotJSON(row RoutingPolicy) string {
	var ids []string
	_ = json.Unmarshal([]byte(row.ServiceIDs), &ids)
	var retry []string
	_ = json.Unmarshal([]byte(row.RetryOn), &retry)
	out, _ := json.Marshal(map[string]any{
		"model_id":      row.ModelID,
		"model_version": row.ModelVersion,
		"enabled":       row.Enabled,
		"service_ids":   ids,
		"max_attempts":  row.MaxAttempts,
		"retry_on":      retry,
		"revision":      row.Revision,
	})
	return string(out)
}

// PublicationState returns pending when an unpublished outbox row
// exists for the model's current revision (feature #45, §5.1).
func (r *RoutingPolicyRepository) PublicationState(ctx context.Context, modelID string, revision int64) string {
	if revision <= 0 {
		return ""
	}
	var count int64
	err := r.DB(ctx).Model(&RoutingPolicyOutbox{}).
		Where("model_id = ? AND revision = ? AND published_at IS NULL", modelID, revision).
		Count(&count).Error
	if err != nil || count > 0 {
		return RoutingPublicationPending
	}
	return RoutingPublicationPublished
}

// ClaimPendingOutbox returns up to limit pending outbox rows in
// revision order per model (feature #45, §13.1).
func (r *RoutingPolicyRepository) ClaimPendingOutbox(ctx context.Context, limit int) ([]RoutingPolicyOutbox, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []RoutingPolicyOutbox
	err := r.DB(ctx).
		Where("published_at IS NULL").
		Order("created_at ASC").Order("revision ASC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

// MarkOutboxPublished records a successful MQ publish (idempotent).
func (r *RoutingPolicyRepository) MarkOutboxPublished(ctx context.Context, id string) error {
	now := time.Now().UTC()
	return r.DB(ctx).Model(&RoutingPolicyOutbox{}).
		Where("id = ?", id).
		Updates(map[string]any{"published_at": now, "last_error": ""}).Error
}

// RecordOutboxAttempt increments the attempt counter and stores the
// last error of a failed publish.
func (r *RoutingPolicyRepository) RecordOutboxAttempt(ctx context.Context, id string, attemptErr error) error {
	return r.DB(ctx).Model(&RoutingPolicyOutbox{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"attempts":   gorm.Expr("attempts + 1"),
			"last_error": fmt.Sprintf("%.200s", attemptErr.Error()),
		}).Error
}

// ClusterNames resolves the display names of the given cluster ids.
func (r *RoutingPolicyRepository) ClusterNames(ctx context.Context, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []clusterNameRow
	if err := r.DB(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = row.Name
	}
	return out, nil
}
