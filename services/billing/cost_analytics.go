package billing

import (
	"context"
	"strings"

	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// costDimensions is the set of supported cost dimensions (AD5).
var costDimensions = map[string]bool{
	"organization": true,
	"model":        true,
	"api_key":      true,
	"user":         true,
	"project":      true,
}

// GetCostAnalyticsOverview returns cost analytics: summary cards, a
// dimension breakdown, a cost trend, and cost-per-token (feature #29,
// AC1-AC2). The admin binding is the fleet-wide view (cross-org by
// default, optional org filter, gated by the caller's role, AD10); the
// user binding is hard-scoped to the caller's org (AD9).
func (s *Service) GetCostAnalyticsOverview(ctx context.Context, req *billingv1.GetCostAnalyticsOverviewRequest) (*billingv1.GetCostAnalyticsOverviewResponse, error) {
	surface := surfaceFromContext(ctx)

	var orgFilter string
	if surface == SurfaceAdmin {
		// The admin fleet view is cross-org by default (AD10).
		orgFilter = strings.TrimSpace(req.GetOrganizationId())
		if orgFilter == "" {
			orgFilter, _ = s.resolveOrg(ctx)
		}
		if err := s.requireAdminRole(ctx, orgFilter); err != nil {
			return nil, err
		}
	} else {
		// User binding: hard-scoped to the caller's org (AD9).
		org, err := s.resolveOrg(ctx)
		if err != nil {
			return nil, err
		}
		orgFilter = org
	}
	dimension := strings.TrimSpace(req.GetDimension())
	if dimension == "" {
		dimension = "organization"
	}
	if !costDimensions[dimension] {
		return nil, apierrors.New(apierrors.CodeCostDimensionInvalid)
	}
	since, until, err := validateRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	repo, err := s.costAnalyticsRepo()
	if err != nil {
		return nil, err
	}
	bucketSize := bucketSizeForRange(since, until)
	buckets, breakdown, err := repo.AggregateCost(ctx, orgFilter, dimension, "", since, until, bucketSize)
	if err != nil {
		return nil, err
	}
	watermark, err := repo.DataThrough(ctx, orgFilter, since, until, bucketSize)
	if err != nil {
		return nil, err
	}

	cards := buildCostCards(buckets, breakdown, watermark)
	rows := make([]*billingv1.CostBreakdownRow, 0, len(breakdown))
	for _, b := range breakdown {
		rows = append(rows, &billingv1.CostBreakdownRow{
			DimensionValue: b.DimensionValue,
			DimensionName:  s.dimensionName(dimension, b.DimensionValue),
			TotalCostCents: b.TotalCostCents,
			TotalCostMicros: b.TotalCostMicros,
			TotalTokens:    b.TotalTokens,
		})
	}
	series := buildCostSeries(buckets)

	return &billingv1.GetCostAnalyticsOverviewResponse{
		Response:  okResponse(),
		Cards:     cards,
		Breakdown: rows,
		Series:    series,
	}, nil
}

// GetCostAnalytics returns single-dimension-value cost analytics:
// summary cards and a cost trend (feature #29, AC3-AC4). The admin
// binding covers any dimension value (AD10); the user binding is
// hard-scoped to the caller's org (AD9).
func (s *Service) GetCostAnalytics(ctx context.Context, req *billingv1.GetCostAnalyticsRequest) (*billingv1.GetCostAnalyticsResponse, error) {
	dimension := strings.TrimSpace(req.GetDimension())
	if !costDimensions[dimension] {
		return nil, apierrors.New(apierrors.CodeCostDimensionInvalid)
	}
	value := strings.TrimSpace(req.GetValue())
	if value == "" {
		return nil, apierrors.New(apierrors.CodeCostDimensionValueNotFound)
	}
	surface := surfaceFromContext(ctx)

	var orgFilter string
	if surface == SurfaceAdmin {
		orgFilter = strings.TrimSpace(req.GetOrganizationId())
		if orgFilter == "" {
			orgFilter, _ = s.resolveOrg(ctx)
		}
		if err := s.requireAdminRole(ctx, orgFilter); err != nil {
			return nil, err
		}
	} else {
		org, err := s.resolveOrg(ctx)
		if err != nil {
			return nil, err
		}
		orgFilter = org
	}

	since, until, err := validateRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	repo, err := s.costAnalyticsRepo()
	if err != nil {
		return nil, err
	}
	bucketSize := bucketSizeForRange(since, until)
	buckets, _, err := repo.AggregateCost(ctx, orgFilter, dimension, value, since, until, bucketSize)
	if err != nil {
		return nil, err
	}
	watermark, err := repo.DataThrough(ctx, orgFilter, since, until, bucketSize)
	if err != nil {
		return nil, err
	}
	// An unknown dimension value returns 11302 (AD2).
	if len(buckets) == 0 {
		return nil, apierrors.New(apierrors.CodeCostDimensionValueNotFound)
	}

	cards := buildCostCards(buckets, nil, watermark)
	series := buildCostSeries(buckets)

	return &billingv1.GetCostAnalyticsResponse{
		Response: okResponse(),
		Cards:    cards,
		Series:   series,
	}, nil
}

// costAnalyticsRepo lazily wires and returns the cost-analytics
// repository.
func (s *Service) costAnalyticsRepo() (*CostAnalyticsRepository, error) {
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	return NewCostAnalyticsRepository(db), nil
}

// dimensionName resolves a dimension value to a display name (read-only,
// AD3). Unknown values return the raw value.
func (s *Service) dimensionName(dimension, value string) string {
	db, err := s.gormDB()
	if err != nil {
		return value
	}
	switch dimension {
	case "model":
		var name string
		if err := db.Table("models").Select("name").Where("id = ?", value).Scan(&name).Error; err == nil && name != "" {
			return name
		}
	case "api_key":
		var name string
		if err := db.Table("api_keys").Select("name").Where("id = ?", value).Scan(&name).Error; err == nil && name != "" {
			return name
		}
	case "project":
		var name string
		if err := db.Table("projects").Select("display_name").Where("id = ?", value).Scan(&name).Error; err == nil && name != "" {
			return name
		}
	case "user":
		var name string
		if err := db.Table("users").Select("display_name").Where("id = ?", value).Scan(&name).Error; err == nil && name != "" {
			return name
		}
	}
	return value
}

// buildCostCards derives the headline summary cards from the bucket rows
// and the breakdown. cost_per_token and top_dimension_share_pct are
// derived client-side; the wire carries integer cents and integer tokens
// only (AD3).
func buildCostCards(buckets []CostBucketRow, breakdown []CostBreakdownAggRow, watermark int64) *billingv1.CostAnalyticsCard {
	card := &billingv1.CostAnalyticsCard{DataThrough: watermark}
	for _, b := range buckets {
		card.TotalCostCents += b.TotalCostCents
		card.TotalCostMicros += b.TotalCostMicros
		card.TotalTokens += b.TotalTokens
	}
	if len(breakdown) > 0 {
		card.TopDimensionValue = breakdown[0].DimensionValue
	}
	return card
}

// buildCostSeries maps the bucket rows to the wire series points.
func buildCostSeries(buckets []CostBucketRow) []*billingv1.CostSeriesPoint {
	out := make([]*billingv1.CostSeriesPoint, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, &billingv1.CostSeriesPoint{
			Bucket:         b.Bucket,
			TotalCostCents: b.TotalCostCents,
			TotalCostMicros: b.TotalCostMicros,
			TotalTokens:    b.TotalTokens,
		})
	}
	return out
}
