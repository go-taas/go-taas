package routing

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// DecisionFilter narrows a decision-log list query.
type DecisionFilter struct {
	GroupID       string
	Strategy      string
	MatchedRule   string // name substring search
	SelectedModel string // name substring search
	Outcome       string // served | failed
	Since         time.Time
	Offset        int
	Limit         int
}

// DecisionRepository persists the decision log and answers the
// observed-latency query against request_logs (read-only to routing).
type DecisionRepository struct {
	db *database.Manager
}

// NewDecisionRepository constructs a DecisionRepository bound to a GORM
// database.
func NewDecisionRepository(db *gorm.DB) *DecisionRepository {
	return &DecisionRepository{db: database.NewManager(db)}
}

// Insert writes one decision entry, generating the id when unset.
func (r *DecisionRepository) Insert(ctx context.Context, d *Decision) error {
	if d.ID == "" {
		d.ID = uuid.NewString()
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	return r.db.DB(ctx).Create(d).Error
}

// GetByID returns the decision with the given id (13101 on a miss).
func (r *DecisionRepository) GetByID(ctx context.Context, id string) (*Decision, error) {
	var row Decision
	err := r.db.DB(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeRoutingGroupNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// List returns one page of decisions, newest first, and the total count
// matching the (unpaginated) conditions.
func (r *DecisionRepository) List(ctx context.Context, f DecisionFilter) ([]*Decision, int64, error) {
	q := r.db.DB(ctx).Model(&Decision{})
	if s := strings.TrimSpace(f.GroupID); s != "" {
		q = q.Where("group_id = ?", s)
	}
	if s := strings.TrimSpace(f.Strategy); s != "" {
		q = q.Where("strategy = ?", s)
	}
	if s := strings.TrimSpace(f.MatchedRule); s != "" {
		q = q.Where("matched_rule_name LIKE ?", "%"+s+"%")
	}
	if s := strings.TrimSpace(f.SelectedModel); s != "" {
		q = q.Where("selected_model_name LIKE ?", "%"+s+"%")
	}
	switch strings.TrimSpace(f.Outcome) {
	case "served":
		q = q.Where("selected_model_id != ?", "")
	case "failed":
		q = q.Where("selected_model_id = ?", "")
	}
	if !f.Since.IsZero() {
		q = q.Where("created_at > ?", f.Since)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]*Decision, 0)
	err := q.Order("created_at DESC, id DESC").Offset(f.Offset).Limit(f.Limit).Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// CountSince returns the number of decisions of one group since the
// cutoff (the 24h request counter, AD15).
func (r *DecisionRepository) CountSince(ctx context.Context, groupID string, since time.Time) (int64, error) {
	var total int64
	err := r.db.DB(ctx).Model(&Decision{}).
		Where("group_id = ? AND created_at > ?", groupID, since).
		Count(&total).Error
	return total, err
}

// DeleteBefore removes decisions older than the cutoff, at most
// batchSize per pass, and returns the number deleted (the retention
// runner's loop signal).
func (r *DecisionRepository) DeleteBefore(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = 1
	}
	res := r.db.DB(ctx).
		Where("id IN (SELECT id FROM routing_decisions WHERE created_at < ? LIMIT ?)", cutoff, batchSize).
		Delete(&Decision{})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// requestLogRow is the minimal request_logs projection of the
// observed-latency query (the metering table is read-only to routing).
type requestLogRow struct {
	ModelID   string
	LatencyMs int64
}

// ObservedP50 returns the nearest-rank p50 latency in milliseconds of
// the successful request logs of each model since the cutoff (AD3). One
// query, grouped in Go by the metering nearest-rank convention. Models
// without samples are absent from the map.
func (r *DecisionRepository) ObservedP50(ctx context.Context, modelIDs []string, since time.Time) (map[string]int64, error) {
	result := make(map[string]int64, len(modelIDs))
	if len(modelIDs) == 0 {
		return result, nil
	}
	rows := make([]requestLogRow, 0)
	err := r.db.DB(ctx).
		Table("request_logs").
		Select("model_id", "latency_ms").
		Where("model_id IN ? AND status = ? AND created_at > ?", modelIDs, "success", since).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	byModel := make(map[string][]int64, len(modelIDs))
	for _, row := range rows {
		byModel[row.ModelID] = append(byModel[row.ModelID], row.LatencyMs)
	}
	for modelID, latencies := range byModel {
		result[modelID] = nearestRankP50(latencies)
	}
	return result, nil
}

// nearestRankP50 computes the nearest-rank median of the latencies: the
// value at rank ceil(0.5 * n) of the ascending order (the metering
// convention). An empty input returns 0.
func nearestRankP50(latencies []int64) int64 {
	if len(latencies) == 0 {
		return 0
	}
	sorted := make([]int64, len(latencies))
	copy(sorted, latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := (len(sorted) + 1) / 2
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}
