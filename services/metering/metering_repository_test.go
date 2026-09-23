package metering

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func newMeteringTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Voucher{}, &UsageRecord{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func newMeteringTestRepo(t *testing.T) *Repository {
	t.Helper()
	return NewRepository(newMeteringTestDB(t))
}

// mustIngest inserts a voucher directly and fails the test on error.
func mustIngest(ctx context.Context, t *testing.T, repo *Repository, v *Voucher) *Voucher {
	t.Helper()
	stored, err := repo.IngestVoucher(ctx, v)
	require.NoError(t, err)
	return stored
}

func testVoucher(requestID, orgID, keyID, modelID string, completedAt time.Time) *Voucher {
	return &Voucher{
		RequestID:        requestID,
		OrganizationID:   orgID,
		APIKeyID:         keyID,
		ModelID:          modelID,
		PromptTokens:     100,
		CompletionTokens: 50,
		CachedTokens:     10,
		ReasoningTokens:  5,
		CompletedAt:      completedAt,
	}
}

// AC1: ingest writes a voucher; a duplicate request_id returns the
// existing row without writing a second one.
func TestRepositoryIngestIdempotency(t *testing.T) {
	repo := newMeteringTestRepo(t)
	ctx := context.Background()
	completed := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)

	first := mustIngest(ctx, t, repo, testVoucher("req-1", "org-1", "key-1", "model-a", completed))
	assert.NotEmpty(t, first.ID)

	// Duplicate delivery: same request_id, different token counts —
	// the existing row wins, nothing is overwritten.
	dup := testVoucher("req-1", "org-1", "key-1", "model-a", completed)
	dup.PromptTokens = 999999
	second, err := repo.IngestVoucher(ctx, dup)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, int64(100), second.PromptTokens, "existing row must not be overwritten")

	var count int64
	require.NoError(t, repo.DB(ctx).Model(&Voucher{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "exactly one row after duplicate delivery")
}

// AC4: SettleBucket sums the bucket's vouchers into one usage record
// and marks them settled.
func TestRepositorySettleBucketSumsAndMarks(t *testing.T) {
	repo := newMeteringTestRepo(t)
	ctx := context.Background()
	hourStart := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)

	mustIngest(ctx, t, repo, testVoucher("r1", "org-1", "key-1", "model-a", hourStart.Add(10*time.Minute)))
	v2 := testVoucher("r2", "org-1", "key-1", "model-b", hourStart.Add(30*time.Minute))
	v2.PromptTokens = 200
	v2.CompletionTokens = 100
	mustIngest(ctx, t, repo, v2)
	// A voucher in a different hour must not be swept into the bucket.
	mustIngest(ctx, t, repo, testVoucher("r3", "org-1", "key-1", "model-a", hourStart.Add(90*time.Minute)))

	bucket := HourBucket{APIKeyID: "key-1", OrganizationID: "org-1", PeriodStart: hourStart.Unix()}
	record, err := repo.SettleBucket(ctx, bucket, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.Equal(t, int64(300), record.PromptTokens)
	assert.Equal(t, int64(150), record.CompletionTokens)
	assert.Equal(t, int64(20), record.CachedTokens)
	assert.Equal(t, int64(10), record.ReasoningTokens)
	assert.Equal(t, int64(2), record.RequestCount)
	assert.Equal(t, hourStart.Unix()+3600, record.PeriodEnd)

	// Both bucket vouchers are marked; the other-hour voucher is not.
	var settled int64
	require.NoError(t, repo.DB(ctx).Model(&Voucher{}).
		Where("settled_usage_record_id IS NOT NULL").Count(&settled).Error)
	assert.Equal(t, int64(2), settled)
}

// AC5: SettleBucket is idempotent — re-running over an already-settled
// bucket inserts no duplicate record and double-counts nothing.
func TestRepositorySettleBucketIdempotent(t *testing.T) {
	repo := newMeteringTestRepo(t)
	ctx := context.Background()
	hourStart := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)

	mustIngest(ctx, t, repo, testVoucher("r1", "org-1", "key-1", "model-a", hourStart.Add(10*time.Minute)))

	bucket := HourBucket{APIKeyID: "key-1", OrganizationID: "org-1", PeriodStart: hourStart.Unix()}
	first, err := repo.SettleBucket(ctx, bucket, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, first)

	// Re-run: no new record, no double counting.
	second, err := repo.SettleBucket(ctx, bucket, time.Now().UTC())
	require.NoError(t, err)
	assert.Nil(t, second, "nothing left to settle")

	var records int64
	require.NoError(t, repo.DB(ctx).Model(&UsageRecord{}).Count(&records).Error)
	assert.Equal(t, int64(1), records)

	stored, err := repo.DB(ctx).Model(&UsageRecord{}).Count(&records), error(nil)
	_ = stored
	_ = err
}

