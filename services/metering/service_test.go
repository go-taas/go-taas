package metering

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	meteringv1 "github.com/go-taas/go-taas/proto/taas/metering/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func newServiceTestEnv(t *testing.T) *Service {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Voucher{}, &UsageRecord{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return NewForFVT(db, nil)
}

func withOrg(ctx context.Context, orgID string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs(organizationMetadataKey, orgID))
}

func validEvent(requestID string) *meteringv1.IngestMeteringEventRequest {
	return &meteringv1.IngestMeteringEventRequest{
		RequestId:      requestID,
		OrganizationId: "org-1",
		ApiKeyId:       "key-1",
		ModelId:        "model-a",
		ServiceId:      "svc-1",
		CompletedAt:    time.Now().Add(-time.Hour).Unix(),
		Usage: &meteringv1.TokenUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			CachedTokens:     10,
			ReasoningTokens:  5,
		},
	}
}

// AC2: the validation matrix rejects bad events with 10401 and writes
// nothing.
func TestServiceIngestValidation(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := context.Background()

	cases := []struct {
		name string
		mut  func(*meteringv1.IngestMeteringEventRequest)
	}{
		{"missing request id", func(r *meteringv1.IngestMeteringEventRequest) { r.RequestId = "" }},
		{"request id too long", func(r *meteringv1.IngestMeteringEventRequest) {
			r.RequestId = string(make([]byte, maxRequestIDLen+1))
		}},
		{"missing org", func(r *meteringv1.IngestMeteringEventRequest) { r.OrganizationId = "" }},
		{"missing api key", func(r *meteringv1.IngestMeteringEventRequest) { r.ApiKeyId = "" }},
		{"missing model", func(r *meteringv1.IngestMeteringEventRequest) { r.ModelId = "" }},
		{"negative tokens", func(r *meteringv1.IngestMeteringEventRequest) {
			r.Usage.PromptTokens = -1
		}},
		{"zero completed at", func(r *meteringv1.IngestMeteringEventRequest) { r.CompletedAt = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validEvent(fmt.Sprintf("req-%s", tc.name))
			tc.mut(req)
			_, err := svc.IngestMeteringEvent(ctx, req)
			require.Error(t, err)
			ae, ok := apierrors.As(err)
			require.True(t, ok)
			assert.Equal(t, apierrors.CodeMeteringEventInvalid, ae.Code)
		})
	}

	var count int64
	require.NoError(t, svc.repo.DB(ctx).Model(&Voucher{}).Count(&count).Error)
	assert.Equal(t, int64(0), count, "no voucher written for invalid events")
}

// AC1 (RPC path): a valid event is stored and a duplicate returns the
// same voucher id.
func TestServiceIngestIdempotent(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := context.Background()

	first, err := svc.IngestMeteringEvent(ctx, validEvent("req-dup"))
	require.NoError(t, err)
	assert.NotEmpty(t, first.VoucherId)

	second, err := svc.IngestMeteringEvent(ctx, validEvent("req-dup"))
	require.NoError(t, err)
	assert.Equal(t, first.VoucherId, second.VoucherId)
}

// AC9: GetVoucher on an unknown id returns 10403.
func TestServiceGetVoucherNotFound(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withOrg(context.Background(), "org-1")

	_, err := svc.GetVoucher(ctx, &meteringv1.GetVoucherRequest{VoucherId: "voucher-missing"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeMeteringVoucherNotFound, ae.Code)
}

// AC10: an inverted or over-long range returns 10404.
func TestServiceRangeValidation(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withOrg(context.Background(), "org-1")
	now := time.Now().Unix()

	_, err := svc.ListVouchers(ctx, &meteringv1.ListVouchersRequest{Since: now, Until: now - 10})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeMeteringRangeInvalid, ae.Code)

	_, err = svc.ListVouchers(ctx, &meteringv1.ListVouchersRequest{Since: now - 93*24*3600, Until: now})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeMeteringRangeInvalid, ae.Code)

	// An unknown group_by is also a 10404.
	_, err = svc.GetUsageSummary(ctx, &meteringv1.GetUsageSummaryRequest{GroupBy: "user"})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeMeteringRangeInvalid, ae.Code)
}

// AC14: queries without an organization header are unauthorized, and
// one org never sees another org's data.
func TestServiceOrgScoping(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := context.Background()

	// No header: 10001.
	_, err := svc.ListVouchers(ctx, &meteringv1.ListVouchersRequest{})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeUnauthorized, ae.Code)

	// Seed one voucher for org-1.
	_, err = svc.IngestMeteringEvent(ctx, validEvent("req-org-1"))
	require.NoError(t, err)

	// org-2 sees nothing.
	resp, err := svc.ListVouchers(withOrg(ctx, "org-2"), &meteringv1.ListVouchersRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.GetVouchers())
	assert.Equal(t, int64(0), resp.GetPageMeta().GetTotal())

	// org-1 sees its voucher.
	resp, err = svc.ListVouchers(withOrg(ctx, "org-1"), &meteringv1.ListVouchersRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.GetVouchers(), 1)
	assert.Equal(t, "org-1", resp.GetVouchers()[0].GetOrganizationId())
}

