package optimization

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
)

// Engine is the deterministic rule runner (AD6): one pass computes the
// four rules over the window, ranks the output, caps it per org and
// atomically swaps the snapshot. The mutex serializes passes — a
// concurrent trigger waits, it never overlaps.
type Engine struct {
	mu        sync.Mutex
	db        *gorm.DB
	cfg       config.OptimizationConfig
	charges   chargeReader
	requests  requestLogReader
	services  serviceReader
	catalog   catalogReader
	cardTypes CardTypeLister
}

// NewEngine builds the engine over the shared GORM handle with the
// gorm read repositories (AD10).
func NewEngine(db *gorm.DB, cfg config.OptimizationConfig, cardTypes CardTypeLister) *Engine {
	charges, requests, services, catalog := newGormReaders(db)
	return &Engine{
		db:        db,
		cfg:       cfg,
		charges:   charges,
		requests:  requests,
		services:  services,
		catalog:   catalog,
		cardTypes: cardTypes,
	}
}

// SetCardTypeLister wires the accelerator seam (AD14). Nil is safe: R3
// is skipped.
func (e *Engine) SetCardTypeLister(lister CardTypeLister) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cardTypes = lister
}

// Run executes one pass (AD6). It returns the emitted count. On error
// the previous snapshot is untouched — the swap is one transaction.
func (e *Engine) Run(ctx context.Context, trigger string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	started := time.Now().UTC()
	windowDays := e.cfg.WindowDays
	if windowDays <= 0 {
		windowDays = 1
	}
	rc := &ruleContext{
		windowStart: started.AddDate(0, 0, -windowDays),
		windowEnd:   started,
		cfg:         e.cfg,
		charges:     e.charges,
		requests:    e.requests,
		services:    e.services,
		catalog:     e.catalog,
		cardTypes:   e.cardTypes,
	}

	// The four rules; a rule-level error skips only that rule.
	entries := runRules(ctx, rc)

	// Rank: saving DESC, rule ASC, target_display ASC (FR2.1).
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].EstimatedMonthlySaving != entries[j].EstimatedMonthlySaving {
			return entries[i].EstimatedMonthlySaving > entries[j].EstimatedMonthlySaving
		}
		if entries[i].Rule != entries[j].Rule {
			return entries[i].Rule < entries[j].Rule
		}
		return entries[i].TargetDisplay < entries[j].TargetDisplay
	})

	// The per-org cap (AD5): keep the top N per organization.
	perOrg := map[string]int{}
	var capped []ComputedRecommendation
	for _, entry := range entries {
		if e.cfg.TopNPerOrg > 0 && perOrg[entry.OrganizationID] >= e.cfg.TopNPerOrg {
			continue
		}
		perOrg[entry.OrganizationID]++
		capped = append(capped, entry)
	}

	// The atomic swap (AD6): one transaction — upsert by fingerprint,
	// delete vanished, insert the run row.
	finished := time.Now().UTC()
	err := NewRepository(e.db).UpsertSnapshot(ctx, capped, e.cfg.Currency, started, finished, trigger)
	if err != nil {
		logger.S().Errorw("optimization: engine run failed, previous snapshot kept", "err", err, "trigger", trigger)
		return 0, err
	}
	logger.S().Infow("optimization: engine run complete",
		"rules", len(Rules), "emitted", len(capped), "trigger", trigger,
		"duration_ms", time.Since(started).Milliseconds())
	return len(capped), nil
}

// RunFailure records a failed run row (used by the runner when the
// pass errors before the swap; the transaction above already rolls
// back, so the log row is written separately).
func (e *Engine) RunFailure(ctx context.Context, trigger string, started time.Time, runErr error) {
	finished := time.Now().UTC()
	run := Run{
		ID:             uuid.NewString(),
		StartedAt:      started,
		FinishedAt:     finished,
		DurationMs:     finished.Sub(started).Milliseconds(),
		RulesEvaluated: len(Rules),
		Trigger:        trigger,
		Success:        false,
		Error:          truncate(runErr.Error(), maxRunErrorLen),
	}
	if err := e.db.WithContext(ctx).Create(&run).Error; err != nil {
		logger.S().Warnw("optimization: failed to record run failure", "err", err)
	}
}

// truncate bounds a string to n bytes.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// String renders the engine summary (diagnostics).
func (e *Engine) String() string {
	return fmt.Sprintf("optimization.Engine(window=%dd, topN=%d)", e.cfg.WindowDays, e.cfg.TopNPerOrg)
}
