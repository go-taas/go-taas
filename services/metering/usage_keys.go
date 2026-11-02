package metering

import (
	"context"
	"strings"

	meteringv1 "github.com/go-taas/go-taas/proto/taas/metering/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// usageKeysMaxRangeSeconds caps the usage-keys range at 92 days (AD5).
const usageKeysMaxRangeSeconds = 92 * 24 * 3600

var _ = usageKeysMaxRangeSeconds

// GetUsageKeysOverview returns per-key analytics: summary cards, a
// top-keys ranking, a per-key table, and a per-key trend (feature #28,
// AC1-AC2). The admin binding is the fleet-wide view (cross-org by
// default, optional org filter, gated by the caller's role, AD9); the
// user binding is hard-scoped to the caller's org (AD8).
func (s *Service) GetUsageKeysOverview(ctx context.Context, req *meteringv1.GetUsageKeysOverviewRequest) (*meteringv1.GetUsageKeysOverviewResponse, error) {
	surface := surfaceFromContext(ctx)

	var orgFilter string
	if surface == SurfaceAdmin {
		// The admin fleet view is cross-org by default (AD9). The org
		// filter is optional: read from the session active org or the
		// transitional header when the caller wants to scope to their
		// own org.
		orgFilter = strings.TrimSpace(req.GetOrganizationId())
		if orgFilter == "" {
			orgFilter, _ = s.resolveOrg(ctx)
		}
		if err := s.requireAdminRole(ctx, orgFilter); err != nil {
			return nil, err
		}
	} else {
		// User binding: hard-scoped to the caller's org (AD8).
		org, err := s.resolveOrg(ctx)
		if err != nil {
			return nil, err
		}
		orgFilter = org
	}
	since, until, err := s.validateUsageKeysRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	modelFilter := strings.TrimSpace(req.GetModelId())
	repo, err := s.usageKeysRepo()
	if err != nil {
		return nil, err
	}
	bucketSize := bucketSizeForRange(since, until)
	filter := UsageKeysFilter{
		OrganizationID: orgFilter,
		ModelID:        modelFilter,
		OwnerUserID:    strings.TrimSpace(req.GetOwnerUserId()),
		ProjectID:      strings.TrimSpace(req.GetProjectId()),
	}
	buckets, keys, err := repo.AggregateUsageKeys(ctx, filter, since, until, bucketSize)
	if err != nil {
		return nil, err
	}
	watermark, err := repo.DataThrough(ctx, filter, since, until, bucketSize)
	if err != nil {
		return nil, err
	}
	costs, err := repo.ChargeCostByKey(ctx, orgFilter, since, until)
	if err != nil {
		return nil, err
	}

	cards := buildUsageKeysCards(buckets, watermark)
	keyRows := make([]*meteringv1.UsageKeyRow, 0, len(keys))
	for _, k := range keys {
		name, _ := repo.APIKeyName(ctx, k.APIKeyID)
		keyRows = append(keyRows, &meteringv1.UsageKeyRow{
			ApiKeyId:           k.APIKeyID,
			ApiKeyName:         name,
			OrganizationId:     k.OrganizationID,
			OwnerUserId:        k.OwnerUserID,
			OwnerDisplayName:   s.userLabel(ctx, k.OwnerUserID),
			ProjectId:          k.ProjectID,
			ProjectDisplayName: s.projectLabel(ctx, k.ProjectID),
			RequestCount:   k.RequestCount,
			ErrorCount:     k.ErrorCount,
			TotalTokens:    k.TotalTokens,
			TotalCostCents: costs[k.APIKeyID],
			AvgLatencyMs:   k.AvgLatencyMs,
			P95LatencyMs:   k.P95LatencyMs,
			DataThrough:    k.DataThrough,
		})
	}
	topKeys := buildTopKeys(keyRows, costs)
	series := buildUsageKeysSeries(buckets, costs)

	return &meteringv1.GetUsageKeysOverviewResponse{
		Response: okResponse(),
		Cards:    cards,
		Keys:     keyRows,
		TopKeys:  topKeys,
		Series:   series,
	}, nil
}

// GetUsageKeys returns single-key analytics: summary cards and a per-key
// trend (feature #28, AC3-AC4). The admin binding covers any key (AD9);
// the user binding is hard-scoped to the caller's org (AD8).
func (s *Service) GetUsageKeys(ctx context.Context, req *meteringv1.GetUsageKeysRequest) (*meteringv1.GetUsageKeysResponse, error) {
	apiKeyID := strings.TrimSpace(req.GetApiKeyId())
	if apiKeyID == "" {
		return nil, apierrors.New(apierrors.CodeUsageKeyNotFound)
	}
	surface := surfaceFromContext(ctx)

	var orgFilter string
	if surface == SurfaceAdmin {
		// Admin binding: cross-org by default, optional org filter (AD9).
		orgFilter = strings.TrimSpace(req.GetOrganizationId())
		if orgFilter == "" {
			orgFilter, _ = s.resolveOrg(ctx)
		}
		if err := s.requireAdminRole(ctx, orgFilter); err != nil {
			return nil, err
		}
	} else {
		// User binding: hard-scoped to the caller's org (AD8).
		org, err := s.resolveOrg(ctx)
		if err != nil {
			return nil, err
		}
		orgFilter = org
	}

	since, until, err := s.validateUsageKeysRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	repo, err := s.usageKeysRepo()
	if err != nil {
		return nil, err
	}
	bucketSize := bucketSizeForRange(since, until)
	buckets, err := repo.AggregateUsageKey(ctx, orgFilter, apiKeyID, since, until, bucketSize)
	if err != nil {
		return nil, err
	}
	watermark, err := repo.DataThrough(ctx, UsageKeysFilter{OrganizationID: orgFilter}, since, until, bucketSize)
	if err != nil {
		return nil, err
	}
	costs, err := repo.ChargeCostByKey(ctx, orgFilter, since, until)
	if err != nil {
		return nil, err
	}
	// An unknown key returns 11201 (AD2).
	if len(buckets) == 0 && costs[apiKeyID] == 0 {
		return nil, apierrors.New(apierrors.CodeUsageKeyNotFound)
	}

	cards := buildUsageKeysCards(buckets, watermark)
	cards.TotalCostCents = costs[apiKeyID]
	series := buildUsageKeysSeries(buckets, costs)

	return &meteringv1.GetUsageKeysResponse{
		Response: okResponse(),
		Cards:    cards,
		Series:   series,
	}, nil
}

// validateUsageKeysRange checks and defaults the since/until pair: until
// defaults to now, since to until-24h; since > until or a range > 92
// days returns 10404 (AD5).
func (s *Service) validateUsageKeysRange(since, until int64) (int64, int64, error) {
	return validateRange(since, until)
}

// usageKeysRepo lazily wires and returns the usage-keys repository.
func (s *Service) usageKeysRepo() (*UsageKeysRepository, error) {
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	return NewUsageKeysRepository(db), nil
}

// buildUsageKeysCards derives the headline summary cards from the bucket
// rows. error_rate and share_pct are derived client-side; the wire
// carries integer counts, integer milliseconds, and integer cents only
// (AD3).
func buildUsageKeysCards(buckets []UsageKeysBucketRow, watermark int64) *meteringv1.UsageKeysCard {
	card := &meteringv1.UsageKeysCard{DataThrough: watermark}
	var latencySum, latencyCount int64
	var latencies []int64
	for _, b := range buckets {
		card.RequestCount += b.RequestCount
		card.ErrorCount += b.ErrorCount
		card.TotalTokens += b.TotalTokens
		card.TotalCostCents += b.TotalCostCents
		latencySum += b.AvgLatencyMs * b.RequestCount
		latencyCount += b.RequestCount
		for i := int64(0); i < b.RequestCount; i++ {
			latencies = append(latencies, b.AvgLatencyMs)
		}
	}
	card.AvgLatencyMs = avg(latencySum, latencyCount)
	card.P95LatencyMs = percentile(latencies, 0.95)
	return card
}

// buildTopKeys returns the top keys by cost, sorted descending (default
// 5, AD2).
func buildTopKeys(keys []*meteringv1.UsageKeyRow, costs map[string]int64) []*meteringv1.UsageKeyTopRow {
	sorted := make([]*meteringv1.UsageKeyRow, len(keys))
	copy(sorted, keys)
	// Sort by cost descending, then request count descending.
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			ci := costs[sorted[i].ApiKeyId]
			cj := costs[sorted[j].ApiKeyId]
			if cj > ci || (cj == ci && sorted[j].RequestCount > sorted[i].RequestCount) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	n := len(sorted)
	if n > 5 {
		n = 5
	}
	out := make([]*meteringv1.UsageKeyTopRow, 0, n)
	for i := 0; i < n; i++ {
		k := sorted[i]
		out = append(out, &meteringv1.UsageKeyTopRow{
			ApiKeyId:       k.ApiKeyId,
			ApiKeyName:     k.ApiKeyName,
			TotalCostCents: costs[k.ApiKeyId],
			RequestCount:   k.RequestCount,
			TotalTokens:    k.TotalTokens,
		})
	}
	return out
}

// buildUsageKeysSeries maps the bucket rows to the wire series points.
func buildUsageKeysSeries(buckets []UsageKeysBucketRow, _ map[string]int64) []*meteringv1.UsageKeysSeriesPoint {
	out := make([]*meteringv1.UsageKeysSeriesPoint, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, &meteringv1.UsageKeysSeriesPoint{
			Bucket:         b.Bucket,
			RequestCount:   b.RequestCount,
			ErrorCount:     b.ErrorCount,
			TotalTokens:    b.TotalTokens,
			TotalCostCents: b.TotalCostCents,
			AvgLatencyMs:   b.AvgLatencyMs,
			P95LatencyMs:   b.P95LatencyMs,
		})
	}
	return out
}
