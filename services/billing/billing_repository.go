package billing

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Repository persists price entries, usage lines and charge records on
// top of the generic repository base. All reads and writes join an open
// transaction via the context.
type Repository struct {
	*database.BaseRepository[PriceEntry]
	db *database.Manager
	// accounts is the feature-#8 account/ledger repository.
	accounts *AccountRepository
}

// NewRepository constructs a Repository bound to a database Manager.
func NewRepository(db *gorm.DB) *Repository {
	mgr := database.NewManager(db)
	return &Repository{
		BaseRepository: database.NewBaseRepository[PriceEntry](mgr),
		db:             mgr,
		accounts:       NewAccountRepository(db),
	}
}

// Accounts exposes the account/ledger repository (feature #8).
func (r *Repository) Accounts() *AccountRepository { return r.accounts }

// UpsertPrice inserts or updates one price cell version in place: ON
// CONFLICT (model_id, accelerator_type, effective_from) DO UPDATE of
// the rates, tiers and updated_at. A repeated save with the same key
// returns the same price_id (FR1.1, AC1).
func (r *Repository) UpsertPrice(ctx context.Context, entry *PriceEntry) (*PriceEntry, error) {
	if entry.ID == "" {
		entry.ID = uuid.NewString()
	}
	err := r.DB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "model_id"},
			{Name: "accelerator_type"},
			{Name: "effective_from"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"input_price_per_million",
			"output_price_per_million",
			"cached_price_per_million",
			"currency",
			"tiers_json",
			"updated_at",
		}),
	}).Create(entry).Error
	if err != nil {
		return nil, apierrors.Wrap(apierrors.CodeInternal, err, "billing: price upsert failed")
	}
	// Re-select so an in-place update returns the stored row (the
	// original ID is preserved by the conflict target).
	var stored PriceEntry
	if err := r.DB(ctx).
		Where("model_id = ? AND accelerator_type = ? AND effective_from = ?",
			entry.ModelID, entry.AcceleratorType, entry.EffectiveFrom).
		First(&stored).Error; err != nil {
		return nil, apierrors.Wrap(apierrors.CodeInternal, err, "billing: price re-select failed")
	}
	return &stored, nil
}

