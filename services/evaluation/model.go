package evaluation

//revive:disable:exported

import "time"

const (
	RunPending             = "pending"
	RunRunning             = "running"
	RunCompleted           = "completed"
	RunCompletedWithErrors = "completed_with_errors"
	RunFailed              = "failed"
	RunCancelled           = "cancelled"
	CasePending            = "pending"
	CasePassed             = "passed"
	CaseFailed             = "failed"
	CaseError              = "error"
	CaseCancelled          = "cancelled"
)

type Evaluation struct {
	EvaluationID   string     `gorm:"primaryKey;type:uuid"`
	OrganizationID string     `gorm:"size:64;not null;index:idx_evaluations_org_created,priority:1"`
	Name           string     `gorm:"size:128;not null"`
	Description    string     `gorm:"size:500;not null;default:''"`
	PromptID       string     `gorm:"type:uuid;not null;index"`
	PromptVersion  int        `gorm:"not null"`
	CreatedBy      string     `gorm:"size:64;not null"`
	CreatedAt      time.Time  `gorm:"not null;index:idx_evaluations_org_created,priority:2"`
	UpdatedAt      time.Time  `gorm:"not null"`
	DeletedAt      *time.Time `gorm:"index"`
}

func (Evaluation) TableName() string { return "evaluations" }

type EvaluationCase struct {
	CaseID         string    `gorm:"primaryKey;type:uuid"`
	EvaluationID   string    `gorm:"type:uuid;not null;index:idx_evaluation_cases_position,priority:1"`
	OrganizationID string    `gorm:"size:64;not null;index"`
	Position       int       `gorm:"not null;index:idx_evaluation_cases_position,priority:2"`
	Name           string    `gorm:"size:128;not null"`
	Variables      string    `gorm:"type:jsonb;not null"`
	ExpectedOutput string    `gorm:"type:text;not null;default:''"`
	Checks         string    `gorm:"type:jsonb;not null"`
	CreatedAt      time.Time `gorm:"not null"`
	UpdatedAt      time.Time `gorm:"not null"`
}

func (EvaluationCase) TableName() string { return "evaluation_cases" }

type EvaluationRun struct {
	RunID                 string     `gorm:"primaryKey;type:uuid"`
	EvaluationID          string     `gorm:"type:uuid;not null;index:idx_evaluation_runs_suite_created,priority:1"`
	OrganizationID        string     `gorm:"size:64;not null;index:idx_evaluation_runs_org_status,priority:1"`
	Status                string     `gorm:"size:32;not null;index:idx_evaluation_runs_org_status,priority:2"`
	PromptID              string     `gorm:"type:uuid;not null"`
	PromptVersion         int        `gorm:"not null"`
	PromptContentSnapshot string     `gorm:"type:text;not null"`
	PromptVariables       string     `gorm:"type:jsonb;not null"`
	ModelID               string     `gorm:"size:128;not null"`
	APIKeyID              string     `gorm:"type:uuid;not null"`
	CaseCount             int        `gorm:"not null"`
	CompletedCount        int        `gorm:"not null;default:0"`
	PassedCount           int        `gorm:"not null;default:0"`
	FailedCount           int        `gorm:"not null;default:0"`
	ErrorCount            int        `gorm:"not null;default:0"`
	CancelledCount        int        `gorm:"not null;default:0"`
	TotalCostMinor        int64      `gorm:"not null;default:0"`
	Currency              string     `gorm:"size:8;not null;default:USD"`
	MedianLatencyMS       int64      `gorm:"not null;default:0"`
	CreatedBy             string     `gorm:"size:64;not null"`
	CreatedAt             time.Time  `gorm:"not null;index:idx_evaluation_runs_suite_created,priority:2"`
	StartedAt             *time.Time `gorm:"index"`
	CompletedAt           *time.Time `gorm:"index"`
	RetainedUntil         time.Time  `gorm:"not null;index"`
	CancelRequestedAt     *time.Time
	LeaseOwner            string     `gorm:"size:128"`
	LeaseUntil            *time.Time `gorm:"index"`
}

func (EvaluationRun) TableName() string { return "evaluation_runs" }

type EvaluationRunCase struct {
	ResultID       string     `gorm:"primaryKey;type:uuid"`
	RunID          string     `gorm:"type:uuid;not null;index:idx_evaluation_run_cases_position,priority:1"`
	CaseID         *string    `gorm:"type:uuid;index"`
	CasePosition   int        `gorm:"not null;index:idx_evaluation_run_cases_position,priority:2"`
	CaseName       string     `gorm:"size:128;not null"`
	Variables      string     `gorm:"type:jsonb;not null"`
	ExpectedOutput string     `gorm:"type:text;not null;default:''"`
	Checks         string     `gorm:"type:jsonb;not null"`
	Status         string     `gorm:"size:16;not null;index"`
	Completion     string     `gorm:"type:text;not null;default:''"`
	CheckResults   string     `gorm:"type:jsonb;not null;default:'[]'"`
	InputTokens    int64      `gorm:"not null;default:0"`
	OutputTokens   int64      `gorm:"not null;default:0"`
	LatencyMS      int64      `gorm:"not null;default:0"`
	CostMinor      int64      `gorm:"not null;default:0"`
	Currency       string     `gorm:"size:8;not null;default:USD"`
	ErrorCode      int32      `gorm:"not null;default:0"`
	ErrorMessage   string     `gorm:"size:512;not null;default:''"`
	CreatedAt      time.Time  `gorm:"not null"`
	CompletedAt    *time.Time `gorm:"index"`
}

func (EvaluationRunCase) TableName() string { return "evaluation_run_cases" }