// ListVouchers pagination defaults and caps.
func TestServiceListVouchersPagination(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withOrg(context.Background(), "org-1")

	for i := 0; i < 5; i++ {
		req := validEvent(fmt.Sprintf("req-page-%d", i))
		req.CompletedAt = time.Now().Add(-time.Duration(10-i) * time.Minute).Unix()
		_, err := svc.IngestMeteringEvent(ctx, req)
		require.NoError(t, err)
	}

	resp, err := svc.ListVouchers(ctx, &meteringv1.ListVouchersRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(listDefaultLimit), resp.GetPageMeta().GetLimit())
	assert.Len(t, resp.GetVouchers(), 5)

	resp, err = svc.ListVouchers(ctx, &meteringv1.ListVouchersRequest{
		Page: &commonv1.PageRequest{Offset: 2, Limit: 2},
	})
	require.NoError(t, err)
	assert.Len(t, resp.GetVouchers(), 2)
	assert.Equal(t, int64(2), resp.GetPageMeta().GetOffset())

	// Limit is capped at 100.
	resp, err = svc.ListVouchers(ctx, &meteringv1.ListVouchersRequest{
		Page: &commonv1.PageRequest{Limit: 1000},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(listMaxLimit), resp.GetPageMeta().GetLimit())
}

// GetUsageSummary + ListUsageRecords end-to-end over the service layer.
func TestServiceUsageQueries(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withOrg(context.Background(), "org-1")
	hour := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)

	for i := 0; i < 3; i++ {
		req := validEvent(fmt.Sprintf("req-usage-%d", i))
		req.CompletedAt = hour.Add(time.Duration(i) * time.Minute).Unix()
		_, err := svc.IngestMeteringEvent(ctx, req)
		require.NoError(t, err)
	}

	// Settle the hour.
	_, err := svc.repo.SettleBucket(ctx, HourBucket{APIKeyID: "key-1", OrganizationID: "org-1", PeriodStart: hour.Unix()}, time.Now().UTC())
	require.NoError(t, err)

	summary, err := svc.GetUsageSummary(ctx, &meteringv1.GetUsageSummaryRequest{})
	require.NoError(t, err)
	require.Len(t, summary.GetRows(), 1)
	row := summary.GetRows()[0]
	assert.Equal(t, "key-1", row.GetGroupKey())
	assert.Equal(t, int64(300), row.GetPromptTokens())
	assert.Equal(t, int64(3), row.GetRequestCount())
	assert.Equal(t, int64(1), row.GetSettledHours())
	assert.Equal(t, int64(0), row.GetPendingHours())

	records, err := svc.ListUsageRecords(ctx, &meteringv1.ListUsageRecordsRequest{})
	require.NoError(t, err)
	require.Len(t, records.GetRecords(), 1)
	rec := records.GetRecords()[0]
	assert.Equal(t, "key-1", rec.GetApiKeyId())
	assert.Equal(t, int64(3), rec.GetRequestCount())
	assert.Equal(t, hour.Unix(), rec.GetPeriodStart())
	assert.Equal(t, hour.Unix()+3600, rec.GetPeriodEnd())
}

// GetVoucher returns the stored voucher with the settled flag.
func TestServiceGetVoucher(t *testing.T) {
	svc := newServiceTestEnv(t)
	ctx := withOrg(context.Background(), "org-1")

	ingested, err := svc.IngestMeteringEvent(ctx, validEvent("req-get"))
	require.NoError(t, err)

	resp, err := svc.GetVoucher(ctx, &meteringv1.GetVoucherRequest{VoucherId: ingested.GetVoucherId()})
	require.NoError(t, err)
	assert.Equal(t, "req-get", resp.GetVoucher().GetRequestId())
	assert.Equal(t, "svc-1", resp.GetVoucher().GetServiceId())
	assert.False(t, resp.GetVoucher().GetSettled())
	assert.Equal(t, int64(100), resp.GetVoucher().GetUsage().GetPromptTokens())
}

// validateRange defaults: until=now, since=until-24h.
func TestValidateRangeDefaults(t *testing.T) {
	before := time.Now().Unix()
	since, until, err := validateRange(0, 0)
	require.NoError(t, err)
	assert.InDelta(t, before, until, 5)
	assert.InDelta(t, until-defaultRangeHours*3600, since, 5)
}

// The service registers itself on the gRPC server and exposes the
// gateway handler (wiring smoke test).
func TestServiceRegistration(t *testing.T) {
	svc := newServiceTestEnv(t)
	assert.Equal(t, ServiceName, svc.ServiceName())
	assert.NotNil(t, svc.GetServiceHandlerRegisterFn())

	grpcServer := grpc.NewServer()
	svc.AttachToServer(grpcServer)
	// Registering twice would panic; a single registration proves the
	// wiring is sound.
	assert.NotNil(t, grpcServer.GetServiceInfo()[meteringv1.MeteringService_ServiceDesc.ServiceName])
}
