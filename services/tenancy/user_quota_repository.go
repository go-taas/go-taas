package tenancy

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// UserQuotaFilter narrows ListUserQuotas.
type UserQuotaFilter struct {
	OrganizationID string
	// Name filters by user id or display-name substring, case-insensitive.
	Name string
	// UserIDs restricts the result to a known roster; nil means all
	// members of the organization.
	UserIDs []string
	Offset  int
	Limit   int
}

// ListUserQuotas returns the organization's quota rows ordered by user
// id, plus the total count before pagination.
func (r *Repository) ListUserQuotas(ctx context.Context, filter UserQuotaFilter) ([]*UserQuota, int64, error) {
	query := r.db.DB(ctx).Model(&UserQuota{}).Where("organization_id = ?", filter.OrganizationID)
	if filter.Name != "" {
		// CAST keeps the filter portable: PostgreSQL has no lower(uuid).
		like := "%" + strings.ToLower(filter.Name) + "%"
		query = query.Where("LOWER(CAST(user_id AS TEXT)) LIKE ?", like)
	}
	if filter.UserIDs != nil {
		if len(filter.UserIDs) == 0 {
			return nil, 0, nil
		}
		query = query.Where("user_id IN ?", filter.UserIDs)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*UserQuota
	query = query.Order("user_id ASC")
	if filter.Offset > 0 {
		query = query.Offset(filter.Offset)
	}
	if filter.Limit > 0 {
		query = query.Limit(filter.Limit)
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// FindUserQuota returns one user's quota row, or (nil, nil) when no row
// exists — an absent row means every dimension is unlimited.
func (r *Repository) FindUserQuota(ctx context.Context, orgID, userID string) (*UserQuota, error) {
	var row UserQuota
	err := r.db.DB(ctx).
		Where("organization_id = ? AND user_id = ?", orgID, userID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// UpsertUserQuota writes one user's limits, inserting the row on first
// edit. It is the only write path for the personal tier.
func (r *Repository) UpsertUserQuota(ctx context.Context, row *UserQuota) error {
	return r.db.DB(ctx).Save(row).Error
}

// UserQuotaLimits reads one user's limits as a tuple. A missing row
// reads as unlimited (the zero tuple), never as an error: verdict
// construction must not fail on an unconfigured user.
func (r *Repository) UserQuotaLimits(ctx context.Context, orgID, userID string) (QuotaLimits, error) {
	row, err := r.FindUserQuota(ctx, orgID, userID)
	if err != nil {
		return QuotaLimits{}, err
	}
	return row.Limits(), nil
}

// UpdateUserQuota writes the changed columns plus the updated-by/at
// stamps. patch carries only the dimensions to change.
func (r *Repository) UpdateUserQuota(ctx context.Context, orgID, userID string, patch map[string]int64, actor string, now time.Time) error {
	updates := map[string]any{}
	for dim, val := range patch {
		updates[dim] = val
	}
	updates["quota_updated_by"] = actor
	updates["quota_updated_at"] = now
	return r.db.DB(ctx).Model(&UserQuota{}).
		Where("organization_id = ? AND user_id = ?", orgID, userID).
		Updates(updates).Error
}

// ProjectQuotaLimits reads one project's limits as a tuple. A missing
// project reads as unlimited.
func (r *Repository) ProjectQuotaLimits(ctx context.Context, projectID string) (QuotaLimits, error) {
	if projectID == "" {
		return QuotaLimits{}, nil
	}
	var row Project
	err := r.db.DB(ctx).Where("id = ?", projectID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return QuotaLimits{}, nil
	}
	if err != nil {
		return QuotaLimits{}, err
	}
	return QuotaLimits{
		MaxConcurrentRequests:     row.MaxConcurrentRequests,
		MaxBatchJobsPerDay:        row.MaxBatchJobsPerDay,
		MaxOutputTokensPerRequest: row.MaxOutputTokensPerRequest,
		MonthlySpendLimitCents:    row.MonthlySpendLimitCents,
	}, nil
}


// ProjectMemberQuotaLimits reads one member's limits inside a project as a
// tuple — the member tier of the quota ladder. A missing project, or a user
// who is not a member of it, reads as unlimited: the tier is optional and
// never an error.
func (r *Repository) ProjectMemberQuotaLimits(ctx context.Context, projectID, userID string) (QuotaLimits, error) {
	if projectID == "" || userID == "" {
		return QuotaLimits{}, nil
	}
	var row ProjectMember
	err := r.db.DB(ctx).
		Where("project_id = ? AND user_id = ?", projectID, userID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return QuotaLimits{}, nil
	}
	if err != nil {
		return QuotaLimits{}, err
	}
	return QuotaLimits{
		MaxConcurrentRequests:     row.MaxConcurrentRequests,
		MaxBatchJobsPerDay:        row.MaxBatchJobsPerDay,
		MaxOutputTokensPerRequest: row.MaxOutputTokensPerRequest,
		MonthlySpendLimitCents:    row.MonthlySpendLimitCents,
	}, nil
}
