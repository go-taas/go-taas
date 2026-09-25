package infer

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// statusReport is the observed-state report published by the controller
// on infer.services.status (architecture Section 4.5.2).
type statusReport struct {
	ServiceID     string   `json:"service_id"`
	State         string   `json:"state"`
	Endpoints     []string `json:"endpoints"`
	FailureReason *string  `json:"failure_reason"`
	// Autoscaling is the controller-reported autoscaling status block
	// (feature #16, §6.2). Nil when the report carries no autoscaling
	// status.
	Autoscaling *autoscalingStatusReport `json:"autoscaling,omitempty"`
	ReportedAt  string                   `json:"reported_at"`
}

// autoscalingStatusReport is the autoscaling status block inside a
// status report (feature #16, §6.2).
type autoscalingStatusReport struct {
	State              string `json:"state"`
	CurrentReplicas    int    `json:"current_replicas"`
	DesiredReplicas    int    `json:"desired_replicas"`
	CurrentConcurrency int    `json:"current_concurrency"`
	TargetConcurrency  int    `json:"target_concurrency"`
	LastScalingEventAt string `json:"last_scaling_event_at"`
	ErrorReason        string `json:"error_reason"`
}

// StatusConsumer subscribes to the status subject and applies each
// report to the inference_services table. It implements server.Runner so
// the framework starts it with the server and cancels it on shutdown.
type StatusConsumer struct {
	client  mq.Client
	repo    *InferenceServiceRepository
	workers int
}

// NewStatusConsumer constructs a StatusConsumer. workers <= 0 falls back
// to 1 (sequential handling).
func NewStatusConsumer(client mq.Client, repo *InferenceServiceRepository, workers int) *StatusConsumer {
	if workers <= 0 {
		workers = 1
	}
	return &StatusConsumer{client: client, repo: repo, workers: workers}
}

// NewStatusConsumerRunner builds the StatusConsumer from the shared
// server components. It returns nil when the consumer is disabled or
// the required components are unavailable, so callers can pass the
// result straight to AddRunner.
func NewStatusConsumerRunner(components server.Components) *StatusConsumer {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if !cfg.Infer.StatusConsumer.Enabled {
		return nil
	}
	if components.MQ() == nil || components.DB() == nil {
		logger.S().Warn("infer: status consumer disabled, mq or db component unavailable")
		return nil
	}
	client, ok := components.MQ().Client().(mq.Client)
	if !ok {
		logger.S().Fatalw("infer: unexpected mq handle type",
			"type", fmt.Sprintf("%T", components.MQ().Client()))
		return nil
	}
	raw := components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		logger.S().Fatalw("infer: unexpected db handle type",
			"type", fmt.Sprintf("%T", raw))
		return nil
	}
	repo := NewInferenceServiceRepository(db)
	return NewStatusConsumer(client, repo, cfg.Infer.StatusConsumer.Workers)
}

// Run implements server.Runner: it subscribes to the status subject and
// blocks until ctx is cancelled or the subscription fails.
func (c *StatusConsumer) Run(ctx context.Context) error {
	return c.client.Subscribe(ctx, mq.DefaultSubjects().InferServiceStatus, func(msg mq.Message) error {
		return c.handle(ctx, msg)
	})
}

// handle applies one status report. Parse errors are logged and skipped
// (a malformed report never becomes well-formed — no retry). Unknown
// service ids are logged and skipped (a deleted service's late report).
func (c *StatusConsumer) handle(ctx context.Context, msg mq.Message) error {
	var report statusReport
	if err := json.Unmarshal(msg.Body, &report); err != nil {
		logger.S().Warnw("infer: malformed status report, skipping",
			"subject", msg.Subject, "err", err)
		return nil
	}
	if report.ServiceID == "" || !isValidReportedState(report.State) {
		logger.S().Warnw("infer: invalid status report, skipping",
			"service_id", report.ServiceID, "state", report.State)
		return nil
	}

	if err := c.repo.ApplyStatus(ctx, report.ServiceID, report.State, report.Endpoints, report.FailureReason, report.Autoscaling); err != nil {
		if ae, ok := apierrors.As(err); ok && ae.Code == apierrors.CodeInferServiceNotFound {
			// Late report for a deleted service: skip, do not retry.
			logger.S().Infow("infer: status report for unknown service, skipping",
				"service_id", report.ServiceID)
			return nil
		}
		// Transient database failure: return the error so the broker
		// retries the delivery.
		return err
	}
	logger.S().Infow("infer: status applied",
		"service_id", report.ServiceID, "state", report.State)
	return nil
}

// isValidReportedState reports whether the controller-reported state is
// in the reportable set. pending is never reported (initial DB state);
// terminated is set by the delete RPC, not by the controller.
func isValidReportedState(state string) bool {
	switch state {
	case StateDeploying, StateRunning, StateFailed:
		return true
	default:
		return false
	}
}
