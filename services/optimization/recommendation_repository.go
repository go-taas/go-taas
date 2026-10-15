package optimization

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Filter scopes a recommendation list query (FR2.1). The zero value
// lists active rows of every rule, ranked.
type Filter struct {
	Rule      string
	Status    string // active (default) | dismissed | all
	OrgID     string
	ModelID   string
	MinSaving float64
	Offset    int
	Limit     int
}

// OverviewRow is the summary aggregate (FR2.2 / FR4.1).
type OverviewRow struct {
	TotalSaving    float64
	ActiveCount    int64
	CountByRule    map[string]int64
	DismissedCount int64
	LastRefreshAt  time.Time
	TopOrgID       string
	TopOrgName     string
	TopOrgSaving   float64
}

// Repository persists the recommendations, dismissals and runs. All
// reads and writes join an open transaction via the context.
type Repository struct {
	mgr *database.Manager
}

// NewRepository constructs a Repository bound to a GORM database.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{mgr: database.NewManager(db)}
}

// db resolves the transaction-scoped handle.
func (r *Repository) db(ctx context.Context) *gorm.DB { return r.mgr.DB(ctx) }

// GetByID loads one recommendation row by id; a miss is 13201.
func (r *Repository) GetByID(ctx context.Context, id string) (*Recommendation, error) {
	var row Recommendation
	if err := r.db(ctx).Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apierrors.New(apierrors.CodeRecommendationNotFound)
		}
		return nil, err
	}
	return &row, nil
}

