package metering

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// fakeComponents implements server.Components with pluggable handles so
// the Runner constructors can be exercised without a live stack.
type fakeComponents struct {
	db *gorm.DB
	mq mq.Client
}

func (f *fakeComponents) DB() server.DBComponent {
	if f.db == nil {
		return nil
	}
	return &fakeDBComponent{db: f.db}
}

func (f *fakeComponents) Redis() server.RedisComponent { return nil }

func (f *fakeComponents) MQ() server.MQComponent {
	if f.mq == nil {
		return nil
	}
	return &fakeMQComponent{client: f.mq}
}

type fakeDBComponent struct{ db *gorm.DB }

func (f *fakeDBComponent) GormDB() any { return f.db }

type fakeMQComponent struct{ client mq.Client }

func (f *fakeMQComponent) Publish(ctx context.Context, subject string, body []byte, headers map[string]string) error {
	return f.client.Publish(ctx, subject, body, headers)
}

func (f *fakeMQComponent) Client() any { return f.client }

// meteringTestConfig returns a configuration with every metering runner
// enabled and installs it as the process-wide config.
func meteringTestConfig(t *testing.T, enabled bool) *config.Configuration {
	t.Helper()
	cfg := &config.Configuration{}
	cfg.Metering.EventConsumer.Enabled = enabled
	cfg.Metering.Settlement.Enabled = enabled
	cfg.Metering.Retention.Enabled = enabled
	cfg.Metering.Settlement.Interval = 50 * time.Millisecond
	cfg.Metering.Settlement.GracePeriod = time.Minute
	cfg.Metering.Settlement.Workers = 1
	cfg.Metering.Retention.VoucherTTL = time.Hour
	cfg.Metering.Retention.BatchSize = 10
	cfg.Metering.Retention.Interval = time.Hour
	cfg.Metering.EventConsumer.Workers = 1
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })
	return cfg
}

func newWiringTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:wiring?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Voucher{}, &UsageRecord{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

// TestRunnerConstructorsDisabled verifies the Runner factories return
// nil for nil components, disabled config and missing components, so
// AddRunner can be called unconditionally.
func TestRunnerConstructorsDisabled(t *testing.T) {
	assert.Nil(t, NewEventConsumerRunner(nil))
	assert.Nil(t, NewSettlementRunnerRunner(nil))
	assert.Nil(t, NewRetentionRunnerRunner(nil))

	meteringTestConfig(t, false)
	assert.Nil(t, NewEventConsumerRunner(&fakeComponents{}))
	assert.Nil(t, NewSettlementRunnerRunner(&fakeComponents{}))
	assert.Nil(t, NewRetentionRunnerRunner(&fakeComponents{}))

	meteringTestConfig(t, true)
	// Missing MQ disables the consumer and settlement; retention only
	// needs the DB, so it stays enabled.
	assert.Nil(t, NewEventConsumerRunner(&fakeComponents{}))
	assert.Nil(t, NewSettlementRunnerRunner(&fakeComponents{}))
	db := newWiringTestDB(t)
	assert.NotNil(t, NewRetentionRunnerRunner(&fakeComponents{db: db}))
	assert.Nil(t, NewRetentionRunnerRunner(&fakeComponents{mq: &recordingMQ{}}))
}

// TestRunnerConstructorsEnabled verifies the factories wire a runner
// from complete components.
func TestRunnerConstructorsEnabled(t *testing.T) {
	meteringTestConfig(t, true)
	db := newWiringTestDB(t)
	bus := &recordingMQ{}
	components := &fakeComponents{db: db, mq: bus}

	consumer := NewEventConsumerRunner(components)
	require.NotNil(t, consumer)
	settler := NewSettlementRunnerRunner(components)
	require.NotNil(t, settler)
	retainer := NewRetentionRunnerRunner(components)
	require.NotNil(t, retainer)
}

// TestRunLoopsCancel verifies each Run loop exits cleanly on context
// cancellation (the server framework cancels runners on shutdown).
func TestRunLoopsCancel(t *testing.T) {
	meteringTestConfig(t, true)
	db := newWiringTestDB(t)
	bus := &recordingMQ{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 3)
	go func() { done <- NewEventConsumer(bus, NewForFVT(db, bus), 1).Run(ctx) }()
	go func() { done <- NewSettlementRunner(NewRepository(db), bus, 10*time.Millisecond, 0, 1).Run(ctx) }()
	go func() { done <- NewRetentionRunner(NewRepository(db), time.Hour, 10, 10*time.Millisecond).Run(ctx) }()

	// Let each loop tick at least once, then cancel.
	time.Sleep(80 * time.Millisecond)
	cancel()
	for i := 0; i < 3; i++ {
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("runner did not exit on cancellation")
		}
	}
}

// TestServiceNewAndMigrate verifies the components-based constructor,
// lazy repository wiring and the Migrator path.
func TestServiceNewAndMigrate(t *testing.T) {
	meteringTestConfig(t, true)
	db := newWiringTestDB(t)
	bus := &recordingMQ{}
	svc := New(&fakeComponents{db: db, mq: bus})
	require.NoError(t, svc.Migrate(context.Background()))

	// The lazily wired repository answers queries.
	vouchers, err := svc.ListVouchers(withOrg(context.Background(), "org-1"), nil)
	require.NoError(t, err)
	assert.Empty(t, vouchers.GetVouchers())

	// A service without components fails lazily with an internal error.
	bare := New(nil)
	_, err = bare.ListVouchers(context.Background(), nil)
	require.Error(t, err)
}
