package routing

import (
	"context"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"time"

	routingv1 "github.com/go-taas/go-taas/proto/taas/routing/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// CatalogModel is the routing module's read-only projection of a catalog
// model (the ModelCatalog seam's row).
type CatalogModel struct {
	ID        string
	Name      string
	External  bool
	Published bool
}

// ModelCatalog is the seam to the model module: the availability and
// org-grant checks of the eligibility filter, and the reserved-name and
// candidate-existence checks of the registry (the SetModelCatalog
// pattern).
type ModelCatalog interface {
	// GetCatalogModel returns the catalog model by id, or an error with
	// code 10101 when it does not exist.
	GetCatalogModel(ctx context.Context, modelID string) (*CatalogModel, error)
	// CatalogModelNameExists reports whether a catalog model with the
	// given name exists (the reserved-name check, 13113).
	CatalogModelNameExists(ctx context.Context, name string) (bool, error)
	// IsModelAuthorized implements the #13 default-allow org-grant check.
	IsModelAuthorized(ctx context.Context, modelID, organizationID string) (bool, error)
}

// KeyScopeLister is the seam to the auth module: the key-scope check of
// the eligibility filter (AD13). Implemented by auth.Service.
type KeyScopeLister interface {
	// APIKeyModels returns the key's model-scope list (empty = unscoped).
	APIKeyModels(ctx context.Context, apiKeyID string) ([]string, error)
}

// LatencySource is the observed-latency seam of the latency_priority
// strategy (AD3). Implemented by DecisionRepository against
// request_logs (read-only to routing).
type LatencySource interface {
	// ObservedP50 returns the nearest-rank p50 latency in milliseconds
	// of each model's successful request logs since the cutoff. Models
	// without samples are absent from the map.
	ObservedP50(ctx context.Context, modelIDs []string, since time.Time) (map[string]int64, error)
}

// Resolution is the resolver's plan: the ordered attempt list, the
// skipped candidates with reasons, the matched rule and the strategy.
type Resolution struct {
	// Attempts is the ordered attempt list (<= 3; manual = exactly 1).
	Attempts []*Candidate
	// Skipped carries every candidate filtered by the eligibility check
	// with its reason.
	Skipped []*routingv1.SkippedCandidate
	// MatchedRuleID / MatchedRuleName identify the first-matching intent
	// rule (empty when none matched).
	MatchedRuleID   string
	MatchedRuleName string
	// Strategy is the group's strategy (echoed for the caller).
	Strategy string
}

// resolver holds the resolver's collaborators. The rng is seeded once
// and reused (the weighted draw is sampling, not security).
type resolver struct {
	catalog      ModelCatalog
	keyScope     KeyScopeLister
	latency      LatencySource
	latencyWin   time.Duration
	groupRepo    *Repository
	decisionRepo *DecisionRepository

	mu  sync.Mutex
	rng *rand.Rand // #nosec G404 -- sampling only, not security
}

// newResolver constructs a resolver.
func newResolver(groupRepo *Repository, decisionRepo *DecisionRepository) *resolver {
	return &resolver{
		groupRepo:    groupRepo,
		decisionRepo: decisionRepo,
		// #nosec G404 -- the weighted draw is sampling, not security.
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Resolve runs the binding algorithm (§13.3): load → eligibility filter
// → intent rules → strategy order → assemble. dryRun skips the
// fail-closed error (the panel renders the skipped list instead).
func (r *resolver) Resolve(ctx context.Context, group *Group, prompt, orgID, apiKeyID string, dryRun bool) (*Resolution, error) {
	// 1. Load the group's candidates and enabled rules in declared order.
	candidates, err := r.groupRepo.ListCandidates(ctx, group.ID)
	if err != nil {
		return nil, err
	}
	rules, err := r.groupRepo.ListRules(ctx, group.ID)
	if err != nil {
		return nil, err
	}
	enabledRules := make([]*Rule, 0, len(rules))
	for _, rule := range rules {
		if rule.Enabled {
			enabledRules = append(enabledRules, rule)
		}
	}

	// 2. Eligibility filter (FR3.6): availability, org grant, key scope.
	var keyScope []string
	scoped := false
	if r.keyScope != nil && apiKeyID != "" {
		keyScope, err = r.keyScope.APIKeyModels(ctx, apiKeyID)
		if err != nil {
			return nil, err
		}
		scoped = len(keyScope) > 0
	}
	eligible := make([]*Candidate, 0, len(candidates))
	skipped := make([]*routingv1.SkippedCandidate, 0)
	for _, c := range candidates {
		reason, detail, ok := r.eligibility(ctx, c, orgID, keyScope, scoped)
		if ok {
			eligible = append(eligible, c)
			continue
		}
		skipped = append(skipped, &routingv1.SkippedCandidate{
			ModelId:   c.ModelID,
			ModelName: c.ModelName,
			Reason:    reason,
		})
		_ = detail // detail is folded into the decision entry, not the skip
	}

	// 3. No eligible candidate → fail closed 13109 (dry run returns the
	// skipped list with an empty attempt list and no error).
	if len(eligible) == 0 {
		if dryRun {
			return &Resolution{Skipped: skipped, Strategy: group.Strategy}, nil
		}
		return nil, apierrors.New(apierrors.CodeRoutingGroupNoEligibleCandidate)
	}

	// 4. Intent rules (FR4.3-FR4.4): first match wins; the target leads
	// only when it is eligible.
	res := &Resolution{Skipped: skipped, Strategy: group.Strategy}
	var target *Candidate
	for _, rule := range enabledRules {
		if !ruleMatches(rule, prompt) {
			continue
		}
		res.MatchedRuleID = rule.ID
		res.MatchedRuleName = rule.Name
		for _, c := range eligible {
			if c.ModelID == rule.TargetModelID {
				target = c
				break
			}
		}
		break
	}

	// 5. Strategy order (AD3).
	ordered, err := r.strategyOrder(ctx, group, eligible)
	if err != nil {
		return nil, err
	}

	// 6. Assemble: rule target first, then the strategy order minus the
	// target, truncated to 3 (FR3.5). Manual yields exactly one attempt.
	attempts := make([]*Candidate, 0, maxAttempts)
	if target != nil {
		attempts = append(attempts, target)
	}
	for _, c := range ordered {
		if len(attempts) >= maxAttempts {
			break
		}
		if target != nil && c.ModelID == target.ModelID {
			continue
		}
		attempts = append(attempts, c)
	}
	if group.Strategy == StrategyManual {
		// The pinned candidate is the sole attempt by construction; if
		// it was filtered, step 3 already failed closed.
		attempts = attempts[:1]
	}
	res.Attempts = attempts
	return res, nil
}

// eligibility evaluates one candidate's eligibility (FR3.6) and returns
// the skip reason and detail when filtered.
func (r *resolver) eligibility(ctx context.Context, c *Candidate, orgID string, keyScope []string, scoped bool) (reason, detail string, ok bool) {
	// (a) Availability: the catalog model must exist; an external model
	// must be published. A self-hosted model is available if it exists
	// (the gateway's health routing and #45 handle serving readiness).
	if r.catalog != nil {
		m, err := r.catalog.GetCatalogModel(ctx, c.ModelID)
		if err != nil || m == nil {
			return SkipReasonUnavailable, "model unavailable", false
		}
		if m.External && !m.Published {
			return SkipReasonUnavailable, "model unavailable", false
		}
		// (b) Org grant: the #13 default-allow rule.
		if orgID != "" {
			allowed, err := r.catalog.IsModelAuthorized(ctx, c.ModelID, orgID)
			if err == nil && !allowed {
				return SkipReasonUnauthorized, "org not granted", false
			}
		}
	}
	// (c) Key scope: a scoped key whose list excludes the candidate.
	if scoped {
		found := false
		for _, m := range keyScope {
			if m == c.ModelID {
				found = true
				break
			}
		}
		if !found {
			return SkipReasonUnauthorized, "not in key scope", false
		}
	}
	return "", "", true
}

// ruleMatches evaluates one intent rule against the prompt: keyword →
// case-insensitive substring; regex → precompiled RE2 MatchString.
func ruleMatches(rule *Rule, prompt string) bool {
	if rule.MatchType == MatchRegex {
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			// Validated at save time; an uncompilable pattern never
			// matches (fail safe).
			return false
		}
		return re.MatchString(prompt)
	}
	return strings.Contains(strings.ToLower(prompt), strings.ToLower(rule.Pattern))
}

// strategyOrder orders the eligible candidates by the group's strategy
// (AD3).
func (r *resolver) strategyOrder(ctx context.Context, group *Group, eligible []*Candidate) ([]*Candidate, error) {
	ordered := make([]*Candidate, len(eligible))
	copy(ordered, eligible)
	switch group.Strategy {
	case StrategyManual:
		// The pinned candidate only (the sole eligible attempt by
		// construction).
		for _, c := range ordered {
			if c.Pinned {
				return []*Candidate{c}, nil
			}
		}
		return ordered[:1], nil
	case StrategyWeighted:
		// One random draw proportional to weight, then the rest in
		// descending weight order (ties → declared order).
		drawn := r.weightedDraw(ordered)
		rest := make([]*Candidate, 0, len(ordered)-1)
		for _, c := range ordered {
			if c.ModelID != drawn.ModelID {
				rest = append(rest, c)
			}
		}
		stableSortByWeightDesc(rest)
		return append([]*Candidate{drawn}, rest...), nil
	case StrategyCostPriority:
		// Ascending cost_tier, missing = 6 (ties → declared order).
		stableSortByTier(ordered, func(c *Candidate) int {
			if c.CostTier >= minTier && c.CostTier <= maxTier {
				return c.CostTier
			}
			return missingTier
		})
		return ordered, nil
	case StrategyLatencyPriority:
		// Candidates with observed samples first, ordered by observed
		// p50 ascending; then candidates without samples ordered by
		// declared latency_tier ascending (missing = 6).
		return r.latencyOrder(ctx, ordered)
	}
	return ordered, nil
}

// weightedDraw draws one candidate with probability proportional to
// weight (a single rand.Intn(totalWeight) walk).
func (r *resolver) weightedDraw(candidates []*Candidate) *Candidate {
	total := 0
	for _, c := range candidates {
		w := c.Weight
		if w < minWeight {
			w = minWeight
		}
		total += w
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pick := r.rng.Intn(total)
	for _, c := range candidates {
		w := c.Weight
		if w < minWeight {
			w = minWeight
		}
		pick -= w
		if pick < 0 {
			return c
		}
	}
	return candidates[len(candidates)-1]
}

// stableSortByWeightDesc sorts by descending weight, stable (ties keep
// the declared order).
func stableSortByWeightDesc(rows []*Candidate) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			if rows[j].Weight > rows[j-1].Weight {
				rows[j], rows[j-1] = rows[j-1], rows[j]
				continue
			}
			break
		}
	}
}

