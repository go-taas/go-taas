package tenancy

import "context"

// QuotaLimits is the shared five-dimension limit tuple used by every
// tier: organization, project and user. One tuple type keeps the
// combination rule and any future dimension in a single place.
//
// Every field is 0 when the tier sets no limit.
type QuotaLimits struct {
	MaxConcurrentRequests     int64
	MaxBatchJobsPerDay        int64
	MaxOutputTokensPerRequest int64
	MonthlySpendLimitCents    int64
}

// CombineLimits merges the tiers into the effective limits the platform
// enforces: per dimension, the smallest non-zero value. When every tier
// leaves a dimension unlimited (0), the result is 0 (unlimited).
//
// The rule lets each tier tighten the others but never loosen them: an
// organization budget of 10 requests cannot be raised to 20 by a
// project or personal budget of 20.
func CombineLimits(limits ...QuotaLimits) QuotaLimits {
	out := QuotaLimits{}
	for _, l := range limits {
		out.MaxConcurrentRequests = minNonZero(out.MaxConcurrentRequests, l.MaxConcurrentRequests)
		out.MaxBatchJobsPerDay = minNonZero(out.MaxBatchJobsPerDay, l.MaxBatchJobsPerDay)
		out.MaxOutputTokensPerRequest = minNonZero(out.MaxOutputTokensPerRequest, l.MaxOutputTokensPerRequest)
		out.MonthlySpendLimitCents = minNonZero(out.MonthlySpendLimitCents, l.MonthlySpendLimitCents)
	}
	return out
}

// minNonZero returns the smaller of two values, treating 0 as
// "unlimited" rather than as the smallest number.
func minNonZero(a, b int64) int64 {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}

// EffectiveQuota is the resolved tuple the data plane enforces. It is
// an alias so the wire projection, the resolver seam and the storage
// tuple stay one type.
type EffectiveQuota = QuotaLimits

// QuotaResolver resolves the effective limits for one API key's scope.
// It is implemented by the tenancy service and consumed by the auth
// module when it builds a gateway verdict.
type QuotaResolver interface {
	// EffectiveQuota returns the combined organization, project and
	// personal limits. An empty projectID or userID skips that tier.
	// A missing row reads as unlimited for that tier, never as an error.
	EffectiveQuota(ctx context.Context, orgID, projectID, userID string) (EffectiveQuota, error)
}