// dismissalFor loads the surface's dismissal row for a fingerprint
// (nil when none — the derived status is active).
func (r *Repository) dismissalFor(ctx context.Context, surface, fingerprint string) (*Dismissal, error) {
	var row Dismissal
	err := r.db(ctx).
		Where("surface = ? AND fingerprint = ?", surface, fingerprint).
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// ListRow is one list row with its derived dismissal.
type ListRow struct {
	Recommendation Recommendation
	Dismissal      *Dismissal
}

// listJoinRow is the flat scan target of the LEFT-JOIN query (the
// routing-policy joinRow pattern): one column per projected field, no
// embedded structs, so GORM never tries to parse a relation.
type listJoinRow struct {
	ID                     string     `gorm:"column:id"`
	Fingerprint            string     `gorm:"column:fingerprint"`
	Rule                   string     `gorm:"column:rule"`
	TargetType             string     `gorm:"column:target_type"`
	TargetID               string     `gorm:"column:target_id"`
	TargetDisplay          string     `gorm:"column:target_display"`
	OrganizationID         string     `gorm:"column:organization_id"`
	ModelID                string     `gorm:"column:model_id"`
	APIKeyID               string     `gorm:"column:api_key_id"`
	ServiceID              string     `gorm:"column:service_id"`
	Suggestion             string     `gorm:"column:suggestion"`
	EstimatedMonthlySaving float64    `gorm:"column:estimated_monthly_saving"`
	Currency               string     `gorm:"column:currency"`
	Inputs                 []byte     `gorm:"column:inputs"`
	Arithmetic             string     `gorm:"column:arithmetic"`
	Alternatives           []byte     `gorm:"column:alternatives"`
	ActingPageAdmin        []byte     `gorm:"column:acting_page_admin"`
	ActingPageUser         []byte     `gorm:"column:acting_page_user"`
	FirstSeenAt            time.Time  `gorm:"column:first_seen_at"`
	LastComputedAt         time.Time  `gorm:"column:last_computed_at"`
	DismissalID            *string    `gorm:"column:dismissal_id"`
	DismissalReason        *string    `gorm:"column:dismissal_reason"`
	DismissalNote          *string    `gorm:"column:dismissal_note"`
	DismissalBy            *string    `gorm:"column:dismissal_by"`
	DismissalAt            *time.Time `gorm:"column:dismissal_at"`
}

// toListRow maps the flat join row to the ListRow projection,
// deriving the dismissal (nil when the LEFT JOIN found none — the
// status is active). surface is the query's surface (the dismissal
// row's own surface).
func (j *listJoinRow) toListRow(surface string) ListRow {
	row := ListRow{Recommendation: Recommendation{
		ID:                     j.ID,
		Fingerprint:            j.Fingerprint,
		Rule:                   j.Rule,
		TargetType:             j.TargetType,
		TargetID:               j.TargetID,
		TargetDisplay:          j.TargetDisplay,
		OrganizationID:         j.OrganizationID,
		ModelID:                j.ModelID,
		APIKeyID:               j.APIKeyID,
		ServiceID:              j.ServiceID,
		Suggestion:             j.Suggestion,
		EstimatedMonthlySaving: j.EstimatedMonthlySaving,
		Currency:               j.Currency,
		Inputs:                 j.Inputs,
		Arithmetic:             j.Arithmetic,
		Alternatives:           j.Alternatives,
		ActingPageAdmin:        j.ActingPageAdmin,
		ActingPageUser:         j.ActingPageUser,
		FirstSeenAt:            j.FirstSeenAt,
		LastComputedAt:         j.LastComputedAt,
	}}
	if j.DismissalID != nil {
		row.Dismissal = &Dismissal{
			ID:          *j.DismissalID,
			Surface:     surface,
			Fingerprint: j.Fingerprint,
			Reason:      derefString(j.DismissalReason),
			Note:        derefString(j.DismissalNote),
			DismissedBy: derefString(j.DismissalBy),
			DismissedAt: derefTime(j.DismissalAt),
		}
	}
	return row
}

// derefString dereferences a nullable string.
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// derefTime dereferences a nullable time.
func derefTime(p *time.Time) time.Time {
	if p == nil {
		return time.Time{}
	}
	return *p
}

// List returns the filtered, ranked page (FR2.1). The status is
// derived per surface via a LEFT JOIN on the dismissals table (AD11);
// org-scoped queries exclude R3 (AD8).
func (r *Repository) List(ctx context.Context, filter Filter, surface, orgScope string) ([]ListRow, int64, error) {
	query := r.db(ctx).
		Table("optimization_recommendations rec").
		Select("rec.*, d.reason AS dismissal_reason, d.note AS dismissal_note, d.dismissed_by AS dismissal_by, d.dismissed_at AS dismissal_at, d.id AS dismissal_id").
		Joins("LEFT JOIN optimization_dismissals d ON d.fingerprint = rec.fingerprint AND d.surface = ?", surface)

	if orgScope != "" {
		query = query.Where("rec.organization_id = ?", orgScope).
			Where("rec.rule != ?", RuleIdleService)
	}
	if filter.OrgID != "" {
		query = query.Where("rec.organization_id = ?", filter.OrgID)
	}
	if filter.ModelID != "" {
		query = query.Where("rec.model_id = ?", filter.ModelID)
	}
	if filter.Rule != "" {
		query = query.Where("rec.rule = ?", filter.Rule)
	}
	if filter.MinSaving > 0 {
		query = query.Where("rec.estimated_monthly_saving >= ?", filter.MinSaving)
	}
	switch filter.Status {
	case "", StatusActive:
		query = query.Where("d.id IS NULL")
	case StatusDismissed:
		query = query.Where("d.id IS NOT NULL")
	case StatusAll:
		// no status predicate
	}

	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	var joinRows []listJoinRow
	err := query.
		Order("rec.estimated_monthly_saving DESC, rec.rule ASC, rec.target_display ASC").
		Offset(filter.Offset).
		Limit(limit).
		Scan(&joinRows).Error
	if err != nil {
		return nil, 0, err
	}
	rows := make([]ListRow, 0, len(joinRows))
	for i := range joinRows {
		rows = append(rows, joinRows[i].toListRow(surface))
	}
	return rows, total, nil
}

// Overview computes the summary aggregates for one surface (FR2.2).
// orgScope limits the aggregate to one organization (the user
// surface); empty means the whole fleet.
func (r *Repository) Overview(ctx context.Context, surface, orgScope string) (*OverviewRow, error) {
	base := r.db(ctx).
		Table("optimization_recommendations rec").
		Joins("LEFT JOIN optimization_dismissals d ON d.fingerprint = rec.fingerprint AND d.surface = ?", surface)
	if orgScope != "" {
		base = base.Where("rec.organization_id = ? AND rec.rule != ?", orgScope, RuleIdleService)
	}

	// Active aggregates: SUM(saving), COUNT, per-rule counts.
	var active struct {
		Total float64
		Count int64
	}
	if err := base.Session(&gorm.Session{}).
		Where("d.id IS NULL").
		Select("COALESCE(SUM(rec.estimated_monthly_saving), 0) AS total, COUNT(*) AS count").
		Scan(&active).Error; err != nil {
		return nil, err
	}

	type ruleCount struct {
		Rule  string
		Count int64
	}
	var byRule []ruleCount
	if err := base.Session(&gorm.Session{}).
		Where("d.id IS NULL").
		Select("rec.rule AS rule, COUNT(*) AS count").
		Group("rec.rule").
		Scan(&byRule).Error; err != nil {
		return nil, err
	}
	countByRule := make(map[string]int64, len(byRule))
	for _, rc := range byRule {
		countByRule[rc.Rule] = rc.Count
	}

	var dismissed int64
	if err := base.Session(&gorm.Session{}).
		Where("d.id IS NOT NULL").
		Count(&dismissed).Error; err != nil {
		return nil, err
	}

	// Last successful refresh.
	var lastRefresh time.Time
	var lastRun struct {
		FinishedAt time.Time
	}
	err := r.db(ctx).
		Table("optimization_runs").
		Where("success = ?", true).
		Order("finished_at DESC").
		Limit(1).
		Scan(&lastRun).Error
	if err == nil && !lastRun.FinishedAt.IsZero() {
		lastRefresh = lastRun.FinishedAt
	}

	out := &OverviewRow{
		TotalSaving:    active.Total,
		ActiveCount:    active.Count,
		CountByRule:    countByRule,
		DismissedCount: dismissed,
		LastRefreshAt:  lastRefresh,
	}

	// Top org by summed active saving (admin overview only).
	if orgScope == "" {
		var top struct {
			OrganizationID string
			Total          float64
		}
		if err := base.Session(&gorm.Session{}).
			Where("d.id IS NULL").
			Select("rec.organization_id AS organization_id, SUM(rec.estimated_monthly_saving) AS total").
			Group("rec.organization_id").
			Order("total DESC").
			Limit(1).
			Scan(&top).Error; err != nil {
			return nil, err
		}
		if top.OrganizationID != "" {
			out.TopOrgID = top.OrganizationID
			out.TopOrgSaving = top.Total
			var name struct {
				DisplayName string
			}
			if err := r.db(ctx).
				Table("organizations").
				Select("display_name").
				Where("id = ?", top.OrganizationID).
				Scan(&name).Error; err == nil {
				out.TopOrgName = name.DisplayName
			}
		}
	}
	return out, nil
}

// InsertDismissal records a surface's dismissal; an existing row is
// 13202 (FR3.1).
func (r *Repository) InsertDismissal(ctx context.Context, surface, fingerprint, reason, note, actor string) error {
	row := Dismissal{
		ID:          uuid.NewString(),
		Surface:     surface,
		Fingerprint: fingerprint,
		Reason:      reason,
		Note:        note,
		DismissedBy: actor,
		DismissedAt: time.Now().UTC(),
	}
	if err := r.db(ctx).Create(&row).Error; err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeRecommendationDismissed)
		}
		return err
	}
	return nil
}

