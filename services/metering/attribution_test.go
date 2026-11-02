package metering

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptionalAndDerefID(t *testing.T) {
	assert.Nil(t, optionalID(""))
	v := optionalID("u1")
	require.NotNil(t, v)
	assert.Equal(t, "u1", *v)
	assert.Equal(t, "u1", derefID(v))
	assert.Empty(t, derefID(nil))
}

func TestUsageGroupKeyProjectsAttribution(t *testing.T) {
	owner := "u1"
	project := "p1"
	v := &Voucher{APIKeyID: "k1", ModelID: "m1", OwnerUserID: &owner, ProjectID: &project}
	assert.Equal(t, "k1", usageGroupKey(v, "api_key"))
	assert.Equal(t, "m1", usageGroupKey(v, "model"))
	assert.Equal(t, "u1", usageGroupKey(v, "user"))
	assert.Equal(t, "p1", usageGroupKey(v, "project"))
	// A shared key and an unassigned project are the empty bucket.
	assert.Empty(t, usageGroupKey(&Voucher{}, "user"))
	assert.Empty(t, usageGroupKey(&Voucher{}, "project"))
}

func TestValidateEventRejectsOversizedAttribution(t *testing.T) {
	long := make([]byte, maxOrgIDLen+1)
	for i := range long {
		long[i] = 'a'
	}
	ev := &meteringEvent{
		RequestID: "r1", OrganizationID: "org-a", APIKeyID: "k1", ModelID: "m1",
		CompletedAt: 1,
		OwnerUserID: string(long),
	}
	assert.Error(t, validateEvent(ev))
	ev.OwnerUserID = "u1"
	ev.ProjectID = string(long)
	assert.Error(t, validateEvent(ev))
	ev.ProjectID = "p1"
	assert.NoError(t, validateEvent(ev))
}

func TestSettleBucketSnapshotsOwnership(t *testing.T) {
	db := newMeteringTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	owner := "u1"
	project := "p1"
	hour := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	_, err := repo.IngestVoucher(ctx, &Voucher{
		RequestID: "r1", OrganizationID: "org-a", APIKeyID: "k1", ModelID: "m1",
		PromptTokens: 10, OwnerUserID: &owner, ProjectID: &project,
		CompletedAt: hour.Add(5 * time.Minute),
	})
	require.NoError(t, err)
	bucket := HourBucket{APIKeyID: "k1", OrganizationID: "org-a", PeriodStart: hour.Unix(),
		OwnerUserID: &owner, ProjectID: &project}
	record, err := repo.SettleBucket(ctx, bucket, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.Equal(t, "u1", derefID(record.OwnerUserID))
	assert.Equal(t, "p1", derefID(record.ProjectID))
}