// stableSortByTier sorts by the extracted tier key ascending, stable.
func stableSortByTier(rows []*Candidate, tier func(*Candidate) int) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			if tier(rows[j]) < tier(rows[j-1]) {
				rows[j], rows[j-1] = rows[j-1], rows[j]
				continue
			}
			break
		}
	}
}

// latencyOrder implements the latency_priority ordering (AD3): sampled
// candidates by observed p50 ascending first, then unsampled candidates
// by declared latency_tier ascending (missing = 6, ties → declared
// order).
func (r *resolver) latencyOrder(ctx context.Context, eligible []*Candidate) ([]*Candidate, error) {
	if r.latency == nil {
		// No latency source: fall back to the declared tier order only.
		ordered := make([]*Candidate, len(eligible))
		copy(ordered, eligible)
		stableSortByTier(ordered, func(c *Candidate) int {
			if c.LatencyTier >= minTier && c.LatencyTier <= maxTier {
				return c.LatencyTier
			}
			return missingTier
		})
		return ordered, nil
	}
	ids := make([]string, 0, len(eligible))
	for _, c := range eligible {
		ids = append(ids, c.ModelID)
	}
	p50, err := r.latency.ObservedP50(ctx, ids, time.Now().UTC().Add(-r.latencyWin))
	if err != nil {
		return nil, err
	}
	sampled := make([]*Candidate, 0, len(eligible))
	unsampled := make([]*Candidate, 0, len(eligible))
	for _, c := range eligible {
		if _, ok := p50[c.ModelID]; ok {
			sampled = append(sampled, c)
		} else {
			unsampled = append(unsampled, c)
		}
	}
	// Sampled: ascending observed p50 (ties → declared order).
	for i := 1; i < len(sampled); i++ {
		for j := i; j > 0; j-- {
			if p50[sampled[j].ModelID] < p50[sampled[j-1].ModelID] {
				sampled[j], sampled[j-1] = sampled[j-1], sampled[j]
				continue
			}
			break
		}
	}
	// Unsampled: ascending declared latency_tier (missing = 6).
	stableSortByTier(unsampled, func(c *Candidate) int {
		if c.LatencyTier >= minTier && c.LatencyTier <= maxTier {
			return c.LatencyTier
		}
		return missingTier
	})
	return append(sampled, unsampled...), nil
}
