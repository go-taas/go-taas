package infer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)


// TestIsValidReportedState verifies the set of states a controller report
// may carry: the scheduling and engine-start phases (queued, starting)
// included, pending and terminated excluded because the RPCs own them.
func TestIsValidReportedState(t *testing.T) {
	for _, state := range []string{StateQueued, StateStarting, StateDeploying, StateRunning, StateFailed} {
		assert.True(t, isValidReportedState(state), "%q must be reportable", state)
	}
	for _, state := range []string{StatePending, StateTerminated, "", "bogus"} {
		assert.False(t, isValidReportedState(state), "%q must not be reportable", state)
	}
}
