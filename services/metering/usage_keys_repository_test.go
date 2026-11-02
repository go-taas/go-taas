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
)

func newUsageKeysTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, MigrateSchemaForFVT(db))
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (id TEXT PRIMARY KEY, name TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS charge_records (id TEXT PRIMARY KEY, organization_id TEXT, api_key_id TEXT, model_id TEXT, period_start INTEGER, amount REAL, priced BOOLEAN)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO api_keys (id, name) VALUES ('k1', 'Key One'), ('k2', 'Key Two')`).Error)
	return db
}

func seedUsageLog(t *testing.T, db *gorm.DB, org, key, model string, at time.Time, latency int64, status string, prompt, completion int64) {
	t.Helper()
	row := RequestLog{
		ID:               "id-" + at.Format("20060102150405") + key,
		RequestID:        "req-" + at.Format("20060102150405") + key,
		OrganizationID:   org,
		APIKeyID:         key,
		ModelID:          model,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		LatencyMs:        latency,
		Status:           status,
		CreatedAt:        at.UTC(),
	}
	require.NoError(t, db.Create(&row).Error)
}

func seedCharge(t *testing.T, db *gorm.DB, org, key string, periodStart int64, amount float64) {
	t.Helper()
	require.NoError(t, db.Exec(`INSERT INTO charge_records (id, organization_id, api_key_id, model_id, period_start, amount, priced) VALUES (?, ?, ?, 'm1', ?, ?, 1)`,
		"c-"+key+"-"+fmt.Sprintf("%d", periodStart), org, key, periodStart, amount).Error)
}

func TestAggregateUsageKeys(t *testing.T) {
	db := newUsageKeysTestDB(t)
	now := time.Now().UTC()
	seedUsageLog(t, db, "org-a", "k1", "m1", now.Add(-time.Hour), 100, "success", 10, 20)
	seedUsageLog(t, db, "org-a", "k1", "m1", now, 200, "error", 10, 20)
	seedUsageLog(t, db, "org-a", "k2", "m1", now, 50, "success", 5, 10)

	repo := NewUsageKeysRepository(db)
	buckets, keys, err := repo.AggregateUsageKeys(context.Background(), UsageKeysFilter{OrganizationID: "org-a"}, now.Add(-2*time.Hour).Unix(), now.Add(time.Hour).Unix(), 3600)
	require.NoError(t, err)
	assert.Len(t, buckets, 2)
	assert.Len(t, keys, 2)

	// k1 has 2 requests, 1 error.
	var k1 *UsageKeyAggRow
	for i := range keys {
		if keys[i].APIKeyID == "k1" {
			k1 = &keys[i]
		}
	}
	require.NotNil(t, k1)
	assert.Equal(t, int64(2), k1.RequestCount)
	assert.Equal(t, int64(1), k1.ErrorCount)
	assert.Equal(t, int64(60), k1.TotalTokens)
}

func TestAggregateUsageKey(t *testing.T) {
	db := newUsageKeysTestDB(t)
	now := time.Now().UTC()
	seedUsageLog(t, db, "org-a", "k1", "m1", now, 100, "success", 10, 20)
	seedUsageLog(t, db, "org-a", "k2", "m1", now, 50, "success", 5, 10)

	repo := NewUsageKeysRepository(db)
	buckets, err := repo.AggregateUsageKey(context.Background(), "org-a", "k1", now.Add(-time.Hour).Unix(), now.Add(time.Hour).Unix(), 3600)
	require.NoError(t, err)
	assert.Len(t, buckets, 1)
	assert.Equal(t, int64(1), buckets[0].RequestCount)
}

func TestChargeCostByKey(t *testing.T) {
	db := newUsageKeysTestDB(t)
	now := time.Now().Unix()
	seedCharge(t, db, "org-a", "k1", now, 1.5)
	seedCharge(t, db, "org-a", "k2", now, 2.5)

	repo := NewUsageKeysRepository(db)
	costs, err := repo.ChargeCostByKey(context.Background(), "org-a", now-3600, now+3600)
	require.NoError(t, err)
	assert.Equal(t, int64(1_500_000), costs["k1"], "micro-units: 1.50 is 1_500_000 micros")
	assert.Equal(t, int64(2_500_000), costs["k2"], "micro-units: 2.50 is 2_500_000 micros")
}

func TestUsageKeysDataThrough(t *testing.T) {
	db := newUsageKeysTestDB(t)
	now := time.Now().UTC()
	seedUsageLog(t, db, "org-a", "k1", "m1", now, 100, "success", 10, 20)

	repo := NewUsageKeysRepository(db)
	watermark, err := repo.DataThrough(context.Background(), UsageKeysFilter{OrganizationID: "org-a"}, now.Add(-time.Hour).Unix(), now.Add(time.Hour).Unix(), 3600)
	require.NoError(t, err)
	// The last complete bucket is the hour before the most recent log.
	lastBucket := (now.Unix() / 3600) * 3600
	assert.Equal(t, lastBucket-3600, watermark)
}

func TestBucketSizeForRange(t *testing.T) {
	assert.Equal(t, int64(3600), bucketSizeForRange(0, 7*24*3600))
	assert.Equal(t, int64(24*3600), bucketSizeForRange(0, 8*24*3600))
}

func TestUsageKeysAPIKeyName(t *testing.T) {
	db := newUsageKeysTestDB(t)
	repo := NewUsageKeysRepository(db)
	name, err := repo.APIKeyName(context.Background(), "k1")
	require.NoError(t, err)
	assert.Equal(t, "Key One", name)
}
