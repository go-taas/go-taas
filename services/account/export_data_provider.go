package account

import (
	"context"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
)

// DBExportDataProvider aggregates the caller's data for an export from
// the shared database (AD4). It reads the existing usage_records, bills,
// invoices, and request_logs tables read-only.
type DBExportDataProvider struct {
	db *database.Manager
}

// NewDBExportDataProvider constructs a DBExportDataProvider bound to a
// GORM database.
func NewDBExportDataProvider(db *gorm.DB) *DBExportDataProvider {
	return &DBExportDataProvider{db: database.NewManager(db)}
}

// ExportRows returns the data rows for an export type and range.
func (p *DBExportDataProvider) ExportRows(ctx context.Context, orgID, exportType string, since, until int64) ([]map[string]any, error) {
	switch exportType {
	case ExportTypeUsage:
		return p.usageRows(ctx, orgID, since, until)
	case ExportTypeBilling:
		return p.billingRows(ctx, orgID, since, until)
	case ExportTypeRequestLogs:
		return p.requestLogRows(ctx, orgID, since, until)
	case ExportTypeAccount:
		return p.accountRows(ctx, orgID)
	default:
		return []map[string]any{}, nil
	}
}

func (p *DBExportDataProvider) usageRows(ctx context.Context, orgID string, since, until int64) ([]map[string]any, error) {
	var rows []map[string]any
	err := p.db.DB(ctx).Table("usage_records").
		Select("id, model_id, prompt_tokens, completion_tokens, created_at").
		Where("organization_id = ? AND created_at >= ? AND created_at < ?", orgID, unixTime(since), unixTime(until)).
		Scan(&rows).Error
	return rows, err
}

func (p *DBExportDataProvider) billingRows(ctx context.Context, orgID string, since, until int64) ([]map[string]any, error) {
	var rows []map[string]any
	err := p.db.DB(ctx).Table("bills").
		Select("id, amount, currency, status, created_at").
		Where("organization_id = ? AND created_at >= ? AND created_at < ?", orgID, unixTime(since), unixTime(until)).
		Scan(&rows).Error
	return rows, err
}

func (p *DBExportDataProvider) requestLogRows(ctx context.Context, orgID string, since, until int64) ([]map[string]any, error) {
	var rows []map[string]any
	err := p.db.DB(ctx).Table("request_logs").
		Select("id, model_id, prompt_tokens, completion_tokens, latency_ms, status, created_at").
		Where("organization_id = ? AND created_at >= ? AND created_at < ?", orgID, unixTime(since), unixTime(until)).
		Scan(&rows).Error
	return rows, err
}

func (p *DBExportDataProvider) accountRows(ctx context.Context, orgID string) ([]map[string]any, error) {
	var rows []map[string]any
	err := p.db.DB(ctx).Table("organizations").
		Select("id, display_name, state, created_at").
		Where("id = ?", orgID).
		Scan(&rows).Error
	return rows, err
}