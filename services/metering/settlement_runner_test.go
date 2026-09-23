package metering

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/mq"
)

// recordingMQ captures published settlement events.
type recordingMQ struct {
	mu     sync.Mutex
	pub    []publishedMsg
	failOn bool
}

type publishedMsg struct {
	subject string
	body    []byte
	headers map[string]string
}

func (r *recordingMQ) Publish(_ context.Context, subject string, body []byte, headers map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failOn {
		return fmt.Errorf("publish failed")
	}
	r.pub = append(r.pub, publishedMsg{subject: subject, body: body, headers: headers})
	return nil
}

func (r *recordingMQ) Subscribe(_ context.Context, _ string, _ mq.Handler) error { return nil }

func (r *recordingMQ) Close() error { return nil }

func (r *recordingMQ) published() []publishedMsg {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]publishedMsg, len(r.pub))
	copy(out, r.pub)
	return out
}

func newRunnerTestDB(t *testing.T) *gorm.DB {
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

// AC6: only closed hours (period end + grace) are settled; open hours
// are left pending.
func TestSettlementEligibility(t *testing.T) {
	db := newRunnerTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	// A voucher in a fully closed hour (3h ago, grace 5m).
	closedHour := now.Add(-3 * time.Hour).Truncate(time.Hour)
	// A voucher in the current hour: the period cannot have closed yet,
	// regardless of when in the hour the test runs.
	openHour := now.Truncate(time.Hour)

	for i, hour := range []time.Time{closedHour, openHour} {
		v := testVoucher(fmt.Sprintf("elig-%d", i), "org-1", "key-1", "model-a", hour.Add(5*time.Minute))
		_, err := repo.IngestVoucher(ctx, v)
		require.NoError(t, err)
	}

	runner := NewSettlementRunner(repo, &recordingMQ{}, time.Minute, 5*time.Minute, 2)
	require.NoError(t, runner.SettleOnce(ctx))

	// The closed hour produced one usage record; the open hour none.
	var records int64
	require.NoError(t, db.Model(&UsageRecord{}).Count(&records).Error)
	assert.Equal(t, int64(1), records)

	// The open-hour voucher is still unsettled.
	var pendingCount int64
	require.NoError(t, db.Model(&Voucher{}).
		Where("settled_usage_record_id IS NULL").Count(&pendingCount).Error)
	assert.Equal(t, int64(1), pendingCount)
}

// AC7: a usage record is published to billing.settlements exactly once;
// a publish failure leaves it unpublished for the next pass.
func TestSettlementPublishRetry(t *testing.T) {
	db := newRunnerTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	hour := now.Add(-3 * time.Hour).Truncate(time.Hour)

	v := testVoucher("pub-1", "org-1", "key-1", "model-a", hour.Add(5*time.Minute))
	_, err := repo.IngestVoucher(ctx, v)
	require.NoError(t, err)

	// First pass: publishing fails — the record stays unpublished.
	failing := &recordingMQ{failOn: true}
	runner := NewSettlementRunner(repo, failing, time.Minute, 5*time.Minute, 2)
	require.NoError(t, runner.SettleOnce(ctx))
	unpublished, err := repo.UnpublishedRecords(ctx)
	require.NoError(t, err)
	assert.Len(t, unpublished, 1, "record kept for retry after publish failure")

	// Second pass: publishing succeeds — exactly one event.
	healthy := &recordingMQ{}
	runner = NewSettlementRunner(repo, healthy, time.Minute, 5*time.Minute, 2)
	require.NoError(t, runner.SettleOnce(ctx))
	events := healthy.published()
	require.Len(t, events, 1)
	assert.Equal(t, mq.DefaultSubjects().Settlements, events[0].subject)
	assert.NotEmpty(t, events[0].headers["usage_record_id"])

	var event settlementEvent
	require.NoError(t, json.Unmarshal(events[0].body, &event))
	assert.Equal(t, unpublished[0].ID, event.UsageRecordID)
	assert.Equal(t, int64(100), event.PromptTokens)

	// Third pass: nothing new is published.
	runner = NewSettlementRunner(repo, healthy, time.Minute, 5*time.Minute, 2)
	require.NoError(t, runner.SettleOnce(ctx))
	assert.Len(t, healthy.published(), 1, "no duplicate settlement event")
}

// AC4/AC15 through the runner: a burst settles into one record and one
// publish.
func TestSettlementBurst(t *testing.T) {
	db := newRunnerTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	hour := now.Add(-3 * time.Hour).Truncate(time.Hour)

	for i := 0; i < 50; i++ {
		v := testVoucher(fmt.Sprintf("burst-r-%d", i), "org-1", "key-1", "model-a", hour.Add(time.Duration(i)*time.Second))
		_, err := repo.IngestVoucher(ctx, v)
		require.NoError(t, err)
	}

	bus := &recordingMQ{}
	runner := NewSettlementRunner(repo, bus, time.Minute, 0, 2)
	require.NoError(t, runner.SettleOnce(ctx))
	assert.Len(t, bus.published(), 1)

	var records int64
	require.NoError(t, db.Model(&UsageRecord{}).Count(&records).Error)
	assert.Equal(t, int64(1), records)
	var rec UsageRecord
	require.NoError(t, db.First(&rec).Error)
	assert.Equal(t, int64(50), rec.RequestCount)
}

// AC11: RetainOnce deletes settled vouchers past the TTL and keeps
// unsettled ones.
func TestRetentionRunner(t *testing.T) {
	db := newRunnerTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	oldHour := now.Add(-100 * 24 * time.Hour).Truncate(time.Hour)

	settled := testVoucher("ret-settled", "org-1", "key-1", "model-a", oldHour.Add(5*time.Minute))
	stored, err := repo.IngestVoucher(ctx, settled)
	require.NoError(t, err)
	require.NoError(t, db.Model(&Voucher{}).Where("id = ?", stored.ID).
		Update("settled_usage_record_id", "rec-old").Error)
	require.NoError(t, db.Model(&Voucher{}).Where("id = ?", stored.ID).
		Update("created_at", oldHour).Error)

	pending := testVoucher("ret-pending", "org-1", "key-1", "model-a", oldHour.Add(6*time.Minute))
	storedPending, err := repo.IngestVoucher(ctx, pending)
	require.NoError(t, err)
	require.NoError(t, db.Model(&Voucher{}).Where("id = ?", storedPending.ID).
		Update("created_at", oldHour).Error)

	runner := NewRetentionRunner(repo, 90*24*time.Hour, 100, time.Hour)
	runner.RetainOnce(ctx)

	var remaining []Voucher
	require.NoError(t, db.Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.Equal(t, storedPending.ID, remaining[0].ID)
}

// AC3: the event consumer processes a metering.events message through
// the shared handler; invalid payloads are skipped without error.
func TestEventConsumerHandle(t *testing.T) {
	db := newRunnerTestDB(t)
	repo := NewRepository(db)
	svc := &Service{repo: repo}
	consumer := NewEventConsumer(nil, svc, 1)

	ctx := context.Background()

	// A valid event is ingested.
	valid := meteringEvent{
		RequestID: "cons-1", OrganizationID: "org-1", APIKeyID: "key-1", ModelID: "model-a",
		CompletedAt: time.Now().Add(-time.Hour).Unix(),
		Usage:       tokenUsage{PromptTokens: 10, CompletionTokens: 5},
	}
	body, err := json.Marshal(valid)
	require.NoError(t, err)
	require.NoError(t, consumer.handle(ctx, mq.Message{Body: body}))
	stored, err := repo.FindByRequestID(ctx, "cons-1")
	require.NoError(t, err)
	assert.Equal(t, int64(10), stored.PromptTokens)

	// A duplicate delivery is a no-op.
	require.NoError(t, consumer.handle(ctx, mq.Message{Body: body}))

	// Malformed JSON is skipped (nil error) — poison messages must not
	// wedge the subscription.
	require.NoError(t, consumer.handle(ctx, mq.Message{Body: []byte("{not json")}))

	// An invalid (but well-formed) event is skipped with 10401.
	bad, err := json.Marshal(meteringEvent{RequestID: "", OrganizationID: "org-1", CompletedAt: 1})
	require.NoError(t, err)
	require.NoError(t, consumer.handle(ctx, mq.Message{Body: bad}))

	var count int64
	require.NoError(t, db.Model(&Voucher{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "only the valid event was stored")
}
