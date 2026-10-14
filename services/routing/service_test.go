package routing

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	routingv1 "github.com/go-taas/go-taas/proto/taas/routing/v1"
)

// fakeModelCatalog is an in-memory model-module double implementing the
// ModelCatalog seam.
type fakeModelCatalog struct {
	byID   map[string]*CatalogModel
	grants map[string][]string // modelID -> granted orgs (empty = default-allow)
}

func newFakeModelCatalog() *fakeModelCatalog {
	return &fakeModelCatalog{
		byID:   map[string]*CatalogModel{},
		grants: map[string][]string{},
	}
}

func (c *fakeModelCatalog) addModel(id, name string, external, published bool) *CatalogModel {
	m := &CatalogModel{ID: id, Name: name, External: external, Published: published}
	c.byID[id] = m
	return m
}

func (c *fakeModelCatalog) GetCatalogModel(_ context.Context, modelID string) (*CatalogModel, error) {
	m, ok := c.byID[modelID]
	if !ok {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}
	return m, nil
}

func (c *fakeModelCatalog) CatalogModelNameExists(_ context.Context, name string) (bool, error) {
	for _, m := range c.byID {
		if m.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// IsModelAuthorized mirrors the #13 default-allow rule: a model with
// zero grants is allowed for every org; a restricted model is allowed
// only for its granted orgs.
func (c *fakeModelCatalog) IsModelAuthorized(_ context.Context, modelID, organizationID string) (bool, error) {
	granted := c.grants[modelID]
	if len(granted) == 0 {
		return true, nil
	}
	for _, org := range granted {
		if org == organizationID {
			return true, nil
		}
	}
	return false, nil
}

// fakeKeyScope is an in-memory auth-module double implementing the
// KeyScopeLister seam.
type fakeKeyScope struct {
	scopes map[string][]string
}

func (k *fakeKeyScope) APIKeyModels(_ context.Context, apiKeyID string) ([]string, error) {
	return k.scopes[apiKeyID], nil
}

// fakeLatency is an in-memory LatencySource double.
type fakeLatency struct {
	p50 map[string]int64
}

func (l *fakeLatency) ObservedP50(_ context.Context, _ []string, _ time.Time) (map[string]int64, error) {
	return l.p50, nil
}

// newRoutingSvc builds a routing service over a fresh in-memory database
// wired with fake seams.
func newRoutingSvc(t *testing.T) (*Service, *fakeModelCatalog, *fakeKeyScope) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	require.NoError(t, MigrateSchemaForFVT(db))
	svc := NewForFVT(db)
	catalog := newFakeModelCatalog()
	keyScope := &fakeKeyScope{scopes: map[string][]string{}}
	svc.SetModelCatalog(catalog)
	svc.SetKeyScopeLister(keyScope)
	// The service tests act as an authenticated org admin (the
	// fail-closed requireAdminRole denies a missing session).
	svc.SetSessionUserResolver(fakeSessionUser{id: "admin-1"})
	svc.SetSessionOrgResolver(fakeSessionOrg{org: "org-1"})
	svc.SetRoleGuard(fakeRoleGuard{})
	return svc, catalog, keyScope
}

// createGroup creates a group and returns its id.
func createGroup(t *testing.T, svc *Service, name, strategy string) string {
	t.Helper()
	resp, err := svc.CreateRoutingGroup(context.Background(), &routingv1.CreateRoutingGroupRequest{
		Name:     name,
		Strategy: strategy,
	})
	require.NoError(t, err)
	return resp.GetGroup().GetGroupId()
}

// setCandidates replaces a group's candidate list.
func setCandidates(t *testing.T, svc *Service, groupID string, candidates ...*routingv1.RoutingGroupCandidateInput) {
	t.Helper()
	_, err := svc.ReplaceRoutingGroupCandidates(context.Background(), &routingv1.ReplaceRoutingGroupCandidatesRequest{
		GroupId:    groupID,
		Candidates: candidates,
	})
	require.NoError(t, err)
}

// setRules replaces a group's rule list.
func setRules(t *testing.T, svc *Service, groupID string, rules ...*routingv1.RoutingGroupRuleInput) {
	t.Helper()
	_, err := svc.ReplaceRoutingGroupRules(context.Background(), &routingv1.ReplaceRoutingGroupRulesRequest{
		GroupId: groupID,
		Rules:   rules,
	})
	require.NoError(t, err)
}

// enableGroup flips a group to enabled.
func enableGroup(t *testing.T, svc *Service, groupID, _ string) {
	t.Helper()
	_, err := svc.UpdateRoutingGroup(context.Background(), &routingv1.UpdateRoutingGroupRequest{
		GroupId: groupID,
		Name:    groupNameOf(t, svc, groupID),
		Enabled: true,
	})
	require.NoError(t, err)
}

// groupNameOf reads a group's name.
func groupNameOf(t *testing.T, svc *Service, groupID string) string {
	t.Helper()
	resp, err := svc.GetRoutingGroup(context.Background(), &routingv1.GetRoutingGroupRequest{GroupId: groupID})
	require.NoError(t, err)
	return resp.GetGroup().GetName()
}

// candidate is a shorthand builder.
func candidate(modelID string, weight, costTier, latencyTier int32, pinned bool) *routingv1.RoutingGroupCandidateInput {
	return &routingv1.RoutingGroupCandidateInput{
		ModelId:     modelID,
		Weight:      weight,
		CostTier:    costTier,
		LatencyTier: latencyTier,
		Pinned:      pinned,
	}
}

// TestCreateRoutingGroupValidation covers the name/strategy validation
// (AC1, §5.4).
func TestCreateRoutingGroupValidation(t *testing.T) {
	svc, catalog, _ := newRoutingSvc(t)
	ctx := context.Background()
	catalog.addModel("m1", "qwen", false, false)

	cases := []struct {
		name string
		req  *routingv1.CreateRoutingGroupRequest
		code apierrors.Code
	}{
		{"uppercase name", &routingv1.CreateRoutingGroupRequest{Name: "Bad"}, apierrors.CodeRoutingGroupNameInvalid},
		{"too short", &routingv1.CreateRoutingGroupRequest{Name: "ab"}, apierrors.CodeRoutingGroupNameInvalid},
		{"leading digit", &routingv1.CreateRoutingGroupRequest{Name: "1abc"}, apierrors.CodeRoutingGroupNameInvalid},
		{"leading hyphen", &routingv1.CreateRoutingGroupRequest{Name: "-abc"}, apierrors.CodeRoutingGroupNameInvalid},
		{"bad strategy", &routingv1.CreateRoutingGroupRequest{Name: "team-a", Strategy: "random"}, apierrors.CodeRoutingGroupStrategyInvalid},
		{"reserved name", &routingv1.CreateRoutingGroupRequest{Name: "qwen"}, apierrors.CodeRoutingGroupNameReserved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateRoutingGroup(ctx, tc.req)
			require.Error(t, err)
			assert.Equal(t, tc.code, apierrors.CodeOf(err))
		})
	}

	// A valid create defaults to weighted and disabled.
	resp, err := svc.CreateRoutingGroup(ctx, &routingv1.CreateRoutingGroupRequest{Name: "team-a"})
	require.NoError(t, err)
	assert.Equal(t, StrategyWeighted, resp.GetGroup().GetStrategy())
	assert.False(t, resp.GetGroup().GetEnabled())

	// Duplicate name → 13102.
	_, err = svc.CreateRoutingGroup(ctx, &routingv1.CreateRoutingGroupRequest{Name: "team-a"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingGroupNameConflict, apierrors.CodeOf(err))
}

// TestUpdateRoutingGroupEnableGuards covers the enable guards (AC3).
func TestUpdateRoutingGroupEnableGuards(t *testing.T) {
	svc, catalog, _ := newRoutingSvc(t)
	ctx := context.Background()
	catalog.addModel("m1", "qwen", false, false)
	catalog.addModel("m2", "deepseek", false, false)

	// Enabling an empty group fails.
	id := createGroup(t, svc, "team-a", StrategyWeighted)
	_, err := svc.UpdateRoutingGroup(ctx, &routingv1.UpdateRoutingGroupRequest{
		GroupId: id, Name: "team-a", Enabled: true,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingGroupStrategyInvalid, apierrors.CodeOf(err))

	// Enabling a manual group without exactly one pin fails: switch the
	// strategy first, then set two unpinned candidates.
	_, err = svc.UpdateRoutingGroup(ctx, &routingv1.UpdateRoutingGroupRequest{
		GroupId: id, Name: "team-a", Strategy: StrategyManual,
	})
	require.NoError(t, err)
	setCandidates(t, svc, id, candidate("m1", 10, 0, 0, false), candidate("m2", 10, 0, 0, false))
	_, err = svc.UpdateRoutingGroup(ctx, &routingv1.UpdateRoutingGroupRequest{
		GroupId: id, Name: "team-a", Strategy: StrategyManual, Enabled: true,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingGroupStrategyInvalid, apierrors.CodeOf(err))

	// Exactly one pin enables.
	setCandidates(t, svc, id, candidate("m1", 10, 0, 0, true), candidate("m2", 10, 0, 0, false))
	_, err = svc.UpdateRoutingGroup(ctx, &routingv1.UpdateRoutingGroupRequest{
		GroupId: id, Name: "team-a", Strategy: StrategyManual, Enabled: true,
	})
	require.NoError(t, err)

	// Rename to a conflict fails.
	other := createGroup(t, svc, "team-b", StrategyWeighted)
	_, err = svc.UpdateRoutingGroup(ctx, &routingv1.UpdateRoutingGroupRequest{
		GroupId: other, Name: "team-a",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingGroupNameConflict, apierrors.CodeOf(err))
}

// TestReplaceCandidatesValidation covers the candidate validation
// (AC5, §5.4).
func TestReplaceCandidatesValidation(t *testing.T) {
	svc, catalog, _ := newRoutingSvc(t)
	ctx := context.Background()
	catalog.addModel("m1", "qwen", false, false)
	id := createGroup(t, svc, "team-a", StrategyWeighted)

	cases := []struct {
		name       string
		candidates []*routingv1.RoutingGroupCandidateInput
		code       apierrors.Code
	}{
		{"empty list", nil, apierrors.CodeRoutingGroupCandidateInvalid},
		{"unknown model", []*routingv1.RoutingGroupCandidateInput{candidate("nope", 10, 0, 0, false)}, apierrors.CodeRoutingGroupCandidateInvalid},
		{"duplicate", []*routingv1.RoutingGroupCandidateInput{candidate("m1", 10, 0, 0, false), candidate("m1", 20, 0, 0, false)}, apierrors.CodeRoutingGroupCandidateExists},
		{"weight too high", []*routingv1.RoutingGroupCandidateInput{candidate("m1", 101, 0, 0, false)}, apierrors.CodeRoutingGroupCandidateInvalid},
		{"cost tier too high", []*routingv1.RoutingGroupCandidateInput{candidate("m1", 10, 6, 0, false)}, apierrors.CodeRoutingGroupCandidateInvalid},
		{"latency tier too low", []*routingv1.RoutingGroupCandidateInput{candidate("m1", 10, 0, -1, false)}, apierrors.CodeRoutingGroupCandidateInvalid},
		{"pin outside manual", []*routingv1.RoutingGroupCandidateInput{candidate("m1", 10, 0, 0, true)}, apierrors.CodeRoutingGroupCandidateInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.ReplaceRoutingGroupCandidates(ctx, &routingv1.ReplaceRoutingGroupCandidatesRequest{
				GroupId: id, Candidates: tc.candidates,
			})
			require.Error(t, err)
			assert.Equal(t, tc.code, apierrors.CodeOf(err))
		})
	}

	// A valid replace persists the declared order and defaults.
	setCandidates(t, svc, id, candidate("m1", 0, 2, 3, false))
	resp, err := svc.GetRoutingGroup(ctx, &routingv1.GetRoutingGroupRequest{GroupId: id})
	require.NoError(t, err)
	require.Len(t, resp.GetCandidates(), 1)
	assert.Equal(t, "m1", resp.GetCandidates()[0].GetModelId())
	assert.Equal(t, "qwen", resp.GetCandidates()[0].GetModelName())
	assert.Equal(t, int32(10), resp.GetCandidates()[0].GetWeight()) // default
	assert.Equal(t, int32(2), resp.GetCandidates()[0].GetCostTier())
	assert.Equal(t, int32(3), resp.GetCandidates()[0].GetLatencyTier())
}

// TestReplaceRulesValidation covers the rule validation (AC6, §5.4).
func TestReplaceRulesValidation(t *testing.T) {
	svc, catalog, _ := newRoutingSvc(t)
	ctx := context.Background()
	catalog.addModel("m1", "qwen", false, false)
	catalog.addModel("m2", "deepseek", false, false)
	id := createGroup(t, svc, "team-a", StrategyWeighted)
	setCandidates(t, svc, id, candidate("m1", 10, 0, 0, false), candidate("m2", 10, 0, 0, false))

	cases := []struct {
		name  string
		rules []*routingv1.RoutingGroupRuleInput
		code  apierrors.Code
	}{
		{"empty name", []*routingv1.RoutingGroupRuleInput{{Name: "", MatchType: MatchKeyword, Pattern: "code", TargetModelId: "m1"}}, apierrors.CodeRoutingGroupRuleInvalid},
		{"bad match type", []*routingv1.RoutingGroupRuleInput{{Name: "r1", MatchType: "glob", Pattern: "code", TargetModelId: "m1"}}, apierrors.CodeRoutingGroupRuleInvalid},
		{"empty pattern", []*routingv1.RoutingGroupRuleInput{{Name: "r1", MatchType: MatchKeyword, Pattern: "", TargetModelId: "m1"}}, apierrors.CodeRoutingGroupRuleInvalid},
		{"bad regex", []*routingv1.RoutingGroupRuleInput{{Name: "r1", MatchType: MatchRegex, Pattern: "([unclosed", TargetModelId: "m1"}}, apierrors.CodeRoutingGroupRuleInvalid},
		{"target not candidate", []*routingv1.RoutingGroupRuleInput{{Name: "r1", MatchType: MatchKeyword, Pattern: "code", TargetModelId: "mX"}}, apierrors.CodeRoutingGroupRuleInvalid},
		{"duplicate name", []*routingv1.RoutingGroupRuleInput{
			{Name: "r1", MatchType: MatchKeyword, Pattern: "code", TargetModelId: "m1"},
			{Name: "r1", MatchType: MatchKeyword, Pattern: "math", TargetModelId: "m2"},
		}, apierrors.CodeRoutingGroupRuleExists},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.ReplaceRoutingGroupRules(ctx, &routingv1.ReplaceRoutingGroupRulesRequest{
				GroupId: id, Rules: tc.rules,
			})
			require.Error(t, err)
			assert.Equal(t, tc.code, apierrors.CodeOf(err))
		})
	}

	// A valid replace persists.
	setRules(t, svc, id, &routingv1.RoutingGroupRuleInput{
		Name: "code", MatchType: MatchKeyword, Pattern: "code", TargetModelId: "m1", Enabled: true,
	})
	resp, err := svc.GetRoutingGroup(ctx, &routingv1.GetRoutingGroupRequest{GroupId: id})
	require.NoError(t, err)
	require.Len(t, resp.GetRules(), 1)
	assert.Equal(t, "code", resp.GetRules()[0].GetName())
}

// TestDeleteRoutingGroupInUse covers the in-use guard (AC4).
func TestDeleteRoutingGroupInUse(t *testing.T) {
	svc, _, _ := newRoutingSvc(t)
	ctx := context.Background()
	id := createGroup(t, svc, "team-a", StrategyWeighted)

	// No grants: deletable.
	_, err := svc.DeleteRoutingGroup(ctx, &routingv1.DeleteRoutingGroupRequest{GroupId: id})
	require.NoError(t, err)

	// With a grant: blocked (13111).
	id2 := createGroup(t, svc, "team-b", StrategyWeighted)
	require.NoError(t, svc.GrantGroupAccess(ctx, id2, "org-1", "admin-1"))
	_, err = svc.DeleteRoutingGroup(ctx, &routingv1.DeleteRoutingGroupRequest{GroupId: id2})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingGroupInUse, apierrors.CodeOf(err))
}

