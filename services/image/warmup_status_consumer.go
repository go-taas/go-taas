package image

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// warmupStatusReport is the observed-state report published by the
// controller on image.warmup.status (architecture Section 4.5.2).
type warmupStatusReport struct {
	TaskID        string       `json:"task_id"`
	State         string       `json:"state"`
	NodeResults   []NodeResult `json:"node_results"`
	FailureReason *string      `json:"failure_reason"`
	ReportedAt    string       `json:"reported_at"`
}

// WarmupStatusConsumer subscribes to the warmup status subject and
// applies each report to the warmup_tasks table. It implements
// server.Runner so the framework starts it with the server and cancels
// it on shutdown, mirroring infer's StatusConsumer.
type WarmupStatusConsumer struct {
	client  mq.Client
	tasks   *WarmupTaskRepository
	workers int
}

// NewWarmupStatusConsumer builds a WarmupStatusConsumer. workers <= 0
// falls back to 1.
func NewWarmupStatusConsumer(client mq.Client, tasks *WarmupTaskRepository, workers int) *WarmupStatusConsumer {
	if workers <= 0 {
		workers = 1
	}
	return &WarmupStatusConsumer{client: client, tasks: tasks, workers: workers}
}

// NewWarmupStatusConsumerRunner builds the WarmupStatusConsumer from
// the shared server components. It returns nil when the consumer is
// disabled or the required components are unavailable, so callers can
// pass the result straight to AddRunner.
func NewWarmupStatusConsumerRunner(components server.Components) *WarmupStatusConsumer {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if !cfg.Image.WarmupStatusConsumer.Enabled {
		return nil
	}
	if components.MQ() == nil || components.DB() == nil {
		logger.S().Warn("image: warmup status consumer disabled, mq or db component unavailable")
		return nil
	}
	client, ok := components.MQ().Client().(mq.Client)
	if !ok {
		logger.S().Fatalw("image: unexpected mq handle type",
			"type", fmt.Sprintf("%T", components.MQ().Client()))
		return nil
	}
	raw := components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		logger.S().Fatalw("image: unexpected db handle type",
			"type", fmt.Sprintf("%T", raw))
		return nil
	}
	return NewWarmupStatusConsumer(client, NewWarmupTaskRepository(db), cfg.Image.WarmupStatusConsumer.Workers)
}

// Run implements server.Runner: it subscribes to the warmup status
// subject and blocks until ctx is cancelled or the subscription fails.
func (c *WarmupStatusConsumer) Run(ctx context.Context) error {
	return c.client.Subscribe(ctx, mq.DefaultSubjects().ImageWarmupStatus, func(msg mq.Message) error {
		return c.handle(ctx, msg)
	})
}

// handle applies one status report. Parse errors are logged and
// skipped (a malformed report never becomes well-formed — no retry).
// Unknown task ids are logged and skipped (tasks are never deleted, so
// this is defensive only). Terminal-state overwrite attempts are
// logged and skipped.
func (c *WarmupStatusConsumer) handle(ctx context.Context, msg mq.Message) error {
	var report warmupStatusReport
	if err := json.Unmarshal(msg.Body, &report); err != nil {
		logger.S().Warnw("image: malformed warmup status report, skipping",
			"subject", msg.Subject, "err", err)
		return nil
	}
	if report.TaskID == "" || !isValidReportedTaskState(report.State) {
		logger.S().Warnw("image: invalid warmup status report, skipping",
			"task_id", report.TaskID, "state", report.State)
		return nil
	}

	if err := c.tasks.ApplyStatus(ctx, report.TaskID, report.State, report.NodeResults, report.FailureReason); err != nil {
		if ae, ok := apierrors.As(err); ok && ae.Code == apierrors.CodeImageNotFound {
			// Unknown task or terminal-state overwrite: skip, do not
			// retry.
			logger.S().Infow("image: warmup status report for unknown or settled task, skipping",
				"task_id", report.TaskID)
			return nil
		}
		// Transient database failure: return the error so the broker
		// retries the delivery.
		return err
	}
	logger.S().Infow("image: warmup status applied",
		"task_id", report.TaskID, "state", report.State)
	return nil
}

// isValidReportedTaskState reports whether the controller-reported
// state is in the reportable set. pending is never reported (initial DB
// state); terminal states arrive once and are final.
func isValidReportedTaskState(state string) bool {
	switch state {
	case TaskStateRunning, TaskStateSucceeded, TaskStateFailed:
		return true
	default:
		return false
	}
}
