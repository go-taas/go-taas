package infer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// fakeSubjectLabels resolves ids per subject type like the real composite.
type fakeSubjectLabels map[string]map[string]string

func (f fakeSubjectLabels) LabelSubjects(_ context.Context, subjectType string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if label, ok := f[subjectType][id]; ok {
			out[id] = label
		}
	}
	return out, nil
}

// accessTestService seeds one service and returns a service wired with a
// platform administrator and a label resolver.
func accessTestService(t *testing.T) (*Service, *InferenceServiceRepository, *InferenceService) {
	t.Helper()
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	row := seedService(t, repo, "org-a", "svc-access", StateRunning, time.Now())
	svc := New(nil)
	svc.repo = repo
	svc.SetPlatformAdminResolver(fakePlatformAdmin{admin: true})
	svc.SetSubjectLabeller(fakeSubjectLabels{
		AccessSubjectOrg:     {"org-1": "Acme", "org-2": "Globex"},
		AccessSubjectProject: {"proj-1": "Search"},
		AccessSubjectUser:    {"user-1": "Alice"},
	})
	return svc, repo, row
}

// TestInferenceServiceAccessDefaultIsEveryone is the default an operator
// expects: a new service is usable without anyone granting it.
func TestInferenceServiceAccessDefaultIsEveryone(t *testing.T) {
	svc, _, row := accessTestService(t)

	resp, err := svc.GetInferenceServiceAccess(context.Background(), &inferv1.GetInferenceServiceAccessRequest{ServiceId: row.ID})
	require.NoError(t, err)
	assert.False(t, resp.GetRestricted())
	assert.Empty(t, resp.GetSubjects())
}

// TestSetInferenceServiceAccessRestrictsAndLabels covers the whole round
// trip: the policy is stored, read back with display labels, and reported
// as restricted on the service summary.
func TestSetInferenceServiceAccessRestrictsAndLabels(t *testing.T) {
	svc, _, row := accessTestService(t)

	set, err := svc.SetInferenceServiceAccess(context.Background(), &inferv1.SetInferenceServiceAccessRequest{
		ServiceId:  row.ID,
		Restricted: true,
		Subjects: []*inferv1.InferenceServiceAccessSubject{
			{SubjectType: AccessSubjectOrg, SubjectId: "org-1"},
			{SubjectType: AccessSubjectOrg, SubjectId: "org-2"},
			{SubjectType: AccessSubjectProject, SubjectId: "proj-1"},
			{SubjectType: AccessSubjectUser, SubjectId: "user-1"},
		},
	})
	require.NoError(t, err)
	assert.True(t, set.GetRestricted())
	require.Len(t, set.GetSubjects(), 4)
	labels := map[string]string{}
	for _, subject := range set.GetSubjects() {
		labels[subject.GetSubjectType()+"/"+subject.GetSubjectId()] = subject.GetLabel()
	}
	assert.Equal(t, "Acme", labels["org/org-1"])
	assert.Equal(t, "Globex", labels["org/org-2"])
	assert.Equal(t, "Search", labels["project/proj-1"])
	assert.Equal(t, "Alice", labels["user/user-1"])

	// The service summary carries the flag so the list can show it.
	get, err := svc.GetInferenceService(orgContext("org-a"), &inferv1.GetInferenceServiceRequest{ServiceId: row.ID})
	require.NoError(t, err)
	assert.True(t, get.GetService().GetAccessRestricted())

	// Clearing the policy restores "everyone".
	cleared, err := svc.SetInferenceServiceAccess(context.Background(), &inferv1.SetInferenceServiceAccessRequest{
		ServiceId: row.ID, Restricted: false,
	})
	require.NoError(t, err)
	assert.False(t, cleared.GetRestricted())
	assert.Empty(t, cleared.GetSubjects())

	get, err = svc.GetInferenceService(orgContext("org-a"), &inferv1.GetInferenceServiceRequest{ServiceId: row.ID})
	require.NoError(t, err)
	assert.False(t, get.GetService().GetAccessRestricted())
}