// TestResolveRoutingGroup covers the internal enforcement RPC (AD14):
// unknown → 13101, disabled → 13110, no eligible → 13109, and the happy
// path's ordered attempt list.
func TestResolveRoutingGroup(t *testing.T) {
	svc, catalog, _ := newRoutingSvc(t)
	ctx := context.Background()
	catalog.addModel("m1", "qwen", false, false)
	catalog.addModel("m2", "deepseek", false, false)
	catalog.addModel("m3", "llama", false, false)

	// Unknown group.
	_, err := svc.ResolveRoutingGroup(ctx, &routingv1.ResolveRoutingGroupRequest{GroupName: "missing"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingGroupNotFound, apierrors.CodeOf(err))

	// Disabled group.
	id := createGroup(t, svc, "team-a", StrategyCostPriority)
	setCandidates(t, svc, id, candidate("m1", 10, 2, 0, false), candidate("m2", 10, 1, 0, false))
	_, err = svc.ResolveRoutingGroup(ctx, &routingv1.ResolveRoutingGroupRequest{GroupName: "team-a"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingGroupDisabled, apierrors.CodeOf(err))

	// Enabled: cost_priority orders ascending cost tier.
	enableGroup(t, svc, id, StrategyCostPriority)
	res, err := svc.ResolveRoutingGroup(ctx, &routingv1.ResolveRoutingGroupRequest{GroupName: "team-a"})
	require.NoError(t, err)
	require.Len(t, res.GetAttemptModelIds(), 2)
	assert.Equal(t, "m2", res.GetAttemptModelIds()[0])
	assert.Equal(t, "m1", res.GetAttemptModelIds()[1])

	// No eligible candidate: an unpublished external model is filtered.
	id2 := createGroup(t, svc, "team-b", StrategyWeighted)
	catalog.addModel("ext-1", "gpt-x", true, false)
	setCandidates(t, svc, id2, candidate("ext-1", 10, 0, 0, false))
	enableGroup(t, svc, id2, StrategyWeighted)
	_, err = svc.ResolveRoutingGroup(ctx, &routingv1.ResolveRoutingGroupRequest{GroupName: "team-b"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingGroupNoEligibleCandidate, apierrors.CodeOf(err))

	// The dry run returns the skipped list instead.
	dry, err := svc.TestRoutingResolution(ctx, &routingv1.TestRoutingResolutionRequest{GroupId: id2, Prompt: "hello"})
	require.NoError(t, err)
	require.Len(t, dry.GetSkipped(), 1)
	assert.Equal(t, "ext-1", dry.GetSkipped()[0].GetModelId())
	assert.Equal(t, SkipReasonUnavailable, dry.GetSkipped()[0].GetReason())
}

// TestResolveRoutingGroupRulesAndStrategies covers the intent rules and
// the remaining strategies (§13.3).
func TestResolveRoutingGroupRulesAndStrategies(t *testing.T) {
	svc, catalog, _ := newRoutingSvc(t)
	ctx := context.Background()
	catalog.addModel("m1", "qwen", false, false)
	catalog.addModel("m2", "deepseek", false, false)
	catalog.addModel("m3", "llama", false, false)

	// Keyword rule: first match wins, target leads.
	id := createGroup(t, svc, "team-a", StrategyWeighted)
	setCandidates(t, svc, id, candidate("m1", 10, 0, 0, false), candidate("m2", 10, 0, 0, false))
	setRules(t, svc, id,
		&routingv1.RoutingGroupRuleInput{Name: "code", MatchType: MatchKeyword, Pattern: "code", TargetModelId: "m2", Enabled: true},
		&routingv1.RoutingGroupRuleInput{Name: "math", MatchType: MatchKeyword, Pattern: "math", TargetModelId: "m1", Enabled: true},
	)
	enableGroup(t, svc, id, StrategyWeighted)
	res, err := svc.ResolveRoutingGroup(ctx, &routingv1.ResolveRoutingGroupRequest{GroupName: "team-a", Prompt: "please write CODE for me"})
	require.NoError(t, err)
	assert.Equal(t, "m2", res.GetAttemptModelIds()[0])
	assert.Equal(t, "code", res.GetMatchedRuleName())

	// Regex rule.
	setRules(t, svc, id, &routingv1.RoutingGroupRuleInput{
		Name: "digits", MatchType: MatchRegex, Pattern: `^compute \d+$`, TargetModelId: "m1", Enabled: true,
	})
	res, err = svc.ResolveRoutingGroup(ctx, &routingv1.ResolveRoutingGroupRequest{GroupName: "team-a", Prompt: "compute 42"})
	require.NoError(t, err)
	assert.Equal(t, "m1", res.GetAttemptModelIds()[0])
	assert.Equal(t, "digits", res.GetMatchedRuleName())

	// A disabled rule never matches.
	setRules(t, svc, id, &routingv1.RoutingGroupRuleInput{
		Name: "off", MatchType: MatchKeyword, Pattern: "compute", TargetModelId: "m2", Enabled: false,
	})
	res, err = svc.ResolveRoutingGroup(ctx, &routingv1.ResolveRoutingGroupRequest{GroupName: "team-a", Prompt: "compute 42"})
	require.NoError(t, err)
	assert.Empty(t, res.GetMatchedRuleName())

	// Manual: the pinned candidate is the sole attempt.
	id2 := createGroup(t, svc, "team-b", StrategyManual)
	setCandidates(t, svc, id2, candidate("m1", 10, 0, 0, true), candidate("m2", 10, 0, 0, false))
	enableGroup(t, svc, id2, StrategyManual)
	res, err = svc.ResolveRoutingGroup(ctx, &routingv1.ResolveRoutingGroupRequest{GroupName: "team-b"})
	require.NoError(t, err)
	require.Len(t, res.GetAttemptModelIds(), 1)
	assert.Equal(t, "m1", res.GetAttemptModelIds()[0])

	// Latency priority: observed p50 ascending, unsampled by tier.
	id3 := createGroup(t, svc, "team-c", StrategyLatencyPriority)
	setCandidates(t, svc, id3, candidate("m1", 10, 0, 3, false), candidate("m2", 10, 0, 1, false), candidate("m3", 10, 0, 2, false))
	enableGroup(t, svc, id3, StrategyLatencyPriority)
	svc.SetLatencySource(&fakeLatency{p50: map[string]int64{"m1": 120, "m2": 80}})
	res, err = svc.ResolveRoutingGroup(ctx, &routingv1.ResolveRoutingGroupRequest{GroupName: "team-c"})
	require.NoError(t, err)
	require.Len(t, res.GetAttemptModelIds(), 3)
	assert.Equal(t, "m2", res.GetAttemptModelIds()[0]) // p50 80
	assert.Equal(t, "m1", res.GetAttemptModelIds()[1]) // p50 120
	assert.Equal(t, "m3", res.GetAttemptModelIds()[2]) // unsampled, tier 2

	// Attempt truncation at 3.
	id4 := createGroup(t, svc, "team-d", StrategyWeighted)
	catalog.addModel("m4", "mixtral", false, false)
	setCandidates(t, svc, id4, candidate("m1", 10, 0, 0, false), candidate("m2", 10, 0, 0, false), candidate("m3", 10, 0, 0, false), candidate("m4", 10, 0, 0, false))
	enableGroup(t, svc, id4, StrategyWeighted)
	res, err = svc.ResolveRoutingGroup(ctx, &routingv1.ResolveRoutingGroupRequest{GroupName: "team-d"})
	require.NoError(t, err)
	assert.Len(t, res.GetAttemptModelIds(), 3)
}

// TestResolveEligibilityOrgAndKeyScope covers the org-grant and
// key-scope filters (FR3.6).
func TestResolveEligibilityOrgAndKeyScope(t *testing.T) {
	svc, catalog, keyScope := newRoutingSvc(t)
	ctx := context.Background()
	catalog.addModel("m1", "qwen", false, false)
	catalog.addModel("m2", "deepseek", false, false)

	id := createGroup(t, svc, "team-a", StrategyWeighted)
	setCandidates(t, svc, id, candidate("m1", 10, 0, 0, false), candidate("m2", 10, 0, 0, false))
	enableGroup(t, svc, id, StrategyWeighted)

	// Default-allow: an org with no grants sees both.
	res, err := svc.ResolveRoutingGroup(ctx, &routingv1.ResolveRoutingGroupRequest{GroupName: "team-a", OrgId: "org-1"})
	require.NoError(t, err)
	assert.Len(t, res.GetAttemptModelIds(), 2)

	// A restricted model filters for an ungranted org.
	catalog.grants["m1"] = []string{"org-9"}
	res, err = svc.ResolveRoutingGroup(ctx, &routingv1.ResolveRoutingGroupRequest{GroupName: "team-a", OrgId: "org-1"})
	require.NoError(t, err)
	require.Len(t, res.GetAttemptModelIds(), 1)
	assert.Equal(t, "m2", res.GetAttemptModelIds()[0])
	require.Len(t, res.GetSkipped(), 1)
	assert.Equal(t, SkipReasonUnauthorized, res.GetSkipped()[0].GetReason())

	// A scoped key excludes an out-of-scope candidate (m1 stays
	// default-allow for org-2; the key scope narrows to m2).
	catalog.grants["m1"] = nil
	keyScope.scopes["key-1"] = []string{"m2"}
	res, err = svc.ResolveRoutingGroup(ctx, &routingv1.ResolveRoutingGroupRequest{GroupName: "team-a", OrgId: "org-2", ApiKeyId: "key-1"})
	require.NoError(t, err)
	require.Len(t, res.GetAttemptModelIds(), 1)
	assert.Equal(t, "m2", res.GetAttemptModelIds()[0])
}

// TestRecordDecisionAndListDecisions covers the decision trail (AC8).
func TestRecordDecisionAndListDecisions(t *testing.T) {
	svc, catalog, _ := newRoutingSvc(t)
	ctx := context.Background()
	catalog.addModel("m1", "qwen", false, false)
	catalog.addModel("m2", "deepseek", false, false)

	id := createGroup(t, svc, "team-a", StrategyWeighted)
	setCandidates(t, svc, id, candidate("m1", 10, 0, 0, false), candidate("m2", 10, 0, 0, false))
	enableGroup(t, svc, id, StrategyWeighted)

	res, err := svc.ResolveRoutingGroup(ctx, &routingv1.ResolveRoutingGroupRequest{GroupName: "team-a"})
	require.NoError(t, err)
	require.NoError(t, svc.RecordDecision(ctx, res, "req-1", "team-a", "m1", "qwen", map[string]string{"m1": OutcomeServed, "m2": OutcomeNotAttempted}))

	list, err := svc.ListRoutingDecisions(ctx, &routingv1.ListRoutingDecisionsRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetDecisions(), 1)
	d := list.GetDecisions()[0]
	assert.Equal(t, "req-1", d.GetRequestId())
	assert.Equal(t, "team-a", d.GetGroupName())
	assert.Equal(t, "m1", d.GetSelectedModelId())
	require.Len(t, d.GetCandidatesConsidered(), 2)
	outcomes := map[string]string{}
	for _, c := range d.GetCandidatesConsidered() {
		outcomes[c.GetModelId()] = c.GetOutcome()
	}
	assert.Equal(t, OutcomeServed, outcomes["m1"])
	assert.Equal(t, OutcomeNotAttempted, outcomes["m2"])

	// Filters narrow the log.
	list, err = svc.ListRoutingDecisions(ctx, &routingv1.ListRoutingDecisionsRequest{SelectedModel: "deepseek"})
	require.NoError(t, err)
	assert.Empty(t, list.GetDecisions())
	list, err = svc.ListRoutingDecisions(ctx, &routingv1.ListRoutingDecisionsRequest{Outcome: "served"})
	require.NoError(t, err)
	assert.Len(t, list.GetDecisions(), 1)

	// GetRoutingDecision returns the detail.
	detail, err := svc.GetRoutingDecision(ctx, &routingv1.GetRoutingDecisionRequest{DecisionId: d.GetDecisionId()})
	require.NoError(t, err)
	assert.Equal(t, "req-1", detail.GetDecision().GetRequestId())

	// The 24h counter feeds the list projection.
	groups, err := svc.ListRoutingGroups(ctx, &routingv1.ListRoutingGroupsRequest{})
	require.NoError(t, err)
	require.Len(t, groups.GetGroups(), 1)
	assert.Equal(t, int64(1), groups.GetGroups()[0].GetRequestCount_24H()) //nolint:staticcheck // generated field name.
}

// TestGroupGrantSeams covers the model.GroupAuthorization and
// VirtualModelLister seam implementations (AD12).
func TestGroupGrantSeams(t *testing.T) {
	svc, _, _ := newRoutingSvc(t)
	ctx := context.Background()
	id := createGroup(t, svc, "team-a", StrategyWeighted)
	id2 := createGroup(t, svc, "team-b", StrategyWeighted)

	// GroupExists: a non-uuid is false; a uuid miss is false; a hit is
	// true.
	exists, err := svc.GroupExists(ctx, "not-a-uuid")
	require.NoError(t, err)
	assert.False(t, exists)
	exists, err = svc.GroupExists(ctx, "00000000-0000-0000-0000-000000000000")
	require.NoError(t, err)
	assert.False(t, exists)
	exists, err = svc.GroupExists(ctx, id)
	require.NoError(t, err)
	assert.True(t, exists)

	// Grants: default-allow lists all groups; a grant on one group keeps
	// it visible while an ungranted restricted group drops out. Here
	// team-b stays visible (zero grants = default-allow), so grant both
	// to demonstrate the narrowing.
	entries, err := svc.ListGrantedGroups(ctx, "org-1")
	require.NoError(t, err)
	assert.Len(t, entries, 2)
	require.NoError(t, svc.GrantGroupAccess(ctx, id, "org-1", "admin-1"))
	require.NoError(t, svc.GrantGroupAccess(ctx, id2, "org-2", "admin-1"))
	entries, err = svc.ListGrantedGroups(ctx, "org-1")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, id, entries[0].ID)
	assert.Equal(t, "team-a", entries[0].Name)
	// org-2 sees its granted group plus the default-allow one? No:
	// team-a is restricted (has grants), so org-2 sees only team-b.
	entries, err = svc.ListGrantedGroups(ctx, "org-2")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, id2, entries[0].ID)

	// ListGroupAuthorizations returns the grant rows.
	rows, total, err := svc.ListGroupAuthorizations(ctx, id, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	assert.Equal(t, "org-1", rows[0].OrganizationID)

	// CountGroupGrants.
	count, err := svc.CountGroupGrants(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// RevokeGroupAccess removes the grant.
	require.NoError(t, svc.RevokeGroupAccess(ctx, id, "org-1"))
	count, err = svc.CountGroupGrants(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
	_ = id2
}

// TestDecisionRetentionRunner covers the retention runner (AD12).
func TestDecisionRetentionRunner(t *testing.T) {
	svc, _, _ := newRoutingSvc(t)
	ctx := context.Background()
	id := createGroup(t, svc, "team-a", StrategyWeighted)

	// Insert an old decision directly.
	decisions, err := svc.decisions()
	require.NoError(t, err)
	old := time.Now().UTC().Add(-48 * time.Hour)
	require.NoError(t, decisions.Insert(ctx, &Decision{
		RequestID: "req-old", GroupID: id, GroupName: "team-a", Strategy: StrategyWeighted,
		SelectedModelID: "m1", SelectedModelName: "qwen",
		CreatedAt:  old,
		Candidates: DecisionCandidates{{ModelID: "m1", ModelName: "qwen", Outcome: OutcomeServed}},
	}))
	require.NoError(t, decisions.Insert(ctx, &Decision{
		RequestID: "req-new", GroupID: id, GroupName: "team-a", Strategy: StrategyWeighted,
		SelectedModelID: "m1", SelectedModelName: "qwen",
		Candidates: DecisionCandidates{{ModelID: "m1", ModelName: "qwen", Outcome: OutcomeServed}},
	}))

	// A one-day retention window prunes the 48h-old entry.
	decisionRepo, err := svc.decisions()
	require.NoError(t, err)
	runner := NewDecisionRetentionRunner(decisionRepo, 1, 100, time.Hour)
	runner.RetainOnce(ctx)

	list, err := svc.ListRoutingDecisions(ctx, &routingv1.ListRoutingDecisionsRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetDecisions(), 1)
	assert.Equal(t, "req-new", list.GetDecisions()[0].GetRequestId())
}

// TestListRoutingGroupsFilters covers the list filters (AD15).
func TestListRoutingGroupsFilters(t *testing.T) {
	svc, _, _ := newRoutingSvc(t)
	ctx := context.Background()
	createGroup(t, svc, "team-a", StrategyWeighted)
	createGroup(t, svc, "team-b", StrategyCostPriority)

	// Name filter.
	list, err := svc.ListRoutingGroups(ctx, &routingv1.ListRoutingGroupsRequest{Name: "team-a"})
	require.NoError(t, err)
	require.Len(t, list.GetGroups(), 1)
	assert.Equal(t, "team-a", list.GetGroups()[0].GetName())

	// Strategy filter.
	list, err = svc.ListRoutingGroups(ctx, &routingv1.ListRoutingGroupsRequest{Strategy: StrategyCostPriority})
	require.NoError(t, err)
	require.Len(t, list.GetGroups(), 1)
	assert.Equal(t, "team-b", list.GetGroups()[0].GetName())

	// Enabled filter: both groups are disabled.
	list, err = svc.ListRoutingGroups(ctx, &routingv1.ListRoutingGroupsRequest{Enabled: "false"})
	require.NoError(t, err)
	assert.Len(t, list.GetGroups(), 2)
	list, err = svc.ListRoutingGroups(ctx, &routingv1.ListRoutingGroupsRequest{Enabled: "true"})
	require.NoError(t, err)
	assert.Empty(t, list.GetGroups())
}

// TestGroupNameReservedByCatalog covers the reserved-name rule (13113)
// through the seam.
func TestGroupNameReservedByCatalog(t *testing.T) {
	svc, catalog, _ := newRoutingSvc(t)
	catalog.addModel("m1", "qwen", false, false)
	_, err := svc.CreateRoutingGroup(context.Background(), &routingv1.CreateRoutingGroupRequest{Name: "qwen"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingGroupNameReserved, apierrors.CodeOf(err))
}

// TestRuleMatches covers the keyword/regex matchers.
func TestRuleMatches(t *testing.T) {
	keyword := &Rule{MatchType: MatchKeyword, Pattern: "Code"}
	assert.True(t, ruleMatches(keyword, "write CODE please"))
	assert.False(t, ruleMatches(keyword, "write math please"))

	re := &Rule{MatchType: MatchRegex, Pattern: `^compute \d+$`}
	assert.True(t, ruleMatches(re, "compute 42"))
	assert.False(t, ruleMatches(re, "compute 42 now"))

	// An uncompilable pattern never matches (fail safe).
	bad := &Rule{MatchType: MatchRegex, Pattern: "([unclosed"}
	assert.False(t, ruleMatches(bad, "anything"))
}

// TestNearestRankP50 covers the nearest-rank percentile helper.
func TestNearestRankP50(t *testing.T) {
	assert.Equal(t, int64(0), nearestRankP50(nil))
	assert.Equal(t, int64(10), nearestRankP50([]int64{10}))
	assert.Equal(t, int64(10), nearestRankP50([]int64{10, 20}))         // rank ceil(0.5*2)=1
	assert.Equal(t, int64(20), nearestRankP50([]int64{10, 20, 30}))     // rank ceil(0.5*3)=2
	assert.Equal(t, int64(20), nearestRankP50([]int64{40, 10, 30, 20})) // rank ceil(0.5*4)=2
}

// TestNormalizePage covers the pagination bounds.
func TestNormalizePage(t *testing.T) {
	offset, limit := normalizePage(nil)
	assert.Equal(t, 0, offset)
	assert.Equal(t, 20, limit)
	offset, limit = normalizePage(&commonv1.PageRequest{Offset: -5, Limit: 500})
	assert.Equal(t, 0, offset)
	assert.Equal(t, 100, limit)
}

// TestValidateGroupName covers the name pattern directly.
func TestValidateGroupName(t *testing.T) {
	for _, name := range []string{"a", "ab", "A-b", "1ab", "-ab", strings.Repeat("a", 65)} {
		assert.Error(t, validateGroupName(name), name)
	}
	for _, name := range []string{"abc", "a-b", "a1b", strings.Repeat("a", 64)} {
		assert.NoError(t, validateGroupName(name), name)
	}
}