// ApplicablePrice returns the price version in effect for (model, card)
// at the given unix time: the greatest effective_from <= at. nil when
// absent — the caller applies the D6 fallback chain (AC3).
func (r *Repository) ApplicablePrice(ctx context.Context, modelID, cardType string, at int64) (*PriceEntry, error) {
	var row PriceEntry
	err := r.DB(ctx).
		Where("model_id = ? AND accelerator_type = ? AND effective_from <= ?",
			modelID, cardType, at).
		Order("effective_from DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListPrices returns one page of price entries and the total count.
// The current view (default) returns, for each (model, card), the max
// effective_from <= now plus any scheduled versions (effective_from >
// now); the history view returns every version (FR2.2).
func (r *Repository) ListPrices(ctx context.Context, filter PriceFilter) ([]*PriceEntry, int64, error) {
	query := r.DB(ctx).Model(&PriceEntry{})
	if filter.ModelID != "" {
		query = query.Where("model_id = ?", filter.ModelID)
	}
	if filter.AcceleratorType != "" {
		query = query.Where("accelerator_type = ?", filter.AcceleratorType)
	}
	if !filter.IncludeHistory {
		// Current view: the latest effective version per cell plus
		// scheduled ones. Implemented as: effective_from <= now AND is
		// the max for its cell, OR effective_from > now.
		now := filter.Now
		if now == 0 {
			now = time.Now().Unix()
		}
		query = query.Where(
			"effective_from > ? OR (model_id, accelerator_type, effective_from) IN (?)",
			now,
			r.DB(ctx).Model(&PriceEntry{}).
				Select("model_id, accelerator_type, MAX(effective_from) AS effective_from").
				Where("effective_from <= ?", now).
				Group("model_id, accelerator_type"),
		)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*PriceEntry
	if err := query.
		Order("model_id ASC").Order("accelerator_type ASC").Order("effective_from DESC").
		Offset(filter.Offset).Limit(filter.Limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// IngestUsageLine inserts a usage line idempotently: INSERT with ON
// CONFLICT DO NOTHING on the request_id unique index; a duplicate
// delivery converges on the existing row (D5, AC4).
func (r *Repository) IngestUsageLine(ctx context.Context, line *UsageLine) (*UsageLine, error) {
	if line.ID == "" {
		line.ID = uuid.NewString()
	}
	result := r.DB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(line)
	if result.Error != nil {
		return nil, apierrors.Wrap(apierrors.CodeInternal, result.Error, "billing: usage line write failed")
	}
	if result.RowsAffected == 0 {
		// Duplicate request_id: converge on the existing row.
		var existing UsageLine
		if err := r.DB(ctx).Where("request_id = ?", line.RequestID).First(&existing).Error; err != nil {
			return nil, apierrors.Wrap(apierrors.CodeInternal, err, "billing: usage line re-select failed")
		}
		return &existing, nil
	}
	return line, nil
}

// UnchargedGroups returns the uncharged usage lines of one (api_key,
// hour) bucket grouped by (model, accelerator_type), with summed tokens
// and request counts — the charging scan (FR4.2).
func (r *Repository) UnchargedGroups(ctx context.Context, apiKeyID string, periodStart, periodEnd int64) ([]UsageGroup, error) {
	var rows []UsageGroup
	err := r.DB(ctx).Model(&UsageLine{}).
		Select("organization_id, api_key_id, model_id, accelerator_type, "+
			"COALESCE(SUM(prompt_tokens),0) AS prompt_tokens, "+
			"COALESCE(SUM(completion_tokens),0) AS completion_tokens, "+
			"COALESCE(SUM(cached_tokens),0) AS cached_tokens, "+
			"COALESCE(SUM(reasoning_tokens),0) AS reasoning_tokens, "+
			"COUNT(*) AS request_count").
		Where("api_key_id = ? AND completed_at >= ? AND completed_at < ? AND charged_charge_id IS NULL",
			apiKeyID, time.Unix(periodStart, 0).UTC(), time.Unix(periodEnd, 0).UTC()).
		Group("organization_id, api_key_id, model_id, accelerator_type").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].PeriodStart = periodStart
		rows[i].PeriodEnd = periodEnd
	}
	return rows, nil
}

// UnchargedHourBuckets returns the distinct (api_key, hour) buckets of
// uncharged usage lines completed before the cutoff — the
// reconciliation scan. Hour bucketing happens in Go (dialect-free).
func (r *Repository) UnchargedHourBuckets(ctx context.Context, eligibleBefore time.Time) ([]HourBucket, error) {
	var lines []UsageLine
	err := r.DB(ctx).
		Select("api_key_id", "organization_id", "completed_at").
		Where("charged_charge_id IS NULL AND completed_at < ?", eligibleBefore.UTC()).
		Find(&lines).Error
	if err != nil {
		return nil, err
	}
	buckets := map[HourBucket]struct{}{}
	for _, l := range lines {
		periodStart := l.CompletedAt.Unix() / 3600 * 3600
		buckets[HourBucket{
			APIKeyID:       l.APIKeyID,
			OrganizationID: l.OrganizationID,
			PeriodStart:    periodStart,
		}] = struct{}{}
	}
	out := make([]HourBucket, 0, len(buckets))
	for b := range buckets {
		out = append(out, b)
	}
	return out, nil
}

// ChargeGroup commits one charge in a single transaction: INSERT the
// charge record with ON CONFLICT DO NOTHING on the group-period unique
// index (a no-op when it exists — re-run safety, AC10), apply the
// feature-#8 account deduction when the charge is new and a spec is
// supplied (AD3), then mark the contributing usage lines inside the
// same transaction.
func (r *Repository) ChargeGroup(ctx context.Context, group UsageGroup, record *ChargeRecord, deduction *DeductionSpec) error {
	return r.db.WithinTx(ctx, func(txCtx context.Context) error {
		result := r.DB(txCtx).Clauses(clause.OnConflict{DoNothing: true}).Create(record)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// The charge already exists (a concurrent pass won): still
			// mark the lines so they converge.
			var existing ChargeRecord
			if err := r.DB(txCtx).
				Where("api_key_id = ? AND model_id = ? AND accelerator_type = ? AND period_start = ?",
					group.APIKeyID, group.ModelID, group.AcceleratorType, group.PeriodStart).
				First(&existing).Error; err != nil {
				return err
			}
			record = &existing
		} else if deduction != nil && r.accounts != nil {
			// New charge with an account: deduct inside this
			// transaction (AD3). A replayed charge skips the
			// deduction (idempotent, AC4).
			if err := r.accounts.ApplyDeduction(txCtx, *deduction); err != nil {
				return err
			}
		}
		return r.DB(txCtx).Model(&UsageLine{}).
			Where("api_key_id = ? AND model_id = ? AND accelerator_type = ? AND completed_at >= ? AND completed_at < ? AND charged_charge_id IS NULL",
				group.APIKeyID, group.ModelID, group.AcceleratorType,
				time.Unix(group.PeriodStart, 0).UTC(), time.Unix(group.PeriodEnd, 0).UTC()).
			Update("charged_charge_id", record.ID).Error
	})
}

// MonthToDateTokens returns the organization's cumulative charged token
// volume for (model, card) in the month containing before, excluding
// the current hour — the D3 tier-selection input.
func (r *Repository) MonthToDateTokens(ctx context.Context, orgID, modelID, cardType string, monthStart, before int64) (int64, error) {
	var agg struct {
		Total int64
	}
	err := r.DB(ctx).Model(&ChargeRecord{}).
		Select("COALESCE(SUM(prompt_tokens + completion_tokens + cached_tokens + reasoning_tokens),0) AS total").
		Where("organization_id = ? AND model_id = ? AND accelerator_type = ? AND period_start >= ? AND period_start < ?",
			orgID, modelID, cardType, monthStart, before).
		Scan(&agg).Error
	if err != nil {
		return 0, err
	}
	return agg.Total, nil
}

// ListCharges returns one page of charge records, newest period first,
// and the total count (FR5.1).
func (r *Repository) ListCharges(ctx context.Context, filter ChargeFilter) ([]*ChargeRecord, int64, error) {
	query := r.DB(ctx).Model(&ChargeRecord{}).
		Where("organization_id = ?", filter.OrganizationID)
	if filter.APIKeyID != "" {
		query = query.Where("api_key_id = ?", filter.APIKeyID)
	}
	if filter.ModelID != "" {
		query = query.Where("model_id = ?", filter.ModelID)
	}
	if filter.Since > 0 {
		query = query.Where("period_start >= ?", filter.Since)
	}
	if filter.Until > 0 {
		query = query.Where("period_start < ?", filter.Until)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*ChargeRecord
	if err := query.
		Order("period_start DESC").Order("id DESC").
		Offset(filter.Offset).Limit(filter.Limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// BillSummaries aggregates the organization's charge records into
// monthly bills computed on read (D9): month bucketing in Go over the
// org's records in range (dialect-free), bill_id = "{org}-{YYYYMM}".
func (r *Repository) BillSummaries(ctx context.Context, orgID string, since, until int64, offset, limit int) ([]*BillSummaryRow, int64, error) {
	var records []*ChargeRecord
	err := r.DB(ctx).
		Where("organization_id = ? AND period_start >= ? AND period_start < ?", orgID, since, until).
		Find(&records).Error
	if err != nil {
		return nil, 0, err
	}
	// Bucket by month start (UTC), deterministic order.
	byMonth := map[int64]*BillSummaryRow{}
	for _, rec := range records {
		monthStart := monthStartOf(time.Unix(rec.PeriodStart, 0).UTC())
		row, ok := byMonth[monthStart]
		if !ok {
			row = &BillSummaryRow{MonthStart: monthStart, Currency: rec.Currency}
			byMonth[monthStart] = row
		}
		row.Amount += rec.Amount
		row.ChargeCount++
		if !rec.Priced {
			row.UnpricedCount++
		}
	}
	all := make([]*BillSummaryRow, 0, len(byMonth))
	for _, row := range byMonth {
		all = append(all, row)
	}
	// Sort newest month first.
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].MonthStart > all[j-1].MonthStart; j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	total := int64(len(all))
	if offset > len(all) {
		offset = len(all)
	}
	end := offset + limit
	if limit <= 0 || end > len(all) {
		end = len(all)
	}
	return all[offset:end], total, nil
}

// monthStartOf truncates t to the first second of its UTC month.
func monthStartOf(t time.Time) int64 {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
}