// DeleteDismissal removes a surface's dismissal; no row is 13204
// (FR3.2).
func (r *Repository) DeleteDismissal(ctx context.Context, surface, fingerprint string) error {
	res := r.db(ctx).
		Where("surface = ? AND fingerprint = ?", surface, fingerprint).
		Delete(&Dismissal{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apierrors.New(apierrors.CodeRecommendationRestoreInvalid)
	}
	return nil
}

// InsertRun records an engine run row.
func (r *Repository) InsertRun(ctx context.Context, run *Run) error {
	return r.db(ctx).Create(run).Error
}

// UpsertSnapshot performs the engine's atomic swap (AD6): one
// transaction — upsert the computed entries by fingerprint (preserving
// id and first_seen_at, refreshing the computed fields), delete the
// vanished fingerprints and record the run row. A failure leaves the
// previous snapshot intact.
func (r *Repository) UpsertSnapshot(ctx context.Context, entries []ComputedRecommendation, currency string, started, finished time.Time, trigger string) error {
	return r.db(ctx).Transaction(func(tx *gorm.DB) error {
		keep := make([]string, 0, len(entries))
		kept := make(map[string]bool, len(entries))
		for i := range entries {
			entry := entries[i]
			fp := Fingerprint(entry.Rule, entry.TargetType, entry.TargetID, entry.Suggestion)
			if !kept[fp] {
				kept[fp] = true
				keep = append(keep, fp)
			}
			inputsJSON, err := json.Marshal(entry.Inputs)
			if err != nil {
				return err
			}
			alternatives := "[]"
			if len(entry.Alternatives) > 0 {
				b, err := json.Marshal(entry.Alternatives)
				if err != nil {
					return err
				}
				alternatives = string(b)
			}
			adminLink, err := json.Marshal(entry.ActingPageAdmin)
			if err != nil {
				return err
			}
			userLink, err := json.Marshal(entry.ActingPageUser)
			if err != nil {
				return err
			}
			// Upsert by fingerprint: preserve id and first_seen_at,
			// refresh the computed fields (FR1.3).
			var existing Recommendation
			err = tx.Where("fingerprint = ?", fp).Take(&existing).Error
			if err == nil {
				updates := map[string]any{
					"rule":                     entry.Rule,
					"target_type":              entry.TargetType,
					"target_id":                entry.TargetID,
					"target_display":           entry.TargetDisplay,
					"organization_id":          entry.OrganizationID,
					"model_id":                 entry.ModelID,
					"api_key_id":               entry.APIKeyID,
					"service_id":               entry.ServiceID,
					"suggestion":               entry.Suggestion,
					"estimated_monthly_saving": entry.EstimatedMonthlySaving,
					"currency":                 currency,
					"inputs":                   datatypes.JSON(inputsJSON),
					"arithmetic":               entry.Arithmetic,
					"alternatives":             datatypes.JSON(alternatives),
					"acting_page_admin":        datatypes.JSON(adminLink),
					"acting_page_user":         datatypes.JSON(userLink),
					"last_computed_at":         started,
					"updated_at":               started,
				}
				if err := tx.Model(&Recommendation{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
					return err
				}
			} else {
				row := Recommendation{
					ID:                     uuid.NewString(),
					Fingerprint:            fp,
					Rule:                   entry.Rule,
					TargetType:             entry.TargetType,
					TargetID:               entry.TargetID,
					TargetDisplay:          entry.TargetDisplay,
					OrganizationID:         entry.OrganizationID,
					ModelID:                entry.ModelID,
					APIKeyID:               entry.APIKeyID,
					ServiceID:              entry.ServiceID,
					Suggestion:             entry.Suggestion,
					EstimatedMonthlySaving: entry.EstimatedMonthlySaving,
					Currency:               currency,
					Inputs:                 datatypes.JSON(inputsJSON),
					Arithmetic:             entry.Arithmetic,
					Alternatives:           datatypes.JSON(alternatives),
					ActingPageAdmin:        datatypes.JSON(adminLink),
					ActingPageUser:         datatypes.JSON(userLink),
					FirstSeenAt:            started,
					LastComputedAt:         started,
				}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
			}
		}
		// Delete the vanished fingerprints (the swap's other half).
		if err := tx.Where("fingerprint NOT IN ?", keep).Delete(&Recommendation{}).Error; err != nil {
			return err
		}
		// The run row (the refresh log).
		run := Run{
			ID:                     uuid.NewString(),
			StartedAt:              started,
			FinishedAt:             finished,
			DurationMs:             finished.Sub(started).Milliseconds(),
			RulesEvaluated:         len(Rules),
			RecommendationsEmitted: len(entries),
			Trigger:                trigger,
			Success:                true,
		}
		return tx.Create(&run).Error
	})
}

// isUniqueViolation reports a PostgreSQL 23505 / SQLite unique
// constraint failure.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") || strings.Contains(msg, "UNIQUE constraint failed")
}
