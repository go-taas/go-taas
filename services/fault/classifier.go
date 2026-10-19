package fault

// Classifier is the deterministic root-cause classifier (feature #53,
// AD12). It maps a signal type to a root-cause category via a closed
// switch — auditable and testable, no hidden judge.
type Classifier struct{}

// NewClassifier constructs a Classifier.
func NewClassifier() *Classifier { return &Classifier{} }

// Classify maps a signal type to a root-cause category (AD12). Unknown
// signal types map to CauseUnknown.
func (c *Classifier) Classify(signalType string) string {
	switch signalType {
	case "crash_loop":
		return CauseServiceCrashLoop
	case "node_not_ready":
		return CauseNodeNotReady
	case "device_plugin_unhealthy":
		return CauseAcceleratorDevicePluginFailure
	case "oom_killed":
		return CauseOOM
	case "image_pull_backoff":
		return CauseImagePullFailure
	default:
		return CauseUnknown
	}
}

// SeverityForCause maps a root-cause category to a severity (feature
// #53, FR1.1). Node/accelerator failures are critical; service failures
// are warning; unknown is info.
func SeverityForCause(cause string) string {
	switch cause {
	case CauseNodeNotReady, CauseAcceleratorDevicePluginFailure:
		return SeverityCritical
	case CauseServiceCrashLoop, CauseOOM, CauseImagePullFailure:
		return SeverityWarning
	default:
		return SeverityInfo
	}
}

// DefaultActionForCause returns the default recovery action for a
// root-cause category (feature #53, FR4.1). Unknown returns "" (no
// automatic action).
func DefaultActionForCause(cause string) string {
	switch cause {
	case CauseServiceCrashLoop, CauseOOM, CauseImagePullFailure:
		return ActionRestart
	case CauseNodeNotReady, CauseAcceleratorDevicePluginFailure:
		return ActionReschedule
	default:
		return ""
	}
}

// ValidRootCause reports whether a category is a valid root cause.
func ValidRootCause(cause string) bool {
	switch cause {
	case CauseServiceCrashLoop, CauseNodeNotReady, CauseAcceleratorDevicePluginFailure,
		CauseOOM, CauseImagePullFailure, CauseUnknown:
		return true
	default:
		return false
	}
}

// ValidStatus reports whether a status filter value is valid.
func ValidStatus(status string) bool {
	switch status {
	case StatusDetected, StatusDiagnosing, StatusRecovering, StatusResolved, StatusFailed:
		return true
	default:
		return false
	}
}

// ValidSeverity reports whether a severity filter value is valid.
func ValidSeverity(severity string) bool {
	switch severity {
	case SeverityCritical, SeverityWarning, SeverityInfo:
		return true
	default:
		return false
	}
}

// ValidTargetType reports whether a target-type filter value is valid.
func ValidTargetType(targetType string) bool {
	switch targetType {
	case TargetService, TargetNode, TargetAccelerator:
		return true
	default:
		return false
	}
}

// ValidAction reports whether a recovery action is valid.
func ValidAction(action string) bool {
	switch action {
	case ActionRestart, ActionReschedule, ActionDrain:
		return true
	default:
		return false
	}
}