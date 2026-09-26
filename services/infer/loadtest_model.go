package infer

import "time"

// Load-test lifecycle states (closed set, feature #20 AD3).
const (
	LoadTestStatePending   = "pending"
	LoadTestStateRunning   = "running"
	LoadTestStateCompleted = "completed"
	LoadTestStateFailed    = "failed"
	LoadTestStateStopped   = "stopped"
)

// Load-test configuration bounds (feature #20 AD5).
const (
	loadTestMinConcurrency = 1
	loadTestMaxConcurrency = 1000
	loadTestMinDuration    = 5
	loadTestMaxDuration    = 3600
	loadTestMinRate        = 0
	loadTestMaxRate        = 10000
	loadTestMaxPromptLen   = 4096
	loadTestMinMaxTokens   = 1
	loadTestMaxMaxTokens   = 8192
)

// LoadTest is the GORM model of the load_tests table and the single
// source of truth for its schema (created by AutoMigrate, no
// hand-written DDL). Feature #20, §5.1.
//
// service_id and model_id are plain uuid strings with no hard foreign
// key: a run is a historical record and must survive its service being
// deleted, so service_name and model_name are denormalized for the
// history list.
type LoadTest struct {
	// ID is the server-generated UUID v4 exposed as load_test_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// ServiceID is the target inference service id.
	ServiceID string `gorm:"type:uuid;not null;index:idx_load_tests_service_created,priority:1"`
	// ServiceName is the denormalized service name for list/search.
	ServiceName string `gorm:"size:63;not null"`
	// ModelID is the deployed model id (denormalized from the service).
	ModelID string `gorm:"type:uuid;not null;index:idx_load_tests_model_state_created,priority:1"`
	// ModelName is the denormalized model name.
	ModelName string `gorm:"size:128;not null"`
	// Concurrency is the worker-pool size, 1-1000 (AD5).
	Concurrency int `gorm:"not null"`
	// DurationSeconds is the run length, 5-3600 (AD5).
	DurationSeconds int `gorm:"not null"`
	// RequestRate is the target req/s; 0 = as fast as possible (AD5).
	RequestRate int `gorm:"not null"`
	// PromptTemplate is the prompt sent on each request, <= 4096 chars.
	PromptTemplate string `gorm:"type:text;not null"`
	// MaxTokens caps each completion, 1-8192 (AD5).
	MaxTokens int `gorm:"not null"`
	// State is the run state (closed set, AD3).
	State string `gorm:"size:16;not null;default:'pending';index:idx_load_tests_state_created,priority:1;index:idx_load_tests_model_state_created,priority:2"`
	// FailureReason is the human-readable reason when state=failed.
	FailureReason *string `gorm:"size:512"`
	// StartedAt is when the runner picked up the run.
	StartedAt *time.Time
	// CompletedAt is when the run reached a terminal state.
	CompletedAt *time.Time

	// Result columns. Zeroed while pending/running; meaningful once
	// completed (AD9) or partial when failed/stopped.
	TotalRequests      int64   `gorm:"not null;default:0"`
	SuccessCount       int64   `gorm:"not null;default:0"`
	FailureCount       int64   `gorm:"not null;default:0"`
	ErrorRate          float64 `gorm:"not null;default:0"`
	ThroughputRPS      float64 `gorm:"not null;default:0"`
	OutputTokensPerSec float64 `gorm:"not null;default:0"`
	InputTokens        int64   `gorm:"not null;default:0"`
	OutputTokens       int64   `gorm:"not null;default:0"`
	LatencyP50Ms       float64 `gorm:"not null;default:0"`
	LatencyP90Ms       float64 `gorm:"not null;default:0"`
	LatencyP95Ms       float64 `gorm:"not null;default:0"`
	LatencyP99Ms       float64 `gorm:"not null;default:0"`

	// CreatedAt is the creation time (UTC).
	CreatedAt time.Time `gorm:"index:idx_load_tests_service_created,priority:2,sort:DESC;index:idx_load_tests_model_state_created,priority:3,sort:DESC;index:idx_load_tests_state_created,priority:2,sort:DESC"`
	// UpdatedAt is the last update (progress tick or terminal write).
	UpdatedAt time.Time
}

// TableName returns the table name of LoadTest.
func (LoadTest) TableName() string { return "load_tests" }

// IsTerminal reports whether the state is a terminal state (completed,
// failed or stopped).
func (l *LoadTest) IsTerminal() bool {
	switch l.State {
	case LoadTestStateCompleted, LoadTestStateFailed, LoadTestStateStopped:
		return true
	default:
		return false
	}
}

// LoadTestProgress is the live in-memory progress snapshot of a running
// run (AD12).
type LoadTestProgress struct {
	RequestsSent   int64
	SuccessCount   int64
	FailureCount   int64
	ElapsedSeconds int64
}

// LoadTestResult is the aggregated final result set (AD6).
type LoadTestResult struct {
	TotalRequests      int64
	SuccessCount       int64
	FailureCount       int64
	ErrorRate          float64
	ThroughputRPS      float64
	OutputTokensPerSec float64
	InputTokens        int64
	OutputTokens       int64
	LatencyP50Ms       float64
	LatencyP90Ms       float64
	LatencyP95Ms       float64
	LatencyP99Ms       float64
}