// TestSetInferenceServiceAccessRejectsBadPolicies keeps a typo from
// silently locking everyone out or storing a meaningless row.
func TestSetInferenceServiceAccessRejectsBadPolicies(t *testing.T) {
	svc, _, row := accessTestService(t)

	// restricted=true with no subjects would mean "nobody".
	_, err := svc.SetInferenceServiceAccess(context.Background(), &inferv1.SetInferenceServiceAccessRequest{
		ServiceId: row.ID, Restricted: true,
	})
	assert.EqualValues(t, apierrors.CodeInferAccessInvalid, apierrors.CodeOf(err))
	assert.Contains(t, err.Error(), "at least one")

	// An unknown subject type is a typo, not a policy.
	_, err = svc.SetInferenceServiceAccess(context.Background(), &inferv1.SetInferenceServiceAccessRequest{
		ServiceId:  row.ID,
		Restricted: true,
		Subjects:   []*inferv1.InferenceServiceAccessSubject{{SubjectType: "team", SubjectId: "t-1"}},
	})
	assert.EqualValues(t, apierrors.CodeInferAccessInvalid, apierrors.CodeOf(err))
	assert.Contains(t, err.Error(), "unknown access subject type")

	// A missing subject id cannot be resolved to anything.
	_, err = svc.SetInferenceServiceAccess(context.Background(), &inferv1.SetInferenceServiceAccessRequest{
		ServiceId:  row.ID,
		Restricted: true,
		Subjects:   []*inferv1.InferenceServiceAccessSubject{{SubjectType: AccessSubjectOrg}},
	})
	assert.EqualValues(t, apierrors.CodeInferAccessInvalid, apierrors.CodeOf(err))

	// Nothing was stored by the rejected writes.
	get, err := svc.GetInferenceServiceAccess(context.Background(), &inferv1.GetInferenceServiceAccessRequest{ServiceId: row.ID})
	require.NoError(t, err)
	assert.False(t, get.GetRestricted())
}

// TestSetInferenceServiceAccessDeduplicates keeps a repeated subject from
// violating the unique index at write time.
func TestSetInferenceServiceAccessDeduplicates(t *testing.T) {
	svc, _, row := accessTestService(t)

	resp, err := svc.SetInferenceServiceAccess(context.Background(), &inferv1.SetInferenceServiceAccessRequest{
		ServiceId:  row.ID,
		Restricted: true,
		Subjects: []*inferv1.InferenceServiceAccessSubject{
			{SubjectType: AccessSubjectOrg, SubjectId: "org-1"},
			{SubjectType: "ORG", SubjectId: "org-1"},
		},
	})
	require.NoError(t, err)
	assert.Len(t, resp.GetSubjects(), 1)
	assert.Equal(t, AccessSubjectOrg, resp.GetSubjects()[0].GetSubjectType())
}

// TestInferenceServiceAccessRequiresPlatformAdmin: a service belongs to the
// platform, so deciding who may use it is a platform decision.
func TestInferenceServiceAccessRequiresPlatformAdmin(t *testing.T) {
	svc, _, row := accessTestService(t)
	svc.SetPlatformAdminResolver(fakePlatformAdmin{admin: false})

	_, err := svc.GetInferenceServiceAccess(context.Background(), &inferv1.GetInferenceServiceAccessRequest{ServiceId: row.ID})
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	_, err = svc.SetInferenceServiceAccess(context.Background(), &inferv1.SetInferenceServiceAccessRequest{ServiceId: row.ID})
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// TestInferenceServiceAccessUnknownService keeps a bad id explicit.
func TestInferenceServiceAccessUnknownService(t *testing.T) {
	svc, _, _ := accessTestService(t)
	_, err := svc.GetInferenceServiceAccess(context.Background(), &inferv1.GetInferenceServiceAccessRequest{
		ServiceId: "00000000-0000-4000-8000-000000000000",
	})
	assert.EqualValues(t, apierrors.CodeInferServiceNotFound, apierrors.CodeOf(err))
}

// TestListReportsAccessRestriction covers the list projection: one query
// marks the restricted rows so the table can show it.
func TestListReportsAccessRestriction(t *testing.T) {
	svc, repo, row := accessTestService(t)
	other := seedService(t, repo, "org-a", "svc-open", StateRunning, time.Now())

	_, err := svc.SetInferenceServiceAccess(context.Background(), &inferv1.SetInferenceServiceAccessRequest{
		ServiceId:  row.ID,
		Restricted: true,
		Subjects:   []*inferv1.InferenceServiceAccessSubject{{SubjectType: AccessSubjectOrg, SubjectId: "org-1"}},
	})
	require.NoError(t, err)

	resp, err := svc.ListInferenceServices(orgContext("org-a"), &inferv1.ListInferenceServicesRequest{})
	require.NoError(t, err)
	flags := map[string]bool{}
	for _, summary := range resp.GetServices() {
		flags[summary.GetServiceId()] = summary.GetAccessRestricted()
	}
	assert.True(t, flags[row.ID])
	assert.False(t, flags[other.ID])
}
