package tenancy

import "context"

// Monthly spend limits for the ladder consumed by the billing module's funds
// check: organization (on the account itself, read by billing), project,
// personal and project member. Every tier reads the same row the quota
// resolver reads, so a limit edited in one console applies to both the request
// gate and the spend gate.

// ProjectMonthlySpendLimitCents returns the project's monthly spend cap;
// 0 means unlimited and an unknown project reads as unlimited.
func (s *Service) ProjectMonthlySpendLimitCents(ctx context.Context, projectID string) (int64, error) {
	if projectID == "" {
		return 0, nil
	}
	repo, err := s.repository()
	if err != nil {
		return 0, err
	}
	limits, err := repo.ProjectQuotaLimits(ctx, projectID)
	if err != nil {
		return 0, err
	}
	return limits.MonthlySpendLimitCents, nil
}

// UserMonthlySpendLimitCents returns the user's monthly spend cap inside
// the organization; 0 means unlimited and a missing row reads as
// unlimited.
func (s *Service) UserMonthlySpendLimitCents(ctx context.Context, orgID, userID string) (int64, error) {
	if orgID == "" || userID == "" {
		return 0, nil
	}
	repo, err := s.repository()
	if err != nil {
		return 0, err
	}
	limits, err := repo.UserQuotaLimits(ctx, orgID, userID)
	if err != nil {
		return 0, err
	}
	return limits.MonthlySpendLimitCents, nil
}

// ProjectMemberMonthlySpendLimitCents returns one member's monthly spend cap
// inside a project; 0 means unlimited and a missing membership reads as
// unlimited.
func (s *Service) ProjectMemberMonthlySpendLimitCents(ctx context.Context, projectID, userID string) (int64, error) {
	if projectID == "" || userID == "" {
		return 0, nil
	}
	repo, err := s.repository()
	if err != nil {
		return 0, err
	}
	limits, err := repo.ProjectMemberQuotaLimits(ctx, projectID, userID)
	if err != nil {
		return 0, err
	}
	return limits.MonthlySpendLimitCents, nil
}
