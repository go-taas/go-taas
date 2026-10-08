package infer

import (
	"context"
	"strconv"
	"time"

	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// Routing policy publisher defaults (feature #45, §9).
const (
	routingPublisherInterval = 5 * time.Second
	routingPublisherBatch    = 50
)

// RoutingPolicyPublisher publishes pending outbox snapshots to the
// dedicated infer.routing.policies subject in revision order per model,
// marking delivery after a successful MQ publish (feature #45, §13.1).
// It is a server.Runner: the framework starts Run with the server and
// cancels it on shutdown. Duplicate delivery is tolerated by the
// gateway adapter (revision-ordered snapshots), so retries are safe.
type RoutingPolicyPublisher struct {
	repo     *RoutingPolicyRepository
	client   mq.Client
	interval time.Duration
}

// NewRoutingPolicyPublisher constructs a RoutingPolicyPublisher.
// interval <= 0 falls back to 5s.
func NewRoutingPolicyPublisher(repo *RoutingPolicyRepository, client mq.Client, interval time.Duration) *RoutingPolicyPublisher {
	if interval <= 0 {
		interval = routingPublisherInterval
	}
	return &RoutingPolicyPublisher{repo: repo, client: client, interval: interval}
}

// RunnerName implements server.Runner.
func (p *RoutingPolicyPublisher) RunnerName() string { return "infer-routing-policy-publisher" }

// Run implements server.Runner: it publishes pending snapshots on the
// interval until ctx is cancelled.
func (p *RoutingPolicyPublisher) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	p.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.RunOnce(ctx)
		}
	}
}

// RunOnce publishes one batch of pending outbox rows. It is extracted
// for tests.
func (p *RoutingPolicyPublisher) RunOnce(ctx context.Context) {
	rows, err := p.repo.ClaimPendingOutbox(ctx, routingPublisherBatch)
	if err != nil {
		logger.S().Warnw("infer: routing-policy outbox claim failed", "err", err)
		return
	}
	for _, row := range rows {
		headers := map[string]string{
			"model_id": row.ModelID,
			"revision": formatRevisionHeader(row.Revision),
			"event":    "routing_policy.updated",
		}
		if err := p.client.Publish(ctx, mq.DefaultSubjects().InferRoutingPolicies, []byte(row.Snapshot), headers); err != nil {
			// The outbox row stays pending; the next tick retries with
			// the attempt counter and last error recorded (feature
			// #45, AD5: MQ failures never roll back a committed
			// policy).
			if recErr := p.repo.RecordOutboxAttempt(ctx, row.ID, err); recErr != nil {
				logger.S().Warnw("infer: routing-policy outbox attempt record failed", "err", recErr)
			}
			logger.S().Warnw("infer: routing-policy publish failed, will retry",
				"model_id", row.ModelID, "revision", row.Revision, "err", err)
			continue
		}
		if err := p.repo.MarkOutboxPublished(ctx, row.ID); err != nil {
			logger.S().Warnw("infer: routing-policy outbox mark failed", "err", err)
		}
	}
}

var _ server.Runner = (*RoutingPolicyPublisher)(nil)

// formatRevisionHeader renders the revision for the message header.
func formatRevisionHeader(rev int64) string {
	return strconv.FormatInt(rev, 10)
}
