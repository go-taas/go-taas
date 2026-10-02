package account

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newProviderDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&DataExport{}))
	// Create the read-only tables the provider reads.
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS usage_records (id TEXT PRIMARY KEY, organization_id TEXT, model_id TEXT, prompt_tokens INTEGER, completion_tokens INTEGER, created_at TIMESTAMP)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS bills (id TEXT PRIMARY KEY, organization_id TEXT, amount REAL, currency TEXT, status TEXT, created_at TIMESTAMP)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS request_logs (id TEXT PRIMARY KEY, organization_id TEXT, model_id TEXT, prompt_tokens INTEGER, completion_tokens INTEGER, latency_ms INTEGER, status TEXT, created_at TIMESTAMP)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS organizations (id TEXT PRIMARY KEY, display_name TEXT, state TEXT, created_at TIMESTAMP)`).Error)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func TestDBExportDataProvider(t *testing.T) {
	db := newProviderDB(t)
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`INSERT INTO usage_records (id, organization_id, model_id, prompt_tokens, completion_tokens, created_at) VALUES ('u1', 'org-1', 'm1', 10, 20, ?)`, now).Error)
	require.NoError(t, db.Exec(`INSERT INTO bills (id, organization_id, amount, currency, status, created_at) VALUES ('b1', 'org-1', 1.5, 'USD', 'paid', ?)`, now).Error)
	require.NoError(t, db.Exec(`INSERT INTO request_logs (id, organization_id, model_id, prompt_tokens, completion_tokens, latency_ms, status, created_at) VALUES ('r1', 'org-1', 'm1', 10, 20, 5, 'ok', ?)`, now).Error)
	require.NoError(t, db.Exec(`INSERT INTO organizations (id, display_name, state, created_at) VALUES ('org-1', 'Org One', 'active', ?)`, now).Error)

	provider := NewDBExportDataProvider(db)
	since := now.Add(-time.Hour).Unix()
	until := now.Add(time.Hour).Unix()

	// AC4: usage rows.
	rows, err := provider.ExportRows(context.Background(), "org-1", ExportTypeUsage, since, until)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "u1", rows[0]["id"])

	// AC4: billing rows.
	rows, err = provider.ExportRows(context.Background(), "org-1", ExportTypeBilling, since, until)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "b1", rows[0]["id"])

	// AC4: request log rows.
	rows, err = provider.ExportRows(context.Background(), "org-1", ExportTypeRequestLogs, since, until)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "r1", rows[0]["id"])

	// AC4: account rows.
	rows, err = provider.ExportRows(context.Background(), "org-1", ExportTypeAccount, 0, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "org-1", rows[0]["id"])

	// AC4: unknown type returns empty.
	rows, err = provider.ExportRows(context.Background(), "org-1", "bogus", since, until)
	require.NoError(t, err)
	assert.Empty(t, rows)
}