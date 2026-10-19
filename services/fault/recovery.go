package fault

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-taas/go-taas/pkg/mq"
)

// RecoveryRunner executes recovery actions (feature #53, AD14). It
// holds the policy store, the MQ publisher, the deployment-trail seam,
// and the audit recorder.
type RecoveryRunner struct {
	repo            *Repository
	publisher       mq.Client
	deploymentTrail DeploymentTrail
}

// NewRecoveryRunner constructs a RecoveryRunner.
func NewRecoveryRunner(repo *Repository, publisher mq.Client) *RecoveryRunner {
	return &RecoveryRunner{repo: repo, publisher: publisher}
}

// SetDeploymentTrail injects the infer deployment-history seam (AD16).
func (r *RecoveryRunner) SetDeploymentTrail(t DeploymentTrail) { r.deploymentTrail = t }

// Run executes the automatic recovery action for an incident's root
// cause (AD14). It reads the policy, publishes the action, and records
// the timeline step. Drain is never automatic (FR4.4).
func (r *RecoveryRunner) Run(ctx context.Context, incident *Incident) error {
	policy, err := r.repo.GetPolicy(ctx, nil)
	if err != nil {
		return err
	}
	action := DefaultActionForCause(incident.RootCause)
	if action == "" {
		// No automatic action for this category; offer a manual one.
		return nil
	}
	enabled := false
	switch action {
	case ActionRestart:
		enabled = policyToggle(policy.AutoRestart, incident.RootCause)
	case ActionReschedule:
		enabled = policyToggle(policy.AutoReschedule, incident.RootCause)
	}
	if !enabled {
		return nil
	}
	// Publish the recovery action.
	if r.publisher != nil {
		body, _ := json.Marshal(map[string]any{
			"incident_id": incident.ID,
			"action":      action,
			"target_type": incident.TargetType,
			"target_name": incident.TargetName,
		})
		_ = r.publisher.Publish(ctx, mq.DefaultSubjects().FaultActions, body, nil)
	}
	// Record the recovery_started step.
	step := &IncidentStep{
		ID:        newUUID(),
		StepType:  StepRecoveryStarted,
		Actor:     "system",
		Detail:    action,
		CreatedAt: time.Now().UTC(),
	}
	_ = r.repo.AddStep(ctx, incident.ID, step)
	_ = r.repo.UpdateIncidentStatus(ctx, incident.ID, StatusRecovering, nil)
	_ = r.repo.UpdateIncidentAutoRecovered(ctx, incident.ID, true)
	// Record the deployment-history trail for service targets (AD16).
	if r.deploymentTrail != nil && incident.TargetType == TargetService {
		_ = r.deploymentTrail.RecordDeploymentEvent(ctx, incident.TargetName, "deployment.updated", "recovery", action)
	}
	return nil
}

// policyToggle reads a per-category toggle from a JSON map.
func policyToggle(raw []byte, category string) bool {
	m := map[string]bool{}
	_ = json.Unmarshal(raw, &m)
	return m[category]
}