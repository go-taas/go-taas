package tenancy

import "time"

// UserQuota is one user's limits inside an organization — the personal
// tier of the three-level quota model (organization, project, user).
// The composite (organization_id, user_id) is the natural key: a person
// carries one budget per tenant.
//
// Every limit is per dimension with 0 = unlimited. The data plane
// enforces the minimum of the non-zero organization, project and
// personal values (see CombineLimits), so a personal limit can tighten
// but never loosen the tenant budget.
type UserQuota struct {
	// OrganizationID is the tenant the budget belongs to.
	OrganizationID string `gorm:"size:64;not null;primaryKey"`
	// UserID is the platform user the budget follows.
	UserID string `gorm:"type:uuid;not null;primaryKey"`
	// MaxConcurrentRequests caps the user's in-flight requests.
	MaxConcurrentRequests int64 `gorm:"not null;default:0"`
	// MaxBatchJobsPerDay caps the batch jobs the user submits per day.
	MaxBatchJobsPerDay int64 `gorm:"not null;default:0"`
	// MaxOutputTokensPerRequest caps one request's output tokens.
	MaxOutputTokensPerRequest int64 `gorm:"not null;default:0"`
	// MonthlySpendLimitCents caps the user's API-key spend per UTC month;
	// 0 = unlimited.
	MonthlySpendLimitCents int64 `gorm:"not null;default:0"`
	// QuotaUpdatedBy is the acting administrator's user id.
	QuotaUpdatedBy string `gorm:"size:64;not null;default:''"`
	// QuotaUpdatedAt is the last edit time (UTC).
	QuotaUpdatedAt time.Time `gorm:"default:null"`
}

// TableName returns the user_quotas table name.
func (UserQuota) TableName() string { return "user_quotas" }

// Limits projects the row onto the dimension tuple the resolver
// combines.
func (q *UserQuota) Limits() QuotaLimits {
	if q == nil {
		return QuotaLimits{}
	}
	return QuotaLimits{
		MaxConcurrentRequests:     q.MaxConcurrentRequests,
		MaxBatchJobsPerDay:        q.MaxBatchJobsPerDay,
		MaxOutputTokensPerRequest: q.MaxOutputTokensPerRequest,
		MonthlySpendLimitCents:    q.MonthlySpendLimitCents,
	}
}