// AC11: DeleteSettledBefore removes only settled vouchers; unsettled
// ones are kept; usage records are never touched.
func TestRepositoryRetentionKeepsUnsettled(t *testing.T) {
	repo := newMeteringTestRepo(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-100 * 24 * time.Hour)
	hourStart := old.Truncate(time.Hour)

	settled := mustIngest(ctx, t, repo, testVoucher("r-settled", "org-1", "key-1", "model-a", hourStart))
	unsettled := mustIngest(ctx, t, repo, testVoucher("r-pending", "org-1", "key-1", "model-a", hourStart))
	require.NoError(t, repo.DB(ctx).Model(&Voucher{}).
		Where("id = ?", settled.ID).
		Update("settled_usage_record_id", "rec-1").Error)
	// Force both created_at into the past (ingest sets created_at=now).
	require.NoError(t, repo.DB(ctx).Model(&Voucher{}).
		Where("id IN ?", []string{settled.ID, unsettled.ID}).
		Update("created_at", old).Error)

	deleted, err := repo.DeleteSettledBefore(ctx, time.Now().UTC(), 1000)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	var remaining []Voucher
	require.NoError(t, repo.DB(ctx).Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.Equal(t, unsettled.ID, remaining[0].ID, "unsettled voucher kept")

	// CountUnsettledOlderThan reports the kept one (FR5.3 warning hook).
	count, err := repo.CountUnsettledOlderThan(ctx, time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

// AC8: UsageSummary groups by key with the settled/pending hour split.
func TestRepositoryUsageSummary(t *testing.T) {
	repo := newMeteringTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	hourA := now.Add(-3 * time.Hour).Truncate(time.Hour)
	hourB := now.Add(-2 * time.Hour).Truncate(time.Hour)

	// key-1: two vouchers in hourA (will be settled), one in hourB
	// (left pending).
	mustIngest(ctx, t, repo, testVoucher("a1", "org-1", "key-1", "model-a", hourA.Add(5*time.Minute)))
	v := testVoucher("a2", "org-1", "key-1", "model-b", hourA.Add(25*time.Minute))
	v.PromptTokens = 500
	mustIngest(ctx, t, repo, v)
	mustIngest(ctx, t, repo, testVoucher("b1", "org-1", "key-1", "model-a", hourB.Add(5*time.Minute)))
	// key-2: one pending voucher in hourB.
	mustIngest(ctx, t, repo, testVoucher("c1", "org-1", "key-2", "model-a", hourB.Add(15*time.Minute)))
	// Another org's voucher must never leak in.
	mustIngest(ctx, t, repo, testVoucher("x1", "org-2", "key-1", "model-a", hourB.Add(15*time.Minute)))

	// Settle key-1's hourA bucket.
	_, err := repo.SettleBucket(ctx, HourBucket{APIKeyID: "key-1", OrganizationID: "org-1", PeriodStart: hourA.Unix()}, now)
	require.NoError(t, err)

	rows, err := repo.UsageSummary(ctx, "org-1", now.Add(-6*time.Hour).Unix(), now.Unix(), false)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	byKey := map[string]*UsageSummaryRow{}
	for _, row := range rows {
		byKey[row.GroupKey] = row
	}

	k1 := byKey["key-1"]
	require.NotNil(t, k1)
	assert.Equal(t, int64(700), k1.PromptTokens, "settled + pending prompt tokens")
	assert.Equal(t, int64(150), k1.CompletionTokens)
	assert.Equal(t, int64(3), k1.RequestCount)
	assert.Equal(t, int64(1), k1.SettledHours, "hourA settled")
	assert.Equal(t, int64(1), k1.PendingHours, "hourB pending")

	k2 := byKey["key-2"]
	require.NotNil(t, k2)
	assert.Equal(t, int64(100), k2.PromptTokens)
	assert.Equal(t, int64(0), k2.SettledHours)
	assert.Equal(t, int64(1), k2.PendingHours)

	// Group by model: model-a spans both keys.
	mrows, err := repo.UsageSummary(ctx, "org-1", now.Add(-6*time.Hour).Unix(), now.Unix(), true)
	require.NoError(t, err)
	byModel := map[string]*UsageSummaryRow{}
	for _, row := range mrows {
		byModel[row.GroupKey] = row
	}
	ma := byModel["model-a"]
	require.NotNil(t, ma)
	assert.Equal(t, int64(300), ma.PromptTokens, "model-a: a1 + b1 + c1")
	assert.Equal(t, int64(3), ma.RequestCount)
}

// AC15: a burst of 1000 events settles exactly once — one usage record,
// 1000 marked vouchers.
func TestRepositoryBurst1000SettlesOnce(t *testing.T) {
	repo := newMeteringTestRepo(t)
	ctx := context.Background()
	hourStart := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)

	for i := 0; i < 1000; i++ {
		v := testVoucher(fmt.Sprintf("burst-%d", i), "org-1", "key-1", "model-a", hourStart.Add(time.Duration(i)*time.Second))
		v.PromptTokens = 10
		_, err := repo.IngestVoucher(ctx, v)
		require.NoError(t, err)
	}

	bucket := HourBucket{APIKeyID: "key-1", OrganizationID: "org-1", PeriodStart: hourStart.Unix()}
	record, err := repo.SettleBucket(ctx, bucket, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.Equal(t, int64(1000), record.RequestCount)
	assert.Equal(t, int64(10000), record.PromptTokens)

	// Second pass: nothing new.
	again, err := repo.SettleBucket(ctx, bucket, time.Now().UTC())
	require.NoError(t, err)
	assert.Nil(t, again)

	var records int64
	require.NoError(t, repo.DB(ctx).Model(&UsageRecord{}).Count(&records).Error)
	assert.Equal(t, int64(1), records, "exactly one usage record")
}

// FindByID maps a miss to 10403.
func TestRepositoryFindByIDNotFound(t *testing.T) {
	repo := newMeteringTestRepo(t)
	ctx := context.Background()
	_, err := repo.FindByID(ctx, "voucher-missing")
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeMeteringVoucherNotFound, ae.Code)
}

// ListVouchers filters by key, model and range, paginated newest-first.
func TestRepositoryListVouchersFilters(t *testing.T) {
	repo := newMeteringTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-5 * time.Hour)

	mustIngest(ctx, t, repo, testVoucher("old-a", "org-1", "key-1", "model-a", old))
	mustIngest(ctx, t, repo, testVoucher("new-a", "org-1", "key-1", "model-a", now.Add(-time.Hour)))
	mustIngest(ctx, t, repo, testVoucher("new-b", "org-1", "key-2", "model-b", now.Add(-30*time.Minute)))
	mustIngest(ctx, t, repo, testVoucher("other-org", "org-2", "key-1", "model-a", now.Add(-time.Hour)))

	rows, total, err := repo.ListVouchers(ctx, VoucherFilter{
		OrganizationID: "org-1", Since: now.Add(-2 * time.Hour).Unix(), Until: now.Unix(),
		Offset: 0, Limit: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	require.Len(t, rows, 2)
	assert.Equal(t, "new-b", rows[0].RequestID, "newest first")
	assert.Equal(t, "new-a", rows[1].RequestID)

	rows, total, err = repo.ListVouchers(ctx, VoucherFilter{
		OrganizationID: "org-1", APIKeyID: "key-1", Since: now.Add(-2 * time.Hour).Unix(), Until: now.Unix(),
		Offset: 0, Limit: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	assert.Equal(t, "new-a", rows[0].RequestID)

	rows, total, err = repo.ListVouchers(ctx, VoucherFilter{
		OrganizationID: "org-1", ModelID: "model-b", Since: now.Add(-2 * time.Hour).Unix(), Until: now.Unix(),
		Offset: 0, Limit: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
}

// ListUsageRecords filters by key and range, newest period first.
func TestRepositoryListUsageRecords(t *testing.T) {
	repo := newMeteringTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	h1 := now.Add(-4 * time.Hour).Truncate(time.Hour)
	h2 := now.Add(-3 * time.Hour).Truncate(time.Hour)

	for i, hour := range []time.Time{h1, h2} {
		mustIngest(ctx, t, repo, testVoucher(fmt.Sprintf("k1-%d", i), "org-1", "key-1", "model-a", hour.Add(10*time.Minute)))
		_, err := repo.SettleBucket(ctx, HourBucket{APIKeyID: "key-1", OrganizationID: "org-1", PeriodStart: hour.Unix()}, now)
		require.NoError(t, err)
	}
	mustIngest(ctx, t, repo, testVoucher("k2-0", "org-1", "key-2", "model-a", h1.Add(10*time.Minute)))
	_, err := repo.SettleBucket(ctx, HourBucket{APIKeyID: "key-2", OrganizationID: "org-1", PeriodStart: h1.Unix()}, now)
	require.NoError(t, err)

	rows, total, err := repo.ListUsageRecords(ctx, UsageRecordFilter{
		OrganizationID: "org-1", Since: h1.Unix(), Until: now.Unix(), Offset: 0, Limit: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	require.Len(t, rows, 3)
	assert.GreaterOrEqual(t, rows[0].PeriodStart, rows[1].PeriodStart, "newest period first")

	rows, total, err = repo.ListUsageRecords(ctx, UsageRecordFilter{
		OrganizationID: "org-1", APIKeyID: "key-2", Since: h1.Unix(), Until: now.Unix(), Offset: 0, Limit: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	assert.Equal(t, "key-2", rows[0].APIKeyID)
}

// UnpublishedRecords + MarkPublished drive the publish-retry scan.
func TestRepositoryUnpublishedAndMarkPublished(t *testing.T) {
	repo := newMeteringTestRepo(t)
	ctx := context.Background()
	hour := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)
	mustIngest(ctx, t, repo, testVoucher("r1", "org-1", "key-1", "model-a", hour.Add(10*time.Minute)))
	record, err := repo.SettleBucket(ctx, HourBucket{APIKeyID: "key-1", OrganizationID: "org-1", PeriodStart: hour.Unix()}, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, record)

	unpublished, err := repo.UnpublishedRecords(ctx)
	require.NoError(t, err)
	require.Len(t, unpublished, 1)
	assert.Equal(t, record.ID, unpublished[0].ID)

	require.NoError(t, repo.MarkPublished(ctx, record.ID))
	unpublished, err = repo.UnpublishedRecords(ctx)
	require.NoError(t, err)
	assert.Empty(t, unpublished)
}
