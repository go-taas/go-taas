package billing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/config"
)

// AC8: ResetOnce applies the guarded UPDATE for the current month
// start; a second pass is a no-op.
func TestCycleResetRunnerResetOnce(t *testing.T) {
	repo, db := newAccountRepo(t)

	// A postpaid account aged into a previous cycle.
	seedAccount(t, repo, "org-1", AccountModePostpaid, 0, 5000, 2500)
	past := monthStartOf(time.Now().UTC().AddDate(0, -1, 0))
	require.NoError(t, db.Exec("UPDATE accounts SET cycle_started_at = ? WHERE organization_id = ?",
		past, "org-1").Error)

	runner := NewCycleResetRunner(repo, time.Minute)
	require.NoError(t, runner.ResetOnce(context.Background()))

	var used int64
	require.NoError(t, db.Raw("SELECT used_this_cycle_cents FROM accounts WHERE organization_id = ?", "org-1").
		Scan(&used).Error)
	assert.Equal(t, int64(0), used, "usage zeroed after the reset pass")

	// Idempotent second pass affects zero rows.
	count, err := repo.ResetCycle(context.Background(), monthStartOf(time.Now().UTC()))
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// Run exits cleanly on context cancellation.
func TestCycleResetRunnerRunStops(t *testing.T) {
	repo, _ := newAccountRepo(t)
	runner := NewCycleResetRunner(repo, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, runner.Run(ctx))
}

// NewCycleResetRunnerRunner returns nil when disabled or components
// are missing.
func TestNewCycleResetRunnerRunnerDisabled(t *testing.T) {
	assert.Nil(t, NewCycleResetRunnerRunner(nil))

	cfg := &config.Configuration{}
	cfg.Billing.CycleReset.Enabled = false
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })
	assert.Nil(t, NewCycleResetRunnerRunner(nil))
}
