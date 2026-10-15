package optimization

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// ChargeGroup is one (org, api key, model, card) aggregate over the
// window from billing's settled charge records — the R1/R2/R4 usage
// and cost input (AD10). Priced rows only.
type ChargeGroup struct {
	OrganizationID   string
	APIKeyID         string
	ModelID          string
	Card             string
	Requests         int64
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	Spend            float64
}

// PriceRates is one applicable price cell's rates per million tokens.
type PriceRates struct {
	Input  float64
	Output float64
	Cached float64
}

// KeyModelLog is one request_logs row's offline-shape fields — the R2
// input (AD13).
type KeyModelLog struct {
	OrganizationID string
	APIKeyID       string
	ModelID        string
	PromptTokens   int64
	Status         string
	CreatedAt      time.Time
}

// ServiceRow is one inference service's deployment fields — the R3
// input (AD10).
type ServiceRow struct {
	ID              string
	OrganizationID  string
	Name            string
	ModelID         string
	ImageID         string
	Replicas        int
	AcceleratorType string
	State           string
	AutoscalingJSON string
}

// PolicyRow is the global autoscaling policy singleton (R3's inherit
// default).
type PolicyRow struct {
	Enabled     bool
	ScaleToZero bool
}

// ModelRow is one catalog model row (R1's candidate axis).
type ModelRow struct {
	ID   string
	Name string
}

// CardTypePair is one (vendor, card_type) fleet pair (AD14).
type CardTypePair struct {
	Vendor   string
	CardType string
}

// chargeReader reads billing's settled aggregates (read-only).
type chargeReader interface {
	// ChargeGroups aggregates charge_records over the window, priced
	// rows only, grouped by (org, key, model, card).
	ChargeGroups(ctx context.Context, since time.Time) ([]ChargeGroup, error)
	// ApplicablePrice returns the effective price rates for (model,
	// card) at the given unix time; nil when unpriced.
	ApplicablePrice(ctx context.Context, modelID, cardType string, at int64) (*PriceRates, error)
}

// requestLogReader reads metering's per-request logs (read-only).
type requestLogReader interface {
	// KeyModelLogs returns the (org, key, model) request-log rows over
	// the window — the R2 offline-shape input.
	KeyModelLogs(ctx context.Context, since time.Time) ([]KeyModelLog, error)
	// CountByServiceSince counts request_logs per service id over the
	// window — the R3 idle input.
	CountByServiceSince(ctx context.Context, since time.Time) (map[string]int64, error)
}

// serviceReader reads infer's deployment state (read-only).
type serviceReader interface {
	// ActiveServices returns every non-terminated inference service.
	ActiveServices(ctx context.Context) ([]ServiceRow, error)
	// GlobalAutoscalingPolicy returns the singleton policy row.
	GlobalAutoscalingPolicy(ctx context.Context) (*PolicyRow, error)
}

// catalogReader reads the model catalog and grants (read-only).
type catalogReader interface {
	// ListModels returns the catalog models (id, name).
	ListModels(ctx context.Context) ([]ModelRow, error)
	// GrantCounts returns model_id -> grant row count (zero rows =
	// public, default-allow).
	GrantCounts(ctx context.Context) (map[string]int64, error)
	// OrgGrants returns model_id -> org_id -> granted.
	OrgGrants(ctx context.Context) (map[string]map[string]bool, error)
	// ImageEngines returns image_id -> engine.
	ImageEngines(ctx context.Context) (map[string]string, error)
	// OrgNames returns org_id -> display name.
	OrgNames(ctx context.Context) (map[string]string, error)
}

// CardTypeLister is implemented by the accelerator service (its
// card-type set lives in the in-memory projection, not a table).
type CardTypeLister interface {
	// ListCardTypes returns the (vendor, card_type) pairs present in
	// the fleet.
	ListCardTypes(ctx context.Context) ([]CardTypePair, error)
}

// gormChargeReader is the chargeReader over the shared GORM handle.
type gormChargeReader struct{ db *gorm.DB }

// ChargeGroups implements chargeReader: the priced-only aggregate over
// charge_records grouped by (org, key, model, card).
func (r *gormChargeReader) ChargeGroups(ctx context.Context, since time.Time) ([]ChargeGroup, error) {
	var rows []ChargeGroup
	err := r.db.WithContext(ctx).
		Table("charge_records").
		Select("organization_id, api_key_id, model_id, accelerator_type AS card, "+
			"SUM(request_count) AS requests, "+
			"SUM(prompt_tokens) AS prompt_tokens, "+
			"SUM(completion_tokens) AS completion_tokens, "+
			"SUM(cached_tokens) AS cached_tokens, "+
			"SUM(amount) AS spend").
		Where("priced = ? AND period_start >= ?", true, since.Unix()).
		Group("organization_id, api_key_id, model_id, accelerator_type").
		Scan(&rows).Error
	return rows, err
}

// ApplicablePrice implements chargeReader: the effective-dated price
// lookup (the billing repository's exact semantics, D1/D6).
func (r *gormChargeReader) ApplicablePrice(ctx context.Context, modelID, cardType string, at int64) (*PriceRates, error) {
	var row PriceRates
	err := r.db.WithContext(ctx).
		Table("price_entries").
		Select("input_price_per_million AS input, output_price_per_million AS output, cached_price_per_million AS cached").
		Where("model_id = ? AND accelerator_type = ? AND effective_from <= ?", modelID, cardType, at).
		Order("effective_from DESC").
		Limit(1).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.Input == 0 && row.Output == 0 && row.Cached == 0 {
		return nil, nil
	}
	return &row, nil
}

// gormRequestLogReader is the requestLogReader over the shared handle.
type gormRequestLogReader struct{ db *gorm.DB }

// KeyModelLogs implements requestLogReader.
func (r *gormRequestLogReader) KeyModelLogs(ctx context.Context, since time.Time) ([]KeyModelLog, error) {
	var rows []KeyModelLog
	err := r.db.WithContext(ctx).
		Table("request_logs").
		Select("organization_id, api_key_id, model_id, prompt_tokens, status, created_at").
		Where("created_at >= ?", since).
		Find(&rows).Error
	return rows, err
}

// CountByServiceSince implements requestLogReader.
func (r *gormRequestLogReader) CountByServiceSince(ctx context.Context, since time.Time) (map[string]int64, error) {
	var rows []struct {
		ServiceID string
		Count     int64
	}
	err := r.db.WithContext(ctx).
		Table("request_logs").
		Select("service_id, COUNT(*) AS count").
		Where("created_at >= ? AND service_id IS NOT NULL AND service_id != ''", since).
		Group("service_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.ServiceID] = row.Count
	}
	return out, nil
}

// gormServiceReader is the serviceReader over the shared handle.
type gormServiceReader struct{ db *gorm.DB }

// ActiveServices implements serviceReader.
func (r *gormServiceReader) ActiveServices(ctx context.Context) ([]ServiceRow, error) {
	var rows []ServiceRow
	err := r.db.WithContext(ctx).
		Table("inference_services").
		Select("id, organization_id, name, model_id, image_id, replicas, accelerator_type, state, autoscaling AS autoscaling_json").
		Where("state != ?", "terminated").
		Find(&rows).Error
	return rows, err
}

// GlobalAutoscalingPolicy implements serviceReader.
func (r *gormServiceReader) GlobalAutoscalingPolicy(ctx context.Context) (*PolicyRow, error) {
	var row PolicyRow
	err := r.db.WithContext(ctx).
		Table("autoscaling_policy").
		Select("enabled, scale_to_zero").
		Where("id = ?", 1).
		Take(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// gormCatalogReader is the catalogReader over the shared handle.
type gormCatalogReader struct{ db *gorm.DB }

// ListModels implements catalogReader.
func (r *gormCatalogReader) ListModels(ctx context.Context) ([]ModelRow, error) {
	var rows []ModelRow
	err := r.db.WithContext(ctx).
		Table("models").
		Select("id, name").
		Find(&rows).Error
	return rows, err
}

// GrantCounts implements catalogReader.
func (r *gormCatalogReader) GrantCounts(ctx context.Context) (map[string]int64, error) {
	var rows []struct {
		ModelID string
		Count   int64
	}
	err := r.db.WithContext(ctx).
		Table("model_authorizations").
		Select("model_id, COUNT(*) AS count").
		Group("model_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.ModelID] = row.Count
	}
	return out, nil
}

// OrgGrants implements catalogReader.
func (r *gormCatalogReader) OrgGrants(ctx context.Context) (map[string]map[string]bool, error) {
	var rows []struct {
		ModelID string
		OrgID   string
	}
	err := r.db.WithContext(ctx).
		Table("model_authorizations").
		Select("model_id, organization_id AS org_id").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]bool, len(rows))
	for _, row := range rows {
		if out[row.ModelID] == nil {
			out[row.ModelID] = map[string]bool{}
		}
		out[row.ModelID][row.OrgID] = true
	}
	return out, nil
}

// ImageEngines implements catalogReader.
func (r *gormCatalogReader) ImageEngines(ctx context.Context) (map[string]string, error) {
	var rows []struct {
		ID      string
		ImageID string
		Engine  string
	}
	err := r.db.WithContext(ctx).
		Table("images").
		Select("id, engine").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.ID] = row.Engine
	}
	return out, nil
}

// OrgNames implements catalogReader.
func (r *gormCatalogReader) OrgNames(ctx context.Context) (map[string]string, error) {
	var rows []struct {
		ID          string
		DisplayName string
	}
	err := r.db.WithContext(ctx).
		Table("organizations").
		Select("id, display_name").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.ID] = row.DisplayName
	}
	return out, nil
}

// newGormReaders builds the five read repositories over the shared
// GORM handle (AD10).
func newGormReaders(db *gorm.DB) (chargeReader, requestLogReader, serviceReader, catalogReader) {
	return &gormChargeReader{db: db},
		&gormRequestLogReader{db: db},
		&gormServiceReader{db: db},
		&gormCatalogReader{db: db}
}
