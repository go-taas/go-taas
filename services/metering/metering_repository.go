package metering

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

// Repository persists vouchers and usage records on top of the generic
// repository base. All reads and writes join an open transaction via
// the context.
type Repository struct {
	*database.BaseRepository[Voucher]
	db *database.Manager
}

// NewRepository constructs a Repository bound to a database Manager.
func NewRepository(db *gorm.DB) *Repository {
	mgr := database.NewManager(db)
	return &Repository{
		BaseRepository: database.NewBaseRepository[Voucher](mgr),
		db:             mgr,
	}
}

// IngestVoucher inserts a voucher idempotently: INSERT with ON CONFLICT
// DO NOTHING on the request_id unique index; when the insert reports no
// rows affected (a duplicate delivery), the existing row is re-selected
// and returned. This is the idempotency primitive (D1, AC1).
// Infrastructure failures map to 10402.
func (r *Repository) IngestVoucher(ctx context.Context, v *Voucher) (*Voucher, error) {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	result := r.DB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(v)
	if result.Error != nil {
		return nil, apierrors.Wrap(apierrors.CodeMeteringVoucherError, result.Error, "metering: voucher write failed")
	}
	if result.RowsAffected == 0 {
		// Duplicate request_id: converge on the existing row.
		existing, err := r.FindByRequestID(ctx, v.RequestID)
		if err != nil {
			return nil, err
		}
		return existing, nil
	}
	return v, nil
}

