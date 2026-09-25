package audit

import (
	"context"

	"github.com/go-taas/go-taas/pkg/logger"
)

// Recorder is the best-effort, non-fatal audit recorder invoked by
// every mutating control-plane API after the mutation succeeds (AD3).
// A recorder failure is logged and never fails or rolls back the
// mutation — the mutation is authoritative, the audit row is a trace.
type Recorder struct {
	repo *Repository
}

// NewRecorder constructs a Recorder bound to a repository.
func NewRecorder(repo *Repository) *Recorder {
	return &Recorder{repo: repo}
}

// Record writes one audit event best-effort. It never returns an error:
// a write failure is logged and skipped (AC2). The caller invokes it
// after the mutation succeeds and ignores the result.
func (r *Recorder) Record(ctx context.Context, ev *AuditEvent) {
	if r == nil || r.repo == nil {
		return
	}
	if _, err := r.repo.InsertAuditEvent(ctx, ev); err != nil {
		logger.S().Warnw("audit: recorder write failed (best-effort, mutation already committed)",
			"action", ev.Action, "resource_type", ev.ResourceType, "resource_id", ev.ResourceID, "err", err)
	}
}
