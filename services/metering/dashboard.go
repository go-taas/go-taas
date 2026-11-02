package metering

import (
	"context"
	"time"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	meteringv1 "github.com/go-taas/go-taas/proto/taas/metering/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// dashboardMaxRangeSeconds caps the dashboard range at 92 days (AD7).
const dashboardMaxRangeSeconds = 92 * 24 * 3600

// GetUsageDashboard returns summary cards and daily time buckets grouped
// by a dimension, joining metering usage with billing charge records for
// a unified usage x cost view (feature #9, AC1-AC8).
func (s *Service) GetUsageDashboard(ctx context.Context, req *meteringv1.GetUsageDashboardRequest) (*meteringv1.GetUsageDashboardResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	since, until, err := validateDashboardRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	groupBy := dashboardGroupBy(req.GetGroupBy())
	if groupBy == "" {
		groupBy = GroupByAPIKey
	}
	repo, err := s.dashboardRepo()
	if err != nil {
		return nil, err
	}
	rows, err := repo.DashboardAggregate(ctx, orgID, since, until, groupBy)
	if err != nil {
		return nil, err
	}
	watermark, err := repo.ChargeWatermark(ctx, orgID)
	if err != nil {
		return nil, err
	}

	// Build the daily buckets, one per UTC day in the range (AD7).
	buckets := make(map[int64][]*meteringv1.DashboardGroup)
	for _, row := range rows {
		day := dayStart(row.Day)
		buckets[day] = append(buckets[day], &meteringv1.DashboardGroup{
			GroupKey:         row.GroupKey,
			CostCents:        row.CostCents,
			CostMicros:       row.CostMicros,
			PromptTokens:     row.PromptTokens,
			CompletionTokens: row.CompletionTokens,
			CachedTokens:     row.CachedTokens,
			ReasoningTokens:  row.ReasoningTokens,
			RequestCount:     row.RequestCount,
			Priced:           row.Priced,
		})
	}
	daily := make([]*meteringv1.DailyBucket, 0)
	for day := dayStart(since); day < until; day += 24 * 3600 {
		daily = append(daily, &meteringv1.DailyBucket{
			Date:   day,
			Groups: buckets[day],
		})
	}

	// Summary cards derived from the same rows. The currency comes from the
	// charges themselves: a price can be entered in any supported currency,
	// and reporting the platform default would label CNY amounts as USD.
	cards := &meteringv1.DashboardCard{
		Currency:    dashboardCurrency(rows),
		DataThrough: watermark,
	}
	for _, row := range rows {
		cards.TotalCostCents += row.CostCents
		cards.TotalCostMicros += row.CostMicros
		cards.PromptTokens += row.PromptTokens
		cards.CompletionTokens += row.CompletionTokens
		cards.CachedTokens += row.CachedTokens
		cards.ReasoningTokens += row.ReasoningTokens
		cards.RequestCount += row.RequestCount
		if !row.Priced {
			cards.UnpricedRequestCount += row.RequestCount
		}
	}

	return &meteringv1.GetUsageDashboardResponse{
		Response:     okResponse(),
		Cards:        cards,
		DailyBuckets: daily,
	}, nil
}

// ListRequestLogs returns per-request metadata logs, filterable by api
// key, model, status and time range (feature #12, AC-A4).
func (s *Service) ListRequestLogs(ctx context.Context, req *meteringv1.ListRequestLogsRequest) (*meteringv1.ListRequestLogsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	since, until, err := validateRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListRequestLogs(ctx, RequestLogFilter{
		OrganizationID: orgID,
		APIKeyID:       req.GetApiKeyId(),
		ModelID:        req.GetModelId(),
		Status:         requestLogStatusString(req.GetStatus()),
		Since:          since,
		Until:          until,
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}
	logs := make([]*meteringv1.RequestLog, 0, len(rows))
	for _, row := range rows {
		logs = append(logs, summarizeRequestLog(row))
	}
	return &meteringv1.ListRequestLogsResponse{
		Response:    okResponse(),
		RequestLogs: logs,
		PageMeta:    &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// GetRequestLog returns one request log for drill-down (feature #12,
// AC-A5); unknown ids return 10405.
func (s *Service) GetRequestLog(ctx context.Context, req *meteringv1.GetRequestLogRequest) (*meteringv1.GetRequestLogResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.FindRequestLogByID(ctx, req.GetRequestLogId())
	if err != nil {
		return nil, err
	}
	return &meteringv1.GetRequestLogResponse{
		Response:   okResponse(),
		RequestLog: summarizeRequestLog(row),
	}, nil
}

// summarizeRequestLog maps a request-log row to the proto message.
func summarizeRequestLog(row *RequestLog) *meteringv1.RequestLog {
	serviceID := ""
	if row.ServiceID != nil {
		serviceID = *row.ServiceID
	}
	return &meteringv1.RequestLog{
		RequestLogId:     row.ID,
		RequestId:        row.RequestID,
		OrganizationId:   row.OrganizationID,
		ApiKeyId:         row.APIKeyID,
		ModelId:          row.ModelID,
		ServiceId:        serviceID,
		PromptTokens:     row.PromptTokens,
		CompletionTokens: row.CompletionTokens,
		CachedTokens:     row.CachedTokens,
		ReasoningTokens:  row.ReasoningTokens,
		LatencyMs:        row.LatencyMs,
		Status:           requestLogStatusEnum(row.Status),
		Error:            row.Error,
		CreatedAt:        row.CreatedAt.Unix(),
	}
}

// validateDashboardRange checks and defaults the since/until pair for
// the dashboard: until defaults to now, since to until-24h; since >
// until or a range > 92 days returns 10404 (AD7, AC7).
func validateDashboardRange(since, until int64) (int64, int64, error) {
	if until <= 0 {
		until = time.Now().Unix()
	}
	if since <= 0 {
		since = until - defaultRangeHours*3600
	}
	if since > until {
		return 0, 0, apierrors.New(apierrors.CodeMeteringRangeInvalid)
	}
	if until-since > dashboardMaxRangeSeconds {
		return 0, 0, apierrors.New(apierrors.CodeMeteringRangeInvalid)
	}
	return since, until, nil
}

// dashboardGroupBy maps the request string to the repository dimension.
func dashboardGroupBy(g string) DashboardGroupBy {
	switch g {
	case "model":
		return GroupByModel
	case "accelerator_type":
		return GroupByAcceleratorType
	default:
		return GroupByAPIKey
	}
}

// dashboardRepo lazily wires and returns the dashboard repository.
func (s *Service) dashboardRepo() (*DashboardRepository, error) {
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	return NewDashboardRepository(db), nil
}

// dashboardCurrency reports the currency the aggregated charges were priced
// in, falling back to the platform's configured billing currency and then to
// USD for an installation that has neither.
func dashboardCurrency(rows []DashboardAggregateRow) string {
	for _, row := range rows {
		if row.Currency != "" && row.CostMicros != 0 {
			return row.Currency
		}
	}
	if cfg := config.GetConfig(); cfg != nil && cfg.Billing.Currency != "" {
		return cfg.Billing.Currency
	}
	return "USD"
}
