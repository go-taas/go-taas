package evaluation

//revive:disable:exported

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Repository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

func (r *Repository) createSuite(ctx context.Context, row *Evaluation) error {
	row.EvaluationID = uuid.NewString()
	return r.db.WithContext(ctx).Create(row).Error
}

func (r *Repository) suite(ctx context.Context, orgID, id string) (*Evaluation, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrEvaluationNotFound
	}
	var row Evaluation
	err := r.db.WithContext(ctx).Where("evaluation_id = ? AND organization_id = ? AND deleted_at IS NULL", id, orgID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrEvaluationNotFound
	}
	return &row, err
}

func (r *Repository) listSuites(ctx context.Context, orgID, search string, offset, limit int) ([]Evaluation, int64, error) {
	query := r.db.WithContext(ctx).Model(&Evaluation{}).Where("organization_id = ? AND deleted_at IS NULL", orgID)
	if search != "" {
		query = query.Where("name LIKE ?", "%"+search+"%")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Evaluation
	err := query.Order("updated_at DESC").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, total, err
}

func (r *Repository) updateSuite(ctx context.Context, row *Evaluation) error {
	result := r.db.WithContext(ctx).Model(&Evaluation{}).
		Where("evaluation_id = ? AND organization_id = ? AND deleted_at IS NULL", row.EvaluationID, row.OrganizationID).
		Updates(map[string]any{"name": row.Name, "description": row.Description, "prompt_id": row.PromptID, "prompt_version": row.PromptVersion, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrEvaluationNotFound
	}
	return nil
}

func (r *Repository) reorderCases(ctx context.Context, orgID, suiteID string, ids []string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing []EvaluationCase
		if err := tx.Where("organization_id = ? AND evaluation_id = ?", orgID, suiteID).Order("position ASC").Find(&existing).Error; err != nil {
			return err
		}
		if len(existing) != len(ids) {
			return ErrEvaluationCaseInvalid
		}
		valid := make(map[string]bool, len(existing))
		for _, row := range existing {
			valid[row.CaseID] = true
		}
		seen := make(map[string]bool, len(ids))
		for _, id := range ids {
			if !valid[id] || seen[id] {
				return ErrEvaluationCaseInvalid
			}
			seen[id] = true
		}
		for position, id := range ids {
			if err := tx.Model(&EvaluationCase{}).Where("organization_id = ? AND evaluation_id = ? AND case_id = ?", orgID, suiteID, id).Update("position", position).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository) cases(ctx context.Context, orgID, suiteID string) ([]EvaluationCase, error) {
	var rows []EvaluationCase
	err := r.db.WithContext(ctx).Where("organization_id = ? AND evaluation_id = ?", orgID, suiteID).Order("position ASC").Find(&rows).Error
	return rows, err
}

func (r *Repository) addCase(ctx context.Context, row *EvaluationCase) error {
	row.CaseID = uuid.NewString()
	return r.db.WithContext(ctx).Create(row).Error
}

func (r *Repository) findCase(ctx context.Context, orgID, suiteID, caseID string) (*EvaluationCase, error) {
	if _, err := uuid.Parse(caseID); err != nil {
		return nil, ErrEvaluationNotFound
	}
	var row EvaluationCase
	err := r.db.WithContext(ctx).Where("organization_id = ? AND evaluation_id = ? AND case_id = ?", orgID, suiteID, caseID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrEvaluationNotFound
	}
	return &row, err
}

func (r *Repository) updateCase(ctx context.Context, row *EvaluationCase) error {
	return r.db.WithContext(ctx).Model(&EvaluationCase{}).Where("organization_id = ? AND evaluation_id = ? AND case_id = ?", row.OrganizationID, row.EvaluationID, row.CaseID).Updates(map[string]any{
		"name": row.Name, "variables": row.Variables, "expected_output": row.ExpectedOutput,
		"checks": row.Checks, "updated_at": time.Now().UTC(),
	}).Error
}

func (r *Repository) countCases(ctx context.Context, orgID, suiteID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&EvaluationCase{}).Where("organization_id = ? AND evaluation_id = ?", orgID, suiteID).Count(&count).Error
	return count, err
}

func (r *Repository) deleteCase(ctx context.Context, orgID, suiteID, caseID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row EvaluationCase
		result := tx.Where("organization_id = ? AND evaluation_id = ? AND case_id = ?", orgID, suiteID, caseID).First(&row)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return ErrEvaluationNotFound
		}
		if result.Error != nil {
			return result.Error
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
		return tx.Model(&EvaluationCase{}).Where("organization_id = ? AND evaluation_id = ? AND position > ?", orgID, suiteID, row.Position).Update("position", gorm.Expr("position - 1")).Error
	})
}

func (r *Repository) softDeleteSuite(ctx context.Context, orgID, id string) error {
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&Evaluation{}).Where("organization_id = ? AND evaluation_id = ? AND deleted_at IS NULL", orgID, id).Updates(map[string]any{"deleted_at": now, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrEvaluationNotFound
	}
	return nil
}

func (r *Repository) createRunSnapshot(ctx context.Context, run *EvaluationRun, cases []EvaluationRunCase, maxInFlight int) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&EvaluationRun{}).Where("organization_id = ? AND status IN ?", run.OrganizationID, []string{RunPending, RunRunning}).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(maxInFlight) {
			return ErrEvaluationLimitExceeded
		}
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		for i := range cases {
			cases[i].RunID = run.RunID
		}
		return tx.Create(&cases).Error
	})
}

func (r *Repository) listRuns(ctx context.Context, orgID, suiteID, status string, offset, limit int) ([]EvaluationRun, int64, error) {
	query := r.db.WithContext(ctx).Model(&EvaluationRun{}).Where("organization_id = ? AND evaluation_id = ?", orgID, suiteID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []EvaluationRun
	err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, total, err
}

func (r *Repository) run(ctx context.Context, orgID, suiteID, runID string) (*EvaluationRun, error) {
	var row EvaluationRun
	err := r.db.WithContext(ctx).Where("organization_id = ? AND evaluation_id = ? AND run_id = ?", orgID, suiteID, runID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrEvaluationRunNotFound
	}
	return &row, err
}

func (r *Repository) runCases(ctx context.Context, runID string) ([]EvaluationRunCase, error) {
	var rows []EvaluationRunCase
	err := r.db.WithContext(ctx).Where("run_id = ?", runID).Order("case_position ASC").Find(&rows).Error
	return rows, err
}

func (r *Repository) requestCancel(ctx context.Context, run *EvaluationRun) error {
	if run.Status != RunPending && run.Status != RunRunning {
		return ErrEvaluationRunStateInvalid
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&EvaluationRun{}).Where("run_id = ? AND organization_id = ? AND evaluation_id = ?", run.RunID, run.OrganizationID, run.EvaluationID).Update("cancel_requested_at", now).Error; err != nil {
			return err
		}
		return tx.Model(&EvaluationRunCase{}).Where("run_id = ? AND status = ?", run.RunID, CasePending).Updates(map[string]any{"status": CaseCancelled, "completed_at": now}).Error
	})
}

func (r *Repository) purgeExpired(ctx context.Context, now time.Time, limit int) (int64, error) {
	var rows []EvaluationRun
	if err := r.db.WithContext(ctx).Where("retained_until < ?", now).Limit(limit).Find(&rows).Error; err != nil {
		return 0, err
	}
	var removed int64
	for _, row := range rows {
		result := r.db.WithContext(ctx).Where("run_id = ?", row.RunID).Delete(&EvaluationRunCase{})
		if result.Error != nil {
			return removed, result.Error
		}
		result = r.db.WithContext(ctx).Where("run_id = ?", row.RunID).Delete(&EvaluationRun{})
		if result.Error != nil {
			return removed, result.Error
		}
		removed += result.RowsAffected
	}
	return removed, nil
}

