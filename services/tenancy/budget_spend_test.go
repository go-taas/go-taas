package tenancy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"
)

// fakeSpendReader is the SpendReader seam, keyed by the scope it was asked
// about so a test can prove the right scope was read.
type fakeSpendReader struct {
	byScope map[string]int64
	err     error
	asked   []string
}

func (f *fakeSpendReader) SpentCents(_ context.Context, orgID, projectID, ownerID string) (int64, error) {
	key := orgID + "|" + projectID + "|" + ownerID
	f.asked = append(f.asked, key)
	if f.err != nil {
		return 0, f.err
	}
	return f.byScope[key], nil
}

// TestBudgetsReportSpendAndRemaining covers the closed loop the consoles show:
// every budget tier reports what has been consumed in the current cycle and
// what is left, each read with the scope that tier is measured against.
func TestBudgetsReportSpendAndRemaining(t *testing.T) {
	db := newQuotaTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.Create(&Organization{
		ID: "org-a", DisplayName: "A", State: StateActive, MaxConcurrentRequests: 10,
	}).Error)
	require.NoError(t, db.Create(&OrgMember{
		OrganizationID: "org-a", UserID: testUser, Role: RoleMember, JoinedAt: time.Now().UTC(),
	}).Error)
	require.NoError(t, db.Create(&Project{
		ID: "p1", OrganizationID: "org-a", DisplayName: "P", State: StateActive,
		MonthlySpendLimitCents: 10000,
	}).Error)
	require.NoError(t, db.Create(&ProjectMember{
		ProjectID: "p1", UserID: testUser, Role: RoleMember, JoinedAt: time.Now().UTC(),
		MonthlySpendLimitCents: 2500,
	}).Error)
	require.NoError(t, db.Create(&UserQuota{
		OrganizationID: "org-a", UserID: testUser, MonthlySpendLimitCents: 5000,
	}).Error)

	spend := &fakeSpendReader{byScope: map[string]int64{
		"org-a|p1|":            4000, // the project spent 40.00 of its 100.00
		"org-a|p1|" + testUser: 1000, // this member spent 10.00 of their 25.00
		"org-a||" + testUser:   3000, // the person spent 30.00 of their 50.00 org-wide
	}}
	svc := NewForFVT(db)
	svc.SetSpendReader(spend)
	// The quota reads gate on the caller's role in the org; a platform
	// administrator administers any tenant, which is the shortest legitimate
	// session for a unit test.
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RolePlatformAdmin}})
	svc.SetSessionResolver(fakeQuotaSessionResolver{user: "admin-1"})
	svc.SetSessionOrgResolver(fakeQuotaSessionOrgResolver{org: "org-a"})

	// Project tier.
	pq, err := svc.GetProjectQuota(ctx, &tenancyv1.GetProjectQuotaRequest{ProjectId: "p1"})
	require.NoError(t, err)
	assert.EqualValues(t, 4000, pq.GetQuota().GetSpentThisCycleCents())
	assert.EqualValues(t, 6000, pq.GetQuota().GetRemainingCents())

	// Member tier: the spend is the member's in this project, not the
	// project total and not the member's org-wide total.
	mq, err := svc.ListProjectMemberQuotas(ctx, &tenancyv1.ListProjectMemberQuotasRequest{ProjectId: "p1"})
	require.NoError(t, err)
	require.Len(t, mq.GetQuotas(), 1)
	assert.EqualValues(t, 1000, mq.GetQuotas()[0].GetSpentThisCycleCents())
	assert.EqualValues(t, 1500, mq.GetQuotas()[0].GetRemainingCents())

	// Personal tier.
	uq, err := svc.GetUserQuota(ctx, &tenancyv1.GetUserQuotaRequest{OrganizationId: "org-a", UserId: testUser})
	require.NoError(t, err)
	assert.EqualValues(t, 3000, uq.GetQuota().GetSpentThisCycleCents())
	assert.EqualValues(t, 2000, uq.GetQuota().GetRemainingCents())

	// An unlimited budget reports 0 remaining (0 means unlimited, so the
	// console compares against the limit to tell the two apart).
	assert.EqualValues(t, 0, remainingCents(0, 500))

	// A spend read failure degrades to 0 instead of failing the quota read.
	svc.SetSpendReader(&fakeSpendReader{err: assert.AnError})
	pq, err = svc.GetProjectQuota(ctx, &tenancyv1.GetProjectQuotaRequest{ProjectId: "p1"})
	require.NoError(t, err, "a ledger read failure must not fail a quota read")
	assert.EqualValues(t, 0, pq.GetQuota().GetSpentThisCycleCents())
}

// TestGetMyQuotaReportsPersonalAndProjectBudgets covers the member's own view:
// the personal budget's spend, and each project membership's spend in that
// project, each with the currency the console labels amounts in.
func TestGetMyQuotaReportsPersonalAndProjectBudgets(t *testing.T) {
	db := newQuotaTestDB(t)
	require.NoError(t, db.Create(&Organization{ID: "org-a", DisplayName: "A", State: StateActive}).Error)
	require.NoError(t, db.Create(&OrgMember{
		OrganizationID: "org-a", UserID: testUser, Role: RoleMember, JoinedAt: time.Now().UTC(),
	}).Error)
	require.NoError(t, db.Create(&Project{ID: "p1", OrganizationID: "org-a", DisplayName: "P", State: StateActive}).Error)
	require.NoError(t, db.Create(&ProjectMember{
		ProjectID: "p1", UserID: testUser, Role: RoleMember, JoinedAt: time.Now().UTC(),
		MonthlySpendLimitCents: 2500,
	}).Error)
	require.NoError(t, db.Create(&UserQuota{
		OrganizationID: "org-a", UserID: testUser, MonthlySpendLimitCents: 5000,
	}).Error)

	spend := &fakeSpendReader{byScope: map[string]int64{
		"org-a||" + testUser:   1200,
		"org-a|p1|" + testUser: 700,
	}}
	svc := NewForFVT(db)
	svc.SetSpendReader(spend)
	svc.SetSessionResolver(fakeQuotaSessionResolver{user: testUser})
	svc.SetSessionOrgResolver(fakeQuotaSessionOrgResolver{org: "org-a"})

	resp, err := svc.GetMyQuota(context.Background(), &tenancyv1.GetMyQuotaRequest{})
	require.NoError(t, err)
	assert.EqualValues(t, 1200, resp.GetPersonalSpentThisCycleCents())
	assert.EqualValues(t, 3800, resp.GetPersonalRemainingCents())
	assert.NotEmpty(t, resp.GetCurrency())

	require.Len(t, resp.GetProjectMemberQuotas(), 1)
	project := resp.GetProjectMemberQuotas()[0]
	assert.EqualValues(t, 700, project.GetSpentThisCycleCents())
	assert.EqualValues(t, 1800, project.GetRemainingCents())
}

// testUser is a UUID because the personal quota read validates the shape
// before it queries a uuid column.
const testUser = "11111111-1111-4111-8111-111111111111"