// FindByRequestID returns the voucher with the given request id.
func (r *Repository) FindByRequestID(ctx context.Context, requestID string) (*Voucher, error) {
	var row Voucher
	err := r.DB(ctx).Where("request_id = ?", requestID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeMeteringVoucherNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// FindByID returns the voucher with the given id. A miss maps to 10403.
func (r *Repository) FindByID(ctx context.Context, id string) (*Voucher, error) {
	var row Voucher
	err := r.DB(ctx).First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeMeteringVoucherNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListVouchers returns one page of vouchers, newest completion first,
// and the total count. Empty filter values match everything (FR4.2).
func (r *Repository) ListVouchers(ctx context.Context, filter VoucherFilter) ([]*Voucher, int64, error) {
	query := r.applyVoucherFilters(r.DB(ctx).Model(&Voucher{}), filter)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*Voucher
	if err := query.
		Order("completed_at DESC").Order("id DESC").
		Offset(filter.Offset).Limit(filter.Limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// applyVoucherFilters adds the org/key/model/range conditions shared by
// count and page queries.
func (r *Repository) applyVoucherFilters(query *gorm.DB, filter VoucherFilter) *gorm.DB {
	query = query.Where("organization_id = ?", filter.OrganizationID)
	if filter.APIKeyID != "" {
		query = query.Where("api_key_id = ?", filter.APIKeyID)
	}
	if filter.ModelID != "" {
		query = query.Where("model_id = ?", filter.ModelID)
	}
	if filter.Since > 0 {
		query = query.Where("completed_at >= ?", time.Unix(filter.Since, 0).UTC())
	}
	if filter.Until > 0 {
		query = query.Where("completed_at < ?", time.Unix(filter.Until, 0).UTC())
	}
	return query
}

// UnsettledVouchers returns every voucher not yet linked to a usage
// record, completed before the cutoff. The settlement runner fetches
// them once per pass and groups into hour buckets in Go (dialect-free
// bucketing, Section 10.2).
func (r *Repository) UnsettledVouchers(ctx context.Context, completedBefore time.Time) ([]*Voucher, error) {
	var rows []*Voucher
	err := r.DB(ctx).
		Where("settled_usage_record_id IS NULL AND completed_at < ?", completedBefore.UTC()).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// SettleBucket settles one (api_key_id, hour) bucket in a single
// transaction: aggregate the unsettled vouchers' token sums, INSERT the
// usage record with ON CONFLICT DO NOTHING on (api_key_id,
// period_start) — a no-op when the record already exists (re-run
// safety, AC5) — then mark the contributing vouchers. The record is
// re-selected on conflict so the caller always gets the committed row
// (AC4).
func (r *Repository) SettleBucket(ctx context.Context, bucket HourBucket, now time.Time) (*UsageRecord, error) {
	var record *UsageRecord
	err := r.db.WithinTx(ctx, func(txCtx context.Context) error {
		start := time.Unix(bucket.PeriodStart, 0).UTC()
		end := start.Add(time.Hour)

		// Aggregate the bucket's unsettled vouchers.
		var agg struct {
			Prompt     int64
			Completion int64
			Cached     int64
			Reasoning  int64
			Count      int64
		}
		if err := r.DB(txCtx).Model(&Voucher{}).
			Select("COALESCE(SUM(prompt_tokens),0) AS prompt, COALESCE(SUM(completion_tokens),0) AS completion, "+
				"COALESCE(SUM(cached_tokens),0) AS cached, COALESCE(SUM(reasoning_tokens),0) AS reasoning, "+
				"COUNT(*) AS count").
			Where("api_key_id = ? AND completed_at >= ? AND completed_at < ? AND settled_usage_record_id IS NULL",
				bucket.APIKeyID, start, end).
			Scan(&agg).Error; err != nil {
			return err
		}
		if agg.Count == 0 {
			// Nothing left to settle (a concurrent pass won).
			return nil
		}

		// Insert the usage record; a conflict means the hour was
		// already settled — keep the existing row.
		record = &UsageRecord{
			ID:               uuid.NewString(),
			OrganizationID:   bucket.OrganizationID,
			APIKeyID:         bucket.APIKeyID,
			PeriodStart:      bucket.PeriodStart,
			PeriodEnd:        bucket.PeriodStart + 3600,
			PromptTokens:     agg.Prompt,
			CompletionTokens: agg.Completion,
			CachedTokens:     agg.Cached,
			ReasoningTokens:  agg.Reasoning,
			RequestCount:     agg.Count,
			SettledAt:        now.UTC(),
		}
		result := r.DB(txCtx).Clauses(clause.OnConflict{DoNothing: true}).Create(record)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// The record already exists: re-select it so the caller
			// sees the committed row.
			var existing UsageRecord
			if err := r.DB(txCtx).
				Where("api_key_id = ? AND period_start = ?", bucket.APIKeyID, bucket.PeriodStart).
				First(&existing).Error; err != nil {
				return err
			}
			record = &existing
		}

		// Mark the contributing vouchers inside the same transaction.
		return r.DB(txCtx).Model(&Voucher{}).
			Where("api_key_id = ? AND completed_at >= ? AND completed_at < ? AND settled_usage_record_id IS NULL",
				bucket.APIKeyID, start, end).
			Update("settled_usage_record_id", record.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return record, nil
}

// ListUsageRecords returns one page of settled usage records, newest
// period first, and the total count (FR4.4).
func (r *Repository) ListUsageRecords(ctx context.Context, filter UsageRecordFilter) ([]*UsageRecord, int64, error) {
	query := r.DB(ctx).Model(&UsageRecord{}).
		Where("organization_id = ?", filter.OrganizationID)
	if filter.APIKeyID != "" {
		query = query.Where("api_key_id = ?", filter.APIKeyID)
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
	var rows []*UsageRecord
	if err := query.
		Order("period_start DESC").
		Offset(filter.Offset).Limit(filter.Limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// UsageSummary aggregates usage for one organization and range, grouped
// by api key (default) or model. The settled part reads usage_records;
// the pending part aggregates unsettled vouchers; both are merged in Go
// with the settled/pending hour split (FR4.1, AC8).
func (r *Repository) UsageSummary(ctx context.Context, orgID string, since, until int64, groupByModel bool) ([]*UsageSummaryRow, error) {
	rows := map[string]*UsageSummaryRow{}
	getRow := func(key string) *UsageSummaryRow {
		if row, ok := rows[key]; ok {
			return row
		}
		row := &UsageSummaryRow{GroupKey: key}
		rows[key] = row
		return row
	}

	// Settled part: usage records overlapping the range.
	groupCol := "api_key_id"
	if groupByModel {
		groupCol = "model_id"
	}
	if !groupByModel {
		var settled []struct {
			GroupKey     string
			Prompt       int64
			Completion   int64
			Cached       int64
			Reasoning    int64
			RequestCount int64
		}
		q := r.DB(ctx).Model(&UsageRecord{}).
			Select("api_key_id AS group_key, SUM(prompt_tokens) AS prompt, SUM(completion_tokens) AS completion, " +
				"SUM(cached_tokens) AS cached, SUM(reasoning_tokens) AS reasoning, SUM(request_count) AS request_count")
		q = applyRecordRange(q, since, until)
		if err := q.Group("api_key_id").Scan(&settled).Error; err != nil {
			return nil, err
		}
		for _, s := range settled {
			row := getRow(s.GroupKey)
			row.PromptTokens += s.Prompt
			row.CompletionTokens += s.Completion
			row.CachedTokens += s.Cached
			row.ReasoningTokens += s.Reasoning
			row.RequestCount += s.RequestCount
		}
		// Settled hours: distinct period_start per key.
		var hours []struct {
			GroupKey string
			Hours    int64
		}
		qh := r.DB(ctx).Model(&UsageRecord{}).
			Select("api_key_id AS group_key, COUNT(DISTINCT period_start) AS hours")
		qh = applyRecordRange(qh, since, until)
		if err := qh.Group("api_key_id").Scan(&hours).Error; err != nil {
			return nil, err
		}
		for _, h := range hours {
			getRow(h.GroupKey).SettledHours += h.Hours
		}
	} else {
		// Grouping by model cannot use usage_records (they carry no
		// model dimension): settled vouchers are aggregated instead —
		// the settled flag on each voucher identifies its bucket.
		var settled []struct {
			GroupKey   string
			Prompt     int64
			Completion int64
			Cached     int64
			Reasoning  int64
			Count      int64
		}
		q := r.DB(ctx).Model(&Voucher{}).
			Select("model_id AS group_key, SUM(prompt_tokens) AS prompt, SUM(completion_tokens) AS completion, " +
				"SUM(cached_tokens) AS cached, SUM(reasoning_tokens) AS reasoning, COUNT(*) AS count")
		q = applyVoucherRangeOrg(q, orgID, since, until)
		if err := q.Where("settled_usage_record_id IS NOT NULL").Group("model_id").Scan(&settled).Error; err != nil {
			return nil, err
		}
		for _, s := range settled {
			row := getRow(s.GroupKey)
			row.PromptTokens += s.Prompt
			row.CompletionTokens += s.Completion
			row.CachedTokens += s.Cached
			row.ReasoningTokens += s.Reasoning
			row.RequestCount += s.Count
		}
	}

	// Pending part: unsettled vouchers in range, grouped the same way.
	var pending []struct {
		GroupKey   string
		Prompt     int64
		Completion int64
		Cached     int64
		Reasoning  int64
		Count      int64
	}
	pq := r.DB(ctx).Model(&Voucher{}).
		Select(groupCol + " AS group_key, SUM(prompt_tokens) AS prompt, SUM(completion_tokens) AS completion, " +
			"SUM(cached_tokens) AS cached, SUM(reasoning_tokens) AS reasoning, COUNT(*) AS count")
	pq = applyVoucherRangeOrg(pq, orgID, since, until)
	if err := pq.Where("settled_usage_record_id IS NULL").Group(groupCol).Scan(&pending).Error; err != nil {
		return nil, err
	}
	for _, p := range pending {
		row := getRow(p.GroupKey)
		row.PromptTokens += p.Prompt
		row.CompletionTokens += p.Completion
		row.CachedTokens += p.Cached
		row.ReasoningTokens += p.Reasoning
		row.RequestCount += p.Count
	}

	// Pending hours: distinct hour buckets of unsettled vouchers per
	// group. Hour bucketing happens in Go (dialect-free).
	var pendingVouchers []Voucher
	pvq := r.DB(ctx).Model(&Voucher{})
	pvq = applyVoucherRangeOrg(pvq, orgID, since, until)
	if err := pvq.Where("settled_usage_record_id IS NULL").Find(&pendingVouchers).Error; err != nil {
		return nil, err
	}
	pendingHours := map[string]map[int64]struct{}{}
	for i := range pendingVouchers {
		v := &pendingVouchers[i]
		key := v.APIKeyID
		if groupByModel {
			key = v.ModelID
		}
		bucket := v.CompletedAt.Unix() / 3600 * 3600
		if pendingHours[key] == nil {
			pendingHours[key] = map[int64]struct{}{}
		}
		pendingHours[key][bucket] = struct{}{}
	}
	for key, buckets := range pendingHours {
		getRow(key).PendingHours += int64(len(buckets))
	}

	out := make([]*UsageSummaryRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	return out, nil
}

// applyRecordRange adds the period_start range conditions to a
// usage_records query.
func applyRecordRange(q *gorm.DB, since, until int64) *gorm.DB {
	if since > 0 {
		q = q.Where("period_start >= ?", since)
	}
	if until > 0 {
		q = q.Where("period_start < ?", until)
	}
	return q
}

// applyVoucherRangeOrg adds the organization and completed_at range
// conditions to a vouchers query.
func applyVoucherRangeOrg(q *gorm.DB, orgID string, since, until int64) *gorm.DB {
	q = q.Where("organization_id = ?", orgID)
	if since > 0 {
		q = q.Where("completed_at >= ?", time.Unix(since, 0).UTC())
	}
	if until > 0 {
		q = q.Where("completed_at < ?", time.Unix(until, 0).UTC())
	}
	return q
}

// UnpublishedRecords returns usage records whose settlement event has
// not been published yet (the publish-retry scan, FR3.4).
func (r *Repository) UnpublishedRecords(ctx context.Context) ([]*UsageRecord, error) {
	var rows []*UsageRecord
	err := r.DB(ctx).Where("settlement_event_published = ?", false).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// MarkPublished flags a usage record's settlement event as published.
func (r *Repository) MarkPublished(ctx context.Context, id string) error {
	return r.DB(ctx).Model(&UsageRecord{}).
		Where("id = ?", id).
		Update("settlement_event_published", true).Error
}

// DeleteSettledBefore deletes up to batch settled vouchers created
// before the cutoff, returning the deleted count. The portable form
// (select ids then delete by ids) bounds each statement on both SQLite
// and Postgres (FR5.1).
func (r *Repository) DeleteSettledBefore(ctx context.Context, before time.Time, batch int) (int64, error) {
	if batch <= 0 {
		batch = 1
	}
	var ids []string
	if err := r.DB(ctx).Model(&Voucher{}).
		Where("settled_usage_record_id IS NOT NULL AND created_at < ?", before.UTC()).
		Limit(batch).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := r.DB(ctx).Where("id IN ?", ids).Delete(&Voucher{})
	return result.RowsAffected, result.Error
}

// CountUnsettledOlderThan counts unsettled vouchers older than the
// cutoff — the FR5.3 warning hook (they are kept, not deleted).
func (r *Repository) CountUnsettledOlderThan(ctx context.Context, before time.Time) (int64, error) {
	var count int64
	err := r.DB(ctx).Model(&Voucher{}).
		Where("settled_usage_record_id IS NULL AND created_at < ?", before.UTC()).
		Count(&count).Error
	if err != nil {
		return 0, err
	}
	return count, nil
}