// claimPendingRun atomically leases the oldest pending run to an owner
// (feature #44, AD5): the conditional UPDATE succeeds for exactly one
// caller, so multiple worker replicas never execute the same run. A
// run whose cancellation was requested before the claim is finalized
// as cancelled instead of executed.
func (r *Repository) claimPendingRun(ctx context.Context, owner string, leaseUntil time.Time) (*EvaluationRun, error) {
	var pending []EvaluationRun
	if err := r.db.WithContext(ctx).
		Where("status = ?", RunPending).
		Order("created_at ASC").Limit(8).Find(&pending).Error; err != nil {
		return nil, err
	}
	for i := range pending {
		run := pending[i]
		result := r.db.WithContext(ctx).Model(&EvaluationRun{}).
			Where("run_id = ? AND status = ?", run.RunID, RunPending).
			Updates(map[string]any{"status": RunRunning, "lease_owner": owner, "lease_until": leaseUntil, "started_at": time.Now().UTC()})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 1 {
			stored, err := r.runByID(ctx, run.RunID)
			if err != nil {
				return nil, err
			}
			return stored, nil
		}
	}
	return nil, nil
}

// runByID loads a run by id regardless of organization.
func (r *Repository) runByID(ctx context.Context, runID string) (*EvaluationRun, error) {
	var row EvaluationRun
	err := r.db.WithContext(ctx).Where("run_id = ?", runID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrEvaluationRunNotFound
	}
	return &row, err
}

// pendingRunCases returns the still-pending cases of a run in
// execution order.
func (r *Repository) pendingRunCases(ctx context.Context, runID string) ([]EvaluationRunCase, error) {
	var rows []EvaluationRunCase
	err := r.db.WithContext(ctx).Where("run_id = ? AND status = ?", runID, CasePending).Order("case_position ASC").Find(&rows).Error
	return rows, err
}

// saveCaseResult persists one immutable case result (feature #44,
// AD5): completion, check results, token usage, latency, cost and
// error state. The conditional UPDATE only lands on a case that is
// still pending, keeping results immutable once written.
func (r *Repository) saveCaseResult(ctx context.Context, c *EvaluationRunCase) error {
	result := r.db.WithContext(ctx).Model(&EvaluationRunCase{}).
		Where("result_id = ? AND status = ?", c.ResultID, CasePending).
		Updates(map[string]any{
			"status":        c.Status,
			"completion":    c.Completion,
			"check_results": c.CheckResults,
			"input_tokens":  c.InputTokens,
			"output_tokens": c.OutputTokens,
			"latency_ms":    c.LatencyMS,
			"cost_minor":    c.CostMinor,
			"currency":      c.Currency,
			"error_code":    c.ErrorCode,
			"error_message": c.ErrorMessage,
			"completed_at":  c.CompletedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrEvaluationRunStateInvalid
	}
	return nil
}

// finalizeRun writes the terminal state of a run (feature #44, AD5).
func (r *Repository) finalizeRun(ctx context.Context, runID, status string, completed, passed, failed, errored, cancelled int, totalCostMinor, medianLatencyMS int64, completedAt time.Time) error {
	return r.db.WithContext(ctx).Model(&EvaluationRun{}).
		Where("run_id = ?", runID).
		Updates(map[string]any{
			"status":            status,
			"completed_count":   completed,
			"passed_count":      passed,
			"failed_count":      failed,
			"error_count":       errored,
			"cancelled_count":   cancelled,
			"total_cost_minor":  totalCostMinor,
			"median_latency_ms": medianLatencyMS,
			"completed_at":      completedAt,
			"lease_owner":       "",
			"lease_until":       nil,
		}).Error
}

// cancelRequested reports whether cancellation was requested on a run.
func (r *Repository) cancelRequested(ctx context.Context, runID string) (bool, error) {
	var row EvaluationRun
	if err := r.db.WithContext(ctx).Select("cancel_requested_at").Where("run_id = ?", runID).First(&row).Error; err != nil {
		return false, err
	}
	return row.CancelRequestedAt != nil && !row.CancelRequestedAt.IsZero(), nil
}
