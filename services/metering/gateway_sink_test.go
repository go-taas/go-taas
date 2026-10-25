// Copyright 2025 The go-taas Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package metering

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/model"
)

// captureMQ records published subjects and bodies.
type captureMQ struct {
	mq.Client
	mu       sync.Mutex
	subject  []string
	bodies   [][]byte
	failNext bool
}

func (c *captureMQ) Publish(_ context.Context, subject string, body []byte, _ map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failNext {
		c.failNext = false
		return assert.AnError
	}
	c.subject = append(c.subject, subject)
	c.bodies = append(c.bodies, append([]byte(nil), body...))
	return nil
}

func (c *captureMQ) Subscribe(context.Context, string, mq.Handler) error { return nil }
func (c *captureMQ) Close() error                                        { return nil }

// sinkTestDSN returns a per-test file database.
//
// A shared-cache in-memory database raises SQLITE_LOCKED (not SQLITE_BUSY)
// when the consumer goroutine and the test write at once, and busy_timeout
// does not apply to that error class.
func sinkTestDSN(t *testing.T) string {
	t.Helper()
	return "file:" + filepath.Join(t.TempDir(), "test.db") + "?_busy_timeout=10000&_txlock=immediate&_journal_mode=WAL"
}

// newSinkTestEnv builds a metering service over a per-test database and a
// capturing queue.
func newSinkTestEnv(t *testing.T) (*Service, *captureMQ, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(sinkTestDSN(t)), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, MigrateSchemaForFVT(db))
	require.NoError(t, model.MigrateSchemaForFVT(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	client := &captureMQ{}
	return NewForFVT(db, client), client, db
}

// gatewayEvent is a well-formed report for a served model name.
func gatewayEvent() *server.GatewayUsageRequest {
	return &server.GatewayUsageRequest{
		RequestID:      "req-gw-1",
		OrganizationID: "org-1",
		APIKeyID:       "key-1",
		Model:          "qwen-2.5-0.5b",
		Usage: server.GatewayTokenUsage{
			PromptTokens:     11,
			CompletionTokens: 22,
			CachedTokens:     3,
			ReasoningTokens:  4,
		},
		CompletedAt:  time.Now().Add(-time.Minute).Unix(),
		LatencyMs:    900,
		TTFTMs:       120,
		GenerationMs: 780,
		Status:       "success",
	}
}

// TestRecordGatewayUsagePublishesTheConsumerShape proves the producer emits
// what the consumer decodes: if the two drifted, every voucher would be
// silently empty.
func TestRecordGatewayUsagePublishesTheConsumerShape(t *testing.T) {
	svc, client, db := newSinkTestEnv(t)
	row := &model.Model{ID: "model-gw", Name: "qwen-2.5-0.5b"}
	require.NoError(t, db.Create(row).Error)

	_, err := svc.RecordGatewayUsage(context.Background(), gatewayEvent())
	require.NoError(t, err)

	require.Len(t, client.bodies, 1)
	assert.Equal(t, mq.DefaultSubjects().MeteringEvents, client.subject[0])

	// Decode with the consumer's own struct, not a test-local copy.
	var decoded meteringEvent
	require.NoError(t, json.Unmarshal(client.bodies[0], &decoded))
	assert.Equal(t, "req-gw-1", decoded.RequestID)
	assert.Equal(t, "org-1", decoded.OrganizationID)
	assert.Equal(t, "key-1", decoded.APIKeyID)
	assert.Equal(t, "model-gw", decoded.ModelID, "the served name must be resolved to the catalog id")
	assert.EqualValues(t, 11, decoded.Usage.PromptTokens)
	assert.EqualValues(t, 22, decoded.Usage.CompletionTokens)
	assert.EqualValues(t, 3, decoded.Usage.CachedTokens)
	assert.EqualValues(t, 4, decoded.Usage.ReasoningTokens)
	assert.EqualValues(t, 900, decoded.LatencyMs)
	assert.EqualValues(t, 120, decoded.TTFTMs)
	assert.Equal(t, "success", decoded.Status)
}

// TestRecordGatewayUsageResolvesTheService covers attribution: the voucher
// should name the service that served the request when one is running.
func TestRecordGatewayUsageResolvesTheService(t *testing.T) {
	svc, client, db := newSinkTestEnv(t)
	require.NoError(t, db.Create(&model.Model{ID: "model-gw", Name: "qwen-2.5-0.5b"}).Error)
	svc.SetServiceLookup(func(_ context.Context, modelID string) string {
		if modelID == "model-gw" {
			return "svc-42"
		}
		return ""
	})

	_, err := svc.RecordGatewayUsage(context.Background(), gatewayEvent())
	require.NoError(t, err)

	var decoded meteringEvent
	require.NoError(t, json.Unmarshal(client.bodies[0], &decoded))
	assert.Equal(t, "svc-42", decoded.ServiceID)
}

// TestRecordGatewayUsageKeepsAnUnknownModel covers the alias case: refusing
// to meter a name the catalog has never seen would silently drop real
// usage, so the name is kept as-is and the row stays attributable.
func TestRecordGatewayUsageKeepsAnUnknownModel(t *testing.T) {
	svc, client, _ := newSinkTestEnv(t)

	_, err := svc.RecordGatewayUsage(context.Background(), gatewayEvent())
	require.NoError(t, err)

	var decoded meteringEvent
	require.NoError(t, json.Unmarshal(client.bodies[0], &decoded))
	assert.Equal(t, "qwen-2.5-0.5b", decoded.ModelID)
	assert.Empty(t, decoded.ServiceID)
}

// TestRecordGatewayUsageSkipsAnEmptyReport keeps empty rows out of the
// ledger: a report with neither usage nor an outcome says nothing.
func TestRecordGatewayUsageSkipsAnEmptyReport(t *testing.T) {
	svc, client, _ := newSinkTestEnv(t)

	ev := gatewayEvent()
	ev.Usage = server.GatewayTokenUsage{}
	ev.Status = ""
	_, err := svc.RecordGatewayUsage(context.Background(), ev)
	require.NoError(t, err)
	assert.Empty(t, client.bodies, "an empty report must not reach the queue")
}

// TestRecordGatewayUsageKeepsAnUnmeteredRequest is the other half of that
// rule: zero tokens with a real outcome still matters, because the consumer
// writes a request log from it (feature #12). Dropping it would erase the
// evidence that a request ran at all — for example a stream the client cut
// before the engine reported usage.
func TestRecordGatewayUsageKeepsAnUnmeteredRequest(t *testing.T) {
	svc, client, _ := newSinkTestEnv(t)

	ev := gatewayEvent()
	ev.Usage = server.GatewayTokenUsage{}
	ev.Status = "error"
	ev.Error = "client disconnected"
	_, err := svc.RecordGatewayUsage(context.Background(), ev)
	require.NoError(t, err)
	require.Len(t, client.bodies, 1)

	var decoded meteringEvent
	require.NoError(t, json.Unmarshal(client.bodies[0], &decoded))
	assert.Equal(t, "error", decoded.Status)
	assert.Equal(t, "client disconnected", decoded.Error)
}

// TestRecordGatewayUsageRequiresRequestID covers the idempotency key: an
// event without it cannot be deduplicated, so it is refused.
func TestRecordGatewayUsageRequiresRequestID(t *testing.T) {
	svc, client, _ := newSinkTestEnv(t)

	ev := gatewayEvent()
	ev.RequestID = ""
	_, err := svc.RecordGatewayUsage(context.Background(), ev)
	require.Error(t, err)
	assert.Empty(t, client.bodies)
}

// TestRecordGatewayUsageReportsAPublishFailure covers the visible-failure
// rule: the caller must be able to tell that the event never landed.
func TestRecordGatewayUsageReportsAPublishFailure(t *testing.T) {
	svc, client, _ := newSinkTestEnv(t)
	client.failNext = true

	_, err := svc.RecordGatewayUsage(context.Background(), gatewayEvent())
	require.Error(t, err)
}

// TestRecordGatewayUsageDefaultsCompletedAt keeps a report without a
// timestamp from writing a 1970 row.
func TestRecordGatewayUsageDefaultsCompletedAt(t *testing.T) {
	svc, client, _ := newSinkTestEnv(t)
	ev := gatewayEvent()
	ev.CompletedAt = 0

	before := time.Now().Add(-time.Minute).Unix()
	_, err := svc.RecordGatewayUsage(context.Background(), ev)
	require.NoError(t, err)

	var decoded meteringEvent
	require.NoError(t, json.Unmarshal(client.bodies[0], &decoded))
	assert.GreaterOrEqual(t, decoded.CompletedAt, before)
}

// TestGatewayUsageIsIdempotentThroughTheConsumer is the end-to-end
// idempotency check: the queue delivers the same event twice (at-least-once
// is the contract) and the ledger still holds one voucher.
func TestGatewayUsageIsIdempotentThroughTheConsumer(t *testing.T) {
	svc, _, _ := newSinkTestEnv(t)
	client := mq.NewFake()
	svc.publisher = client

	consumer := NewEventConsumer(client, svc, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()
	// The fake subscribes synchronously; give the goroutine one turn.
	time.Sleep(50 * time.Millisecond)

	for i := 0; i < 2; i++ {
		_, err := svc.RecordGatewayUsage(context.Background(), gatewayEvent())
		require.NoError(t, err)
	}

	assert.Eventually(t, func() bool {
		var count int64
		_ = svc.repo.DB(context.Background()).Model(&Voucher{}).Count(&count).Error
		return count == 1
	}, 2*time.Second, 20*time.Millisecond, "two deliveries of one request_id must produce one voucher")
}
