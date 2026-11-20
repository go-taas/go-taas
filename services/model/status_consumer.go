package model

import (
	"context"
	"fmt"
	"time"

	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// statusConsumer applies controller status reports for one subject. It
// implements server.Runner so the server supervises it.
type statusConsumer struct {
	client  mq.Client
	service *Service
	subject string
	// group is the consumer group; it keeps the model module's queue
	// independent of the controller's subscriptions.
	group string
	apply func(ctx context.Context, body []byte) error
}

// Run implements server.Runner.
func (c *statusConsumer) Run(ctx context.Context) error {
	return mq.SubscribeInGroup(ctx, c.client, c.subject, c.group, func(msg mq.Message) error {
		if err := c.apply(ctx, msg.Body); err != nil {
			// A malformed or unknown-id report can never become valid;
			// do not make the broker redeliver it forever.
			logger.S().Warnw("model: dropping invalid status report",
				"subject", msg.Subject, "err", err)
		}
		return nil
	})
}

// NewModelTransferStatusConsumerRunner builds the transfer status consumer
// from the shared components, or nil when MQ is unavailable.
func NewModelTransferStatusConsumerRunner(components server.Components, svc *Service) server.Runner {
	client := mqClient(components)
	if client == nil || svc == nil {
		return nil
	}
	return &statusConsumer{
		client:  client,
		service: svc,
		subject: mq.DefaultSubjects().ModelTransferStatus,
		group:   "model-transfer-status",
		apply:   svc.HandleModelTransferStatus,
	}
}

// NewModelWarmupStatusConsumerRunner builds the warmup status consumer
// from the shared components, or nil when MQ is unavailable.
func NewModelWarmupStatusConsumerRunner(components server.Components, svc *Service) server.Runner {
	client := mqClient(components)
	if client == nil || svc == nil {
		return nil
	}
	return &statusConsumer{
		client:  client,
		service: svc,
		subject: mq.DefaultSubjects().ModelWarmupStatus,
		group:   "model-warmup-status",
		apply:   svc.HandleModelWarmupStatus,
	}
}

// NewModelWarmupNodeStatusConsumerRunner builds the LocalModelNode
// snapshot consumer, or nil when MQ is unavailable.
func NewModelWarmupNodeStatusConsumerRunner(components server.Components, svc *Service) server.Runner {
	client := mqClient(components)
	if client == nil || svc == nil {
		return nil
	}
	return &statusConsumer{
		client:  client,
		service: svc,
		subject: mq.DefaultSubjects().ModelWarmupNodes,
		group:   "model-warmup-nodes",
		apply:   svc.HandleWarmupNodeSnapshot,
	}
}

// warmupNodeGroupSyncRunner republishes the platform-managed node groups so
// a controller that (re)starts after the original edit still converges.
type warmupNodeGroupSyncRunner struct {
	service *Service
}

// Run implements server.Runner.
func (r *warmupNodeGroupSyncRunner) Run(ctx context.Context) error {
	return r.service.RunWarmupNodeGroupSync(ctx, 30*time.Second)
}

// NewWarmupNodeGroupSyncRunner builds the node-group sync runner, or nil
// when MQ is unavailable.
func NewWarmupNodeGroupSyncRunner(components server.Components, svc *Service) server.Runner {
	if mqClient(components) == nil || svc == nil {
		return nil
	}
	return &warmupNodeGroupSyncRunner{service: svc}
}

// mqClient unwraps the shared MQ component.
func mqClient(components server.Components) mq.Client {
	if components == nil || components.MQ() == nil {
		return nil
	}
	client, ok := components.MQ().Client().(mq.Client)
	if !ok {
		logger.S().Errorw("model: unexpected mq handle type", "type", fmt.Sprintf("%T", components.MQ().Client()))
		return nil
	}
	return client
}
