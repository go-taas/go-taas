package fault

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// IncidentListFilter narrows ListIncidents (feature #53, FR1.1).
type IncidentListFilter struct {
	Status     string
	Severity   string
	RootCause  string
	TargetType string
	Search     string
	Offset     int
	Limit      int
}

// Repository persists incidents, steps and the fault policy on top of
// the generic repository base.
type Repository struct {
	*database.BaseRepository[Incident]
	db *database.Manager
}

// NewRepository constructs a Repository bound to a database Manager.
func NewRepository(db *gorm.DB) *Repository {
	mgr := database.NewManager(db)
	return &Repository{
		BaseRepository: database.NewBaseRepository[Incident](mgr),
		db:             mgr,
	}
}

// ListIncidents returns the incident rows matching the filter, ordered
// by started_at DESC, paginated with a total count (feature #53, FR1.1).
func (r *Repository) ListIncidents(ctx context.Context, filter IncidentListFilter) ([]*Incident, int64, error) {
	query := r.db.DB(ctx).Model(&Incident{})
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.Severity != "" {
		query = query.Where("severity = ?", filter.Severity)
	}
	if filter.RootCause != "" {
		query = query.Where("root_cause = ?", filter.RootCause)
	}
	if filter.TargetType != "" {
		query = query.Where("target_type = ?", filter.TargetType)
	}
	if filter.Search != "" {
		like := "%" + strings.ToLower(filter.Search) + "%"
		query = query.Where("LOWER(target_name) LIKE ?", like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*Incident
	if err := query.Order("started_at DESC").Offset(filter.Offset).Limit(filter.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// GetIncident returns one incident row. gorm.ErrRecordNotFound passes
// through; the caller maps it to CodeFaultNotFound. A malformed
// (non-UUID) id maps there too, because it can never match a stored row
// and a raw comparison would surface a postgres uuid cast error.
func (r *Repository) GetIncident(ctx context.Context, incidentID string) (*Incident, error) {
	if _, err := uuid.Parse(incidentID); err != nil {
		return nil, gorm.ErrRecordNotFound
	}
	var row Incident
	err := r.db.DB(ctx).Where("id = ?", incidentID).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// CreateIncident inserts a new incident row.
func (r *Repository) CreateIncident(ctx context.Context, incident *Incident) error {
	return r.Create(ctx, incident)
}

// UpdateIncidentStatus updates the incident's status and optional
// resolution time.
func (r *Repository) UpdateIncidentStatus(ctx context.Context, incidentID, status string, resolvedAt *time.Time) error {
	fields := map[string]any{"status": status, "updated_at": time.Now().UTC()}
	if resolvedAt != nil {
		fields["resolved_at"] = *resolvedAt
	}
	return r.db.DB(ctx).Model(&Incident{}).Where("id = ?", incidentID).Updates(fields).Error
}

// UpdateIncidentAutoRecovered marks the incident as auto-recovered.
func (r *Repository) UpdateIncidentAutoRecovered(ctx context.Context, incidentID string, autoRecovered bool) error {
	return r.db.DB(ctx).Model(&Incident{}).Where("id = ?", incidentID).
		Updates(map[string]any{"auto_recovered": autoRecovered, "updated_at": time.Now().UTC()}).Error
}

// ListSteps returns the incident's timeline, ordered by created_at ASC.
func (r *Repository) ListSteps(ctx context.Context, incidentID string) ([]*IncidentStep, error) {
	var rows []*IncidentStep
	err := r.db.DB(ctx).Where("incident_id = ?", incidentID).Order("created_at ASC").Find(&rows).Error
	return rows, err
}

// AddStep appends one timeline step.
func (r *Repository) AddStep(ctx context.Context, incidentID string, step *IncidentStep) error {
	step.IncidentID = incidentID
	return r.db.DB(ctx).Create(step).Error
}

// ListActions returns the incident's recovery actions (feature #53,
// AD14). Actions are stored as steps with step_type recovery_*; this
// reads them back as RecoveryAction rows.
func (r *Repository) ListActions(ctx context.Context, incidentID string) ([]RecoveryAction, error) {
	steps, err := r.ListSteps(ctx, incidentID)
	if err != nil {
		return nil, err
	}
	var out []RecoveryAction
	for _, s := range steps {
		switch s.StepType {
		case StepRecoveryStarted, StepRecoverySucceeded, StepRecoveryFailed, StepOverridden, StepManualAction:
			out = append(out, RecoveryAction{
				Action:    s.Detail,
				Status:    actionStatusForStep(s.StepType),
				Actor:     s.Actor,
				CreatedAt: s.CreatedAt,
			})
		}
	}
	return out, nil
}

// RecoveryAction is one recovery action taken or pending (feature #53,
// AD14).
type RecoveryAction struct {
	Action    string
	Status    string
	Actor     string
	CreatedAt time.Time
}

// actionStatusForStep maps a step type to a recovery-action status.
func actionStatusForStep(stepType string) string {
	switch stepType {
	case StepRecoveryStarted:
		return ActionStatusRunning
	case StepRecoverySucceeded:
		return ActionStatusSucceeded
	case StepRecoveryFailed:
		return ActionStatusFailed
	case StepOverridden:
		return ActionStatusOverridden
	case StepManualAction:
		return ActionStatusSucceeded
	default:
		return ActionStatusPending
	}
}

// GetPolicy returns the single-row fault policy (AD15). It seeds the
// row with the shipped defaults when absent.
func (r *Repository) GetPolicy(ctx context.Context, defaults *FaultPolicy) (*FaultPolicy, error) {
	var row FaultPolicy
	err := r.db.DB(ctx).Where("id = 1").First(&row).Error
	if err == nil {
		return &row, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	// Seed the single row with the defaults.
	seed := &FaultPolicy{ID: 1, DrainAlwaysConfirm: true}
	if defaults != nil {
		seed.AutoRestart = defaults.AutoRestart
		seed.AutoReschedule = defaults.AutoReschedule
		seed.DrainAlwaysConfirm = defaults.DrainAlwaysConfirm
	}
	if err := r.db.DB(ctx).Create(seed).Error; err != nil {
		return nil, err
	}
	return seed, nil
}

// UpdatePolicy merges the per-category toggles and stamps updated_at
// (AD15).
func (r *Repository) UpdatePolicy(ctx context.Context, autoRestart, autoReschedule map[string]bool, now time.Time) (*FaultPolicy, error) {
	current, err := r.GetPolicy(ctx, nil)
	if err != nil {
		return nil, err
	}
	restart := map[string]bool{}
	_ = json.Unmarshal(current.AutoRestart, &restart)
	for k, v := range autoRestart {
		restart[k] = v
	}
	reschedule := map[string]bool{}
	_ = json.Unmarshal(current.AutoReschedule, &reschedule)
	for k, v := range autoReschedule {
		reschedule[k] = v
	}
	restartJSON, _ := json.Marshal(restart)
	rescheduleJSON, _ := json.Marshal(reschedule)
	fields := map[string]any{
		"auto_restart":     string(restartJSON),
		"auto_reschedule":  string(rescheduleJSON),
		"updated_at":       now,
	}
	if err := r.db.DB(ctx).Model(&FaultPolicy{}).Where("id = 1").Updates(fields).Error; err != nil {
		return nil, err
	}
	return r.GetPolicy(ctx, nil)
}

// CleanupExpired deletes incidents older than the retention window
// (FR1.4).
func (r *Repository) CleanupExpired(ctx context.Context, before time.Time) (int64, error) {
	res := r.db.DB(ctx).Where("started_at < ?", before).Delete(&Incident{})
	return res.RowsAffected, res.Error
}

// FindActiveIncidentByTarget returns the active incident for a target,
// or nil when none exists (feature #53, AD10).
func (r *Repository) FindActiveIncidentByTarget(ctx context.Context, targetType, targetName string) (*Incident, error) {
	var row Incident
	err := r.db.DB(ctx).
		Where("target_type = ? AND target_name = ? AND status IN ?", targetType, targetName,
			[]string{StatusDetected, StatusDiagnosing, StatusRecovering}).
		Order("started_at DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// incidentNotFound maps a gorm.ErrRecordNotFound to CodeFaultNotFound.
func incidentNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apierrors.New(apierrors.CodeFaultNotFound)
	}
	return err
}