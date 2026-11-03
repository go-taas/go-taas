package tenancy

import (
	"context"

	"github.com/go-taas/go-taas/pkg/config"
)

// SpendReader reports settled spend, so the quota surfaces can show a budget
// next to what has been consumed. It is implemented by the billing module and
// injected at wiring time: tenancy owns the budgets, billing owns the ledger,
// and neither imports the other.
//
// Every read is scoped to the current UTC cycle, which is the same window the
// spend gate compares against — a console that showed "spent this month" from a
// different window than the gate would explain the wrong number.
type SpendReader interface {
	// SpentCents returns the settled charge amount for a scope. An empty
	// project skips the project condition; an empty owner skips the owner
	// condition; both empty reads the whole organization.
	SpentCents(ctx context.Context, orgID, projectID, ownerUserID string) (int64, error)
}

// SetSpendReader injects the spend reader used to show budgets next to their
// consumption. Nil until wired: spent and remaining are reported as 0.
func (s *Service) SetSpendReader(r SpendReader) { s.spendReader = r }

// spentCents reads one scope's cycle spend, degrading to 0 on a failure: these
// figures are display data attached to a response the control plane already
// produced, and a ledger read failure must not fail a quota read.
func (s *Service) spentCents(ctx context.Context, orgID, projectID, ownerUserID string) int64 {
	if s.spendReader == nil || orgID == "" {
		return 0
	}
	spent, err := s.spendReader.SpentCents(ctx, orgID, projectID, ownerUserID)
	if err != nil {
		return 0
	}
	return spent
}

// remainingCents is the budget left for a cap: 0 when the cap is unlimited
// (0), never negative.
func remainingCents(limitCents, spentCents int64) int64 {
	if limitCents <= 0 {
		return 0
	}
	remaining := limitCents - spentCents
	if remaining < 0 {
		return 0
	}
	return remaining
}

// billingCurrency returns the platform billing currency for display, so a
// budget amount is labelled without the console guessing.
func billingCurrency() string {
	if cfg := config.GetConfig(); cfg != nil && cfg.Billing.Currency != "" {
		return cfg.Billing.Currency
	}
	return "CNY"
}
