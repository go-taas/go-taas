package tenancy

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// concurrencyReport is the gateway's per-org in-flight report payload
// published on quota.orgs.concurrency (feature #51, AD14).
type concurrencyReport struct {
	OrganizationID string `json:"organization_id"`
	InFlight       int64  `json:"in_flight"`
	ReportedAt     int64  `json:"reported_at"`
}

// concurrencySnapshot is the latest in-flight report for one org.
type concurrencySnapshot struct {
	inFlight   int64
	reportedAt time.Time
}

// ConcurrencyConsumer keeps the latest per-org in-flight snapshot from
// the gateway's quota.orgs.concurrency reports (feature #51, AD14). It
// implements server.Runner. The snapshot is best-effort display data —
// enforcement is gateway-local, never this map.
type ConcurrencyConsumer struct {
	mu         sync.RWMutex
	snapshots  map[string]concurrencySnapshot
	staleAfter time.Duration
	client     mq.Client
}

// NewConcurrencyConsumer constructs a ConcurrencyConsumer bound to an
// mq client. staleAfter <= 0 falls back to 2 minutes.
func NewConcurrencyConsumer(client mq.Client, staleAfter time.Duration) *ConcurrencyConsumer {
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	return &ConcurrencyConsumer{
		snapshots:  make(map[string]concurrencySnapshot),
		staleAfter: staleAfter,
		client:     client,
	}
}

// NewConcurrencyConsumerRunner builds the consumer from the shared
// server components. It returns nil when the MQ component is
// unavailable, so callers can pass the result straight to AddRunner.
func NewConcurrencyConsumerRunner(components server.Components) *ConcurrencyConsumer {
	if components == nil {
		return nil
	}
	if components.MQ() == nil {
		logger.S().Warn("tenancy: quota concurrency consumer disabled, mq component unavailable")
		return nil
	}
	client, ok := components.MQ().Client().(mq.Client)
	if !ok {
		logger.S().Fatalw("tenancy: unexpected mq handle type",
			"type", fmt.Sprintf("%T", components.MQ().Client()))
		return nil
	}
	return NewConcurrencyConsumer(client, 0)
}

// Run implements server.Runner: it subscribes to quota.orgs.concurrency
// and blocks until ctx is cancelled or the subscription fails. A plain
// subscribe (no queue group) so every replica keeps its own map.
func (c *ConcurrencyConsumer) Run(ctx context.Context) error {
	return c.client.Subscribe(ctx, mq.DefaultSubjects().QuotaOrgsConcurrency, func(msg mq.Message) error {
		c.handle(msg)
		return nil
	})
}

// HandleForTest applies one in-flight report synchronously. It exists
// so full-verification tests can drive the consumer without a broker.
func (c *ConcurrencyConsumer) HandleForTest(msg mq.Message) {
	c.handle(msg)
}

// handle stores the latest report for the org. Malformed reports are
// logged and skipped.
func (c *ConcurrencyConsumer) handle(msg mq.Message) {
	var rep concurrencyReport
	if err := json.Unmarshal(msg.Body, &rep); err != nil {
		logger.S().Warnw("tenancy: malformed concurrency report, skipping",
			"subject", msg.Subject, "err", err)
		return
	}
	if rep.OrganizationID == "" {
		logger.S().Warnw("tenancy: concurrency report without organization, skipping",
			"subject", msg.Subject)
		return
	}
	c.mu.Lock()
	c.snapshots[rep.OrganizationID] = concurrencySnapshot{
		inFlight:   rep.InFlight,
		reportedAt: time.Now().UTC(),
	}
	c.mu.Unlock()
}

// CurrentInFlight returns the org's latest in-flight count, or 0 when
// there is no entry or the entry is stale (older than staleAfter)
// (feature #51, FR2.4).
func (c *ConcurrencyConsumer) CurrentInFlight(orgID string) int64 {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	snap, ok := c.snapshots[orgID]
	if !ok {
		return 0
	}
	if time.Since(snap.reportedAt) > c.staleAfter {
		return 0
	}
	return snap.inFlight
}
