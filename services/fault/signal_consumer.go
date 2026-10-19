package fault

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// faultSignal is the structured failure signal published by the
// Controller on fault.signals (feature #53, AD12).
type faultSignal struct {
	TargetType string `json:"target_type"`
	TargetName string `json:"target_name"`
	SignalType string `json:"signal_type"`
	Detail     string `json:"detail"`
	ObservedAt string `json:"observed_at"`
}

// debounceState tracks the per-target signal counts for debouncing
// (feature #53, FR3.2).
type debounceState struct {
	mu      sync.Mutex
	counts  map[string]int
	firstAt map[string]time.Time
}

// SignalConsumer subscribes to the fault.signals subject and creates +
// diagnoses incidents (feature #53, AD13). It implements server.Runner.
type SignalConsumer struct {
	client     mq.Client
	repo       *Repository
	classifier *Classifier
	recovery   *RecoveryRunner
	workers    int
	restarts   int
	window     time.Duration
	debounce   *debounceState
}

// NewSignalConsumer constructs a SignalConsumer.
func NewSignalConsumer(client mq.Client, repo *Repository, classifier *Classifier, recovery *RecoveryRunner, workers, restarts int, window time.Duration) *SignalConsumer {
	if workers <= 0 {
		workers = 1
	}
	return &SignalConsumer{
		client:     client,
		repo:       repo,
		classifier: classifier,
		recovery:   recovery,
		workers:    workers,
		restarts:   restarts,
		window:     window,
		debounce:   &debounceState{counts: map[string]int{}, firstAt: map[string]time.Time{}},
	}
}

// NewSignalConsumerRunner builds the SignalConsumer from the shared
// server components. It returns nil when the consumer is disabled or
// the required components are unavailable, so callers can pass the
// result straight to AddRunner.
func NewSignalConsumerRunner(components server.Components) *SignalConsumer {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if !cfg.Fault.SignalsConsumer.Enabled {
		return nil
	}
	if components.MQ() == nil || components.DB() == nil {
		logger.S().Warn("fault: signal consumer disabled, mq or db component unavailable")
		return nil
	}
	client, ok := components.MQ().Client().(mq.Client)
	if !ok {
		logger.S().Fatalw("fault: unexpected mq handle type",
			"type", fmt.Sprintf("%T", components.MQ().Client()))
		return nil
	}
	raw := components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		logger.S().Fatalw("fault: unexpected db handle type",
			"type", fmt.Sprintf("%T", raw))
		return nil
	}
	repo := NewRepository(db)
	classifier := NewClassifier()
	recovery := NewRecoveryRunner(repo, client)
	return NewSignalConsumer(client, repo, classifier, recovery,
		cfg.Fault.SignalsConsumer.Workers,
		cfg.Fault.Debounce.CrashLoopRestarts,
		cfg.Fault.Debounce.CrashLoopWindow)
}

// Run implements server.Runner: it subscribes to the fault.signals
// subject and blocks until ctx is cancelled or the subscription fails.
func (c *SignalConsumer) Run(ctx context.Context) error {
	return c.client.Subscribe(ctx, mq.DefaultSubjects().FaultSignals, func(msg mq.Message) error {
		return c.handle(ctx, msg)
	})
}

// handle applies one failure signal. A malformed signal is logged and
// skipped. The signal is debounced per signal-type; once the threshold
// is reached, an incident is created and diagnosed.
func (c *SignalConsumer) handle(ctx context.Context, msg mq.Message) error {
	var sig faultSignal
	if err := json.Unmarshal(msg.Body, &sig); err != nil {
		logger.S().Warnw("fault: malformed fault signal, skipping",
			"subject", msg.Subject, "err", err)
		return nil
	}
	if sig.TargetType == "" || sig.TargetName == "" || sig.SignalType == "" {
		logger.S().Warnw("fault: invalid fault signal, skipping",
			"target_type", sig.TargetType, "target_name", sig.TargetName)
		return nil
	}
	if !c.debounced(sig.TargetType, sig.TargetName, sig.SignalType) {
		return nil
	}
	// Create the incident (status detected).
	now := time.Now().UTC()
	incident := &Incident{
		ID:         newUUID(),
		Severity:   SeverityInfo,
		Status:     StatusDetected,
		RootCause:  c.classifier.Classify(sig.SignalType),
		TargetType: sig.TargetType,
		TargetName: sig.TargetName,
		StartedAt:  now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	incident.Severity = SeverityForCause(incident.RootCause)
	if err := c.repo.CreateIncident(ctx, incident); err != nil {
		return err
	}
	// Record the detected step.
	_ = c.repo.AddStep(ctx, incident.ID, &IncidentStep{
		ID: newUUID(), StepType: StepDetected, Actor: "system",
		Detail: sig.Detail, CreatedAt: now,
	})
	// Diagnose: move to diagnosing and record the diagnosed step.
	_ = c.repo.UpdateIncidentStatus(ctx, incident.ID, StatusDiagnosing, nil)
	_ = c.repo.AddStep(ctx, incident.ID, &IncidentStep{
		ID: newUUID(), StepType: StepDiagnosed, Actor: "system",
		Detail: incident.RootCause, CreatedAt: time.Now().UTC(),
	})
	// Run the automatic recovery.
	if c.recovery != nil {
		_ = c.recovery.Run(ctx, incident)
	}
	return nil
}

// debounced reports whether a signal passes the debounce threshold
// (feature #53, FR3.2). A crash-loop is reported after N restarts in a
// window; other signal types pass immediately.
func (c *SignalConsumer) debounced(targetType, targetName, signalType string) bool {
	if signalType != "crash_loop" {
		return true
	}
	key := targetType + "/" + targetName
	c.debounce.mu.Lock()
	defer c.debounce.mu.Unlock()
	now := time.Now()
	first, ok := c.debounce.firstAt[key]
	if !ok || now.Sub(first) > c.window {
		c.debounce.firstAt[key] = now
		c.debounce.counts[key] = 1
		return false
	}
	c.debounce.counts[key]++
	return c.debounce.counts[key] >= c.restarts
}