package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// QuotaListFilter narrows ListOrganizationQuotas (feature #51, FR1.2).
type QuotaListFilter struct {
	Name       string // display-name/id substring search, case-insensitive
	LimitState string // "" | "limited" | "unlimited"
	Offset     int
	Limit      int
}

// QuotaChange is one flattened dimension change read from an audit
// event's metadata (feature #51, AD16).
type QuotaChange struct {
	ChangedAt time.Time
	Operator  string
	Dimension string
	OldValue  int64
	NewValue  int64
}

// quotaChangeMetadata is the audit event metadata shape written by
// UpdateOrganizationQuota (feature #51, AD16).
type quotaChangeMetadata struct {
	Changes []quotaChangeEntry `json:"changes"`
}

type quotaChangeEntry struct {
	Dimension string `json:"dimension"`
	Old       int64  `json:"old"`
	New       int64  `json:"new"`
}

// ListOrganizationQuotas returns the per-org quota rows matching the
// filter, ordered by display name, paginated with a total count
// (feature #51, FR1.2).
func (r *Repository) ListOrganizationQuotas(ctx context.Context, filter QuotaListFilter) ([]*Organization, int64, error) {
	query := r.db.DB(ctx).Model(&Organization{})
	if filter.Name != "" {
		like := "%" + strings.ToLower(filter.Name) + "%"
		query = query.Where("LOWER(display_name) LIKE ? OR LOWER(id) LIKE ?", like, like)
	}
	switch filter.LimitState {
	case "limited":
		query = query.Where("max_concurrent_requests > 0 OR max_inference_services > 0 OR max_batch_jobs_per_day > 0 OR max_output_tokens_per_request > 0")
	case "unlimited":
		query = query.Where("max_concurrent_requests = 0 AND max_inference_services = 0 AND max_batch_jobs_per_day = 0 AND max_output_tokens_per_request = 0")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*Organization
	if err := query.Order("display_name ASC").Offset(filter.Offset).Limit(filter.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// GetOrganizationQuota returns one org's quota row. gorm.ErrRecordNotFound
// passes through; the caller maps it to CodeQuotaNotFound.
func (r *Repository) GetOrganizationQuota(ctx context.Context, orgID string) (*Organization, error) {
	var row Organization
	err := r.db.DB(ctx).Where("id = ?", orgID).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// UpdateOrganizationQuota writes the changed quota columns plus the
// updated-by/at stamps (feature #51, FR1.4). patch carries only the
// dimensions to change (nil = unchanged).
func (r *Repository) UpdateOrganizationQuota(ctx context.Context, orgID string, patch map[string]int64, actor string, now time.Time) error {
	updates := map[string]any{}
	for dim, val := range patch {
		updates[dim] = val
	}
	updates["quota_updated_by"] = actor
	updates["quota_updated_at"] = now
	return r.db.DB(ctx).Model(&Organization{}).Where("id = ?", orgID).Updates(updates).Error
}

// CountActiveServicesByOrg counts the org's non-terminated inference
// services (feature #51, §4.2). It is a display read on the shared
// handle; the authoritative enforcement count lives in infer.
func (r *Repository) CountActiveServicesByOrg(ctx context.Context, orgID string) (int64, error) {
	var count int64
	err := r.db.DB(ctx).Table("inference_services").
		Where("organization_id = ? AND state <> ?", orgID, "terminated").
		Count(&count).Error
	return count, err
}

// CountBatchJobsSince counts the org's batch jobs created at or after
// since (feature #51, AD15). Canceled and failed jobs count — the guard
// is on submissions, not successes.
func (r *Repository) CountBatchJobsSince(ctx context.Context, orgID string, since time.Time) (int64, error) {
	var count int64
	err := r.db.DB(ctx).Table("batch_jobs").
		Where("organization_id = ? AND created_at >= ?", orgID, since).
		Count(&count).Error
	return count, err
}

// ListQuotaChanges returns the org's newest quota.updated change
// entries, flattened from the audit trail, newest first, capped at
// limit (feature #51, AD16).
func (r *Repository) ListQuotaChanges(ctx context.Context, orgID string, limit int) ([]QuotaChange, error) {
	type auditRow struct {
		CreatedAt   time.Time
		ActorUserID string
		Metadata    string
	}
	var rows []auditRow
	err := r.db.DB(ctx).Table("audit_events").
		Where("organization_id = ? AND action = ?", orgID, "quota.updated").
		Order("created_at DESC").
		Limit(limit * 4). // a single event may carry up to 4 dimension changes
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	var out []QuotaChange
	for _, row := range rows {
		var meta quotaChangeMetadata
		if err := json.Unmarshal([]byte(row.Metadata), &meta); err != nil {
			continue
		}
		for _, ch := range meta.Changes {
			out = append(out, QuotaChange{
				ChangedAt: row.CreatedAt,
				Operator:  row.ActorUserID,
				Dimension: ch.Dimension,
				OldValue:  ch.Old,
				NewValue:  ch.New,
			})
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

// OrgQuota implements OrgQuotaReader: the four maxima off the org row.
// An unknown org returns the zero struct (all unlimited), never an
// error — a missing org during verdict construction must read as
// unlimited, never fail auth (AD12's fail-open rule).
func (r *Repository) OrgQuota(ctx context.Context, orgID string) (OrgQuota, error) {
	var row Organization
	err := r.db.DB(ctx).Where("id = ?", orgID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return OrgQuota{}, nil
	}
	if err != nil {
		return OrgQuota{}, err
	}
	return OrgQuota{
		MaxConcurrentRequests:     row.MaxConcurrentRequests,
		MaxInferenceServices:      row.MaxInferenceServices,
		MaxBatchJobsPerDay:        row.MaxBatchJobsPerDay,
		MaxOutputTokensPerRequest: row.MaxOutputTokensPerRequest,
	}, nil
}
