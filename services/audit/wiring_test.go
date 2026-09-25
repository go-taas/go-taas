package audit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/server"
)

// fakeComponents implements server.Components with a pluggable DB handle
// so the Runner constructor can be exercised without a live stack.
type fakeComponents struct {
	db *gorm.DB
}

func (f *fakeComponents) DB() server.DBComponent {
	if f.db == nil {
		return nil
	}
	return &fakeDBComponent{db: f.db}
}

func (f *fakeComponents) Redis() server.RedisComponent { return nil }
func (f *fakeComponents) MQ() server.MQComponent       { return nil }

type fakeDBComponent struct{ db *gorm.DB }

func (f *fakeDBComponent) GormDB() any { return f.db }

// auditTestConfig installs an audit config with the retention runner
// enabled.
func auditTestConfig(t *testing.T, enabled bool) *config.Configuration {
	t.Helper()
	cfg := &config.Configuration{}
	cfg.Audit.Retention.Enabled = enabled
	cfg.Audit.Retention.EventTTL = time.Hour
	cfg.Audit.Retention.BatchSize = 10
	cfg.Audit.Retention.Interval = time.Hour
	cfg.Audit.ExportMaxRows = 10000
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })
	return cfg
}

// TestServiceWiring covers the server.Service / Migrator / gateway
// wiring surface.
func TestServiceWiring(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:wiring?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	svc := New(&fakeComponents{db: db})
	assert.Equal(t, ServiceName, svc.ServiceName())
	assert.NotNil(t, svc.GetServiceHandlerRegisterFn())

	// Migrate creates the audit_events table.
	require.NoError(t, svc.Migrate(context.Background()))
	assert.True(t, db.Migrator().HasTable(&AuditEvent{}))

	// MigrateSchemaForFVT also creates the table.
	require.NoError(t, MigrateSchemaForFVT(db))
}

// TestRetentionRunnerWiring covers the runner constructor from shared
// components.
func TestRetentionRunnerWiring(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:wiring2?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	// Disabled -> nil.
	auditTestConfig(t, false)
	assert.Nil(t, NewAuditRetentionRunnerRunner(&fakeComponents{db: db}))

	// Enabled -> a runner.
	auditTestConfig(t, true)
	runner := NewAuditRetentionRunnerRunner(&fakeComponents{db: db})
	require.NotNil(t, runner)

	// No DB component -> nil.
	assert.Nil(t, NewAuditRetentionRunnerRunner(&fakeComponents{db: nil}))

	// Run returns on context cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, runner.Run(ctx))
}

// TestNewAuditRetentionRunnerDefaults covers the constructor defaults.
func TestNewAuditRetentionRunnerDefaults(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:wiring3?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	runner := NewAuditRetentionRunner(NewRepository(db), time.Hour, 0, 0)
	assert.Equal(t, 1000, runner.batchSize)
	assert.Equal(t, time.Hour, runner.interval)
}
