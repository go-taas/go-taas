package optimization

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/services/batch"
)

// logRuleError logs a rule-level failure at warn: the engine skips
// only that rule and keeps the rest of the snapshot (AD6).
func logRuleError(rule string, err error) {
	logger.S().Warnw("optimization: rule evaluation failed, skipping rule", "rule", rule, "err", err)
}

// ComputedRecommendation is one rule's output before persistence: the
// row fields plus the R1 alternatives payload. The engine fingerprints
// and upserts these.
type ComputedRecommendation struct {
	Rule                   string
	TargetType             string
	TargetID               string
	TargetDisplay          string
	OrganizationID         string
	ModelID                string
	APIKeyID               string
	ServiceID              string
	Suggestion             string
	EstimatedMonthlySaving float64
	Inputs                 map[string]any
	Arithmetic             string
	Alternatives           []AlternativeEntry
	ActingPageAdmin        ActingLink
	ActingPageUser         ActingLink
}

// AlternativeEntry is one R1 runner-up candidate (the drawer's
// side-by-side list).
type AlternativeEntry struct {
	ModelID                string  `json:"model_id"`
	ModelName              string  `json:"model_name"`
	EstimatedMonthlySaving float64 `json:"estimated_monthly_saving"`
	InputPricePerMillion   float64 `json:"input_price_per_million"`
	OutputPricePerMillion  float64 `json:"output_price_per_million"`
}

// ActingLink is the one-click acting page {route, label} (AD16).
type ActingLink struct {
	Route string `json:"route"`
	Label string `json:"label"`
}

// ruleContext bundles the run's fixed inputs: the window, the config
// and the read repositories (AD10).
type ruleContext struct {
	windowStart time.Time
	windowEnd   time.Time
	cfg         config.OptimizationConfig
	charges     chargeReader
	requests    requestLogReader
	services    serviceReader
	catalog     catalogReader
	cardTypes   CardTypeLister
}

// runRules executes R1-R4 and merges their outputs. A rule-level error
// skips only that rule (AD6: a partial snapshot is better than none).
func runRules(ctx context.Context, rc *ruleContext) []ComputedRecommendation {
	var out []ComputedRecommendation
	if entries, err := ruleSubstitution(ctx, rc); err == nil {
		out = append(out, entries...)
	} else {
		logRuleError(RuleSubstitution, err)
	}
	if entries, err := ruleBatch(ctx, rc); err == nil {
		out = append(out, entries...)
	} else {
		logRuleError(RuleBatch, err)
	}
	if entries, err := ruleIdleService(ctx, rc); err == nil {
		out = append(out, entries...)
	} else {
		logRuleError(RuleIdleService, err)
	}
	if entries, err := ruleCache(ctx, rc); err == nil {
		out = append(out, entries...)
	} else {
		logRuleError(RuleCache, err)
	}
	return out
}

// orgModelUsage is the per-(org, model) rollup of the charge groups.
type orgModelUsage struct {
	OrganizationID   string
	ModelID          string
	Requests         int64
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	Spend            float64
	// byCard keeps the per-card groups for the per-card arithmetic.
	byCard map[string]ChargeGroup
}

// rollupByOrgModel folds the charge groups into per-(org, model)
// usage rows.
func rollupByOrgModel(groups []ChargeGroup) map[string]*orgModelUsage {
	out := map[string]*orgModelUsage{}
	for _, g := range groups {
		key := g.OrganizationID + "\x00" + g.ModelID
		u, ok := out[key]
		if !ok {
			u = &orgModelUsage{
				OrganizationID: g.OrganizationID,
				ModelID:        g.ModelID,
				byCard:         map[string]ChargeGroup{},
			}
			out[key] = u
		}
		u.Requests += g.Requests
		u.PromptTokens += g.PromptTokens
		u.CompletionTokens += g.CompletionTokens
		u.CachedTokens += g.CachedTokens
		u.Spend += g.Spend
		u.byCard[g.Card] = g
	}
	return out
}

// contextLengthOf resolves a model's configured context length
// case-insensitively (viper lowercases config map keys while catalog
// model names keep their case, AD12). 0 = unknown.
func contextLengthOf(lengths map[string]int64, name string) int64 {
	if v, ok := lengths[name]; ok && v > 0 {
		return v
	}
	for k, v := range lengths {
		if strings.EqualFold(k, name) && v > 0 {
			return v
		}
	}
	return 0
}

// cardHourlyCostOf resolves a card type's configured hourly cost
// case-insensitively (viper lowercases config map keys while card
// types keep their case, AD14). ok = priced.
func cardHourlyCostOf(costs map[string]float64, cardType string) (float64, bool) {
	if v, ok := costs[cardType]; ok {
		return v, true
	}
	for k, v := range costs {
		if strings.EqualFold(k, cardType) {
			return v, true
		}
	}
	return 0, false
}

// ruleSubstitution is R1 — cheaper-model substitution (per org, per
// model A; architecture 13.3).
func ruleSubstitution(ctx context.Context, rc *ruleContext) ([]ComputedRecommendation, error) {
	groups, err := rc.charges.ChargeGroups(ctx, rc.windowStart)
	if err != nil {
		return nil, err
	}
	models, err := rc.catalog.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	grantCounts, err := rc.catalog.GrantCounts(ctx)
	if err != nil {
		return nil, err
	}
	orgGrants, err := rc.catalog.OrgGrants(ctx)
	if err != nil {
		return nil, err
	}
	orgNames, err := rc.catalog.OrgNames(ctx)
	if err != nil {
		return nil, err
	}
	modelByID := map[string]ModelRow{}
	for _, m := range models {
		modelByID[m.ID] = m
	}

	usage := rollupByOrgModel(groups)
	var out []ComputedRecommendation
	for _, u := range usage {
		modelA, ok := modelByID[u.ModelID]
		if !ok {
			continue
		}
		// Eligibility (FR1.2): enough requests and enough billed
		// tokens over the window.
		if u.Requests < int64(rc.cfg.R1.MinRequests) {
			continue
		}
		if u.PromptTokens+u.CompletionTokens < rc.cfg.R1.MinBilledTokens {
			continue
		}
		// Context gate (AD12): A's context length must be configured;
		// unknown -> fail closed, skip.
		lengthA := contextLengthOf(rc.cfg.ModelContextLengths, modelA.Name)
		if lengthA <= 0 {
			continue
		}
		// Candidates B: every other catalog model the org may use
		// (public default-allow or granted) with a known context
		// length >= contextRatio x A's.
		type candidate struct {
			model   ModelRow
			length  int64
			granted bool
		}
		var candidates []candidate
		for _, m := range models {
			if m.ID == modelA.ID {
				continue
			}
			length := contextLengthOf(rc.cfg.ModelContextLengths, m.Name)
			if length <= 0 {
				continue
			}
			if float64(length) < rc.cfg.R1.ContextRatio*float64(lengthA) {
				continue
			}
			granted := false
			if grantCounts[m.ID] == 0 {
				granted = true // public, default-allow
			} else if orgGrants[m.ID] != nil && orgGrants[m.ID][u.OrganizationID] {
				granted = true
			}
			if !granted {
				continue
			}
			candidates = append(candidates, candidate{model: m, length: length, granted: grantCounts[m.ID] > 0 && orgGrants[m.ID] != nil && orgGrants[m.ID][u.OrganizationID]})
		}
		if len(candidates) == 0 {
			continue
		}
		// Per candidate: the honest per-card arithmetic (negative
		// groups offset).
		type scored struct {
			cand       candidate
			total      float64
			perCard    []map[string]any
			bestInput  float64
			bestOutput float64
		}
		var scoredCandidates []scored
		for _, cand := range candidates {
			var total float64
			var perCard []map[string]any
			var bestInput, bestOutput float64
			for card, g := range u.byCard {
				ratesB, err := applicableRates(ctx, rc, cand.model.ID, card)
				if err != nil {
					return nil, err
				}
				if ratesB == nil {
					continue // unpriced group
				}
				spendA := g.Spend
				spendB := float64(g.PromptTokens)/1e6*ratesB.Input + float64(g.CompletionTokens)/1e6*ratesB.Output
				saving := spendA - spendB
				total += saving
				bestInput, bestOutput = ratesB.Input, ratesB.Output
				perCard = append(perCard, map[string]any{
					"card":                               card,
					"requests":                           g.Requests,
					"prompt_tokens":                      g.PromptTokens,
					"completion_tokens":                  g.CompletionTokens,
					"spend":                              round2(spendA),
					"candidate_spend":                    round2(spendB),
					"candidate_input_price_per_million":  ratesB.Input,
					"candidate_output_price_per_million": ratesB.Output,
					"saving":                             round2(saving),
				})
			}
			if total >= rc.cfg.R1.MinSaving {
				scoredCandidates = append(scoredCandidates, scored{cand: cand, total: total, perCard: perCard, bestInput: bestInput, bestOutput: bestOutput})
			}
		}
		if len(scoredCandidates) == 0 {
			continue
		}
		// Best B = max total; alternatives = the rest, saving DESC.
		sort.Slice(scoredCandidates, func(i, j int) bool {
			if scoredCandidates[i].total != scoredCandidates[j].total {
				return scoredCandidates[i].total > scoredCandidates[j].total
			}
			return scoredCandidates[i].cand.model.Name < scoredCandidates[j].cand.model.Name
		})
		best := scoredCandidates[0]
		orgDisplay := orgNames[u.OrganizationID]
		if orgDisplay == "" {
			orgDisplay = u.OrganizationID
		}
		suggestion := fmt.Sprintf("Switch %s's traffic on %s to %s", orgDisplay, modelA.Name, best.cand.model.Name)
		var alternatives []AlternativeEntry
		for _, sc := range scoredCandidates[1:] {
			alternatives = append(alternatives, AlternativeEntry{
				ModelID:                sc.cand.model.ID,
				ModelName:              sc.cand.model.Name,
				EstimatedMonthlySaving: round2(sc.total),
				InputPricePerMillion:   sc.bestInput,
				OutputPricePerMillion:  sc.bestOutput,
			})
		}
		inputs := map[string]any{
			"window_days":            rc.cfg.WindowDays,
			"requests":               u.Requests,
			"prompt_tokens":          u.PromptTokens,
			"completion_tokens":      u.CompletionTokens,
			"model_a":                modelA.Name,
			"model_a_context_length": lengthA,
			"model_b":                best.cand.model.Name,
			"model_b_context_length": best.cand.length,
			"model_b_grant":          map[bool]string{true: "granted", false: "public"}[best.cand.granted],
			"per_card":               best.perCard,
		}
		arithmetic := fmt.Sprintf("(%s x %dM prompt + %s x %dM completion) - (%s x %dM + %s x %dM) = %s %s/month",
			formatPrice(rc.cfg.Currency), u.PromptTokens/1e6,
			formatPrice(rc.cfg.Currency), u.CompletionTokens/1e6,
			formatPrice(rc.cfg.Currency), u.PromptTokens/1e6,
			formatPrice(rc.cfg.Currency), u.CompletionTokens/1e6,
			rc.cfg.Currency, formatAmount(best.total))
		out = append(out, ComputedRecommendation{
			Rule:                   RuleSubstitution,
			TargetType:             TargetOrgModel,
			TargetID:               u.OrganizationID + "/" + u.ModelID,
			TargetDisplay:          orgDisplay + " / " + modelA.Name,
			OrganizationID:         u.OrganizationID,
			ModelID:                u.ModelID,
			Suggestion:             suggestion,
			EstimatedMonthlySaving: round2(best.total),
			Inputs:                 inputs,
			Arithmetic:             arithmetic,
			Alternatives:           alternatives,
			ActingPageAdmin:        ActingLink{Route: "/admin/models/" + best.cand.model.ID + "/authorizations", Label: "Configure grants"},
			ActingPageUser:         ActingLink{Route: "/models", Label: "View model catalog"},
		})
	}
	return out, nil
}

// applicableRates resolves B's rates for a card, falling back to the
// "default" cell (D6); nil when both miss.
func applicableRates(ctx context.Context, rc *ruleContext, modelID, card string) (*PriceRates, error) {
	rates, err := rc.charges.ApplicablePrice(ctx, modelID, card, rc.windowEnd.Unix())
	if err != nil {
		return nil, err
	}
	if rates != nil {
		return rates, nil
	}
	return rc.charges.ApplicablePrice(ctx, modelID, defaultCardType, rc.windowEnd.Unix())
}

// ruleBatch is R2 — batch migration (per org, per key, per model;
// architecture 13.3).
func ruleBatch(ctx context.Context, rc *ruleContext) ([]ComputedRecommendation, error) {
	groups, err := rc.charges.ChargeGroups(ctx, rc.windowStart)
	if err != nil {
		return nil, err
	}
	models, err := rc.catalog.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	modelByID := map[string]ModelRow{}
	for _, m := range models {
		modelByID[m.ID] = m
	}
	logs, err := rc.requests.KeyModelLogs(ctx, rc.windowStart)
	if err != nil {
		return nil, err
	}
	// Offline-shape evidence per (org, key, model).
	type shape struct {
		total   int64
		offline int64
		big     int64
	}
	shapes := map[string]*shape{}
	for _, l := range logs {
		key := l.OrganizationID + "\x00" + l.APIKeyID + "\x00" + l.ModelID
		s, ok := shapes[key]
		if !ok {
			s = &shape{}
			shapes[key] = s
		}
		s.total++
		if !inPeakWindow(l.CreatedAt, rc.cfg) {
			s.offline++
		}
		if l.PromptTokens >= rc.cfg.R2.PromptTokenFloor && l.Status != "streaming" {
			s.big++
		}
	}
	// Aggregate spend per (org, key, model).
	type keySpend struct {
		OrganizationID string
		APIKeyID       string
		ModelID        string
		Requests       int64
		Spend          float64
	}
	spends := map[string]*keySpend{}
	for _, g := range groups {
		key := g.OrganizationID + "\x00" + g.APIKeyID + "\x00" + g.ModelID
		ks, ok := spends[key]
		if !ok {
			ks = &keySpend{OrganizationID: g.OrganizationID, APIKeyID: g.APIKeyID, ModelID: g.ModelID}
			spends[key] = ks
		}
		ks.Requests += g.Requests
		ks.Spend += g.Spend
	}
	var out []ComputedRecommendation
	for key, ks := range spends {
		if ks.Requests < int64(rc.cfg.R2.MinRequests) {
			continue
		}
		// The model must exist in the catalog (AD13).
		modelRow, ok := modelByID[ks.ModelID]
		if !ok {
			continue
		}
		s := shapes[key]
		if s == nil || s.total == 0 {
			continue
		}
		offlineRatio := float64(s.offline) / float64(s.total)
		bigRatio := float64(s.big) / float64(s.total)
		if offlineRatio < rc.cfg.R2.OfflineRatio && bigRatio < rc.cfg.R2.OfflineRatio {
			continue
		}
		saving := batch.BatchDiscountFactor * ks.Spend
		suggestion := fmt.Sprintf("Key %s's offline workload on %s fits the batch API", ks.APIKeyID, modelRow.Name)
		inputs := map[string]any{
			"window_days":        rc.cfg.WindowDays,
			"requests":           ks.Requests,
			"spend":              round2(ks.Spend),
			"offline_ratio":      round4(offlineRatio),
			"large_prompt_ratio": round4(bigRatio),
			"batch_discount":     batch.BatchDiscountFactor,
		}
		arithmetic := fmt.Sprintf("%s x %s = %s %s/month",
			formatAmount(batch.BatchDiscountFactor), formatAmount(ks.Spend),
			rc.cfg.Currency, formatAmount(saving))
		out = append(out, ComputedRecommendation{
			Rule:                   RuleBatch,
			TargetType:             TargetAPIKey,
			TargetID:               ks.OrganizationID + "/" + ks.APIKeyID + "/" + ks.ModelID,
			TargetDisplay:          ks.APIKeyID + " / " + modelRow.Name,
			OrganizationID:         ks.OrganizationID,
			ModelID:                ks.ModelID,
			APIKeyID:               ks.APIKeyID,
			Suggestion:             suggestion,
			EstimatedMonthlySaving: round2(saving),
			Inputs:                 inputs,
			Arithmetic:             arithmetic,
			ActingPageAdmin:        ActingLink{Route: "/admin/batch", Label: "Submit batch job"},
			ActingPageUser:         ActingLink{Route: "/batch", Label: "Submit batch job"},
		})
	}
	return out, nil
}

// inPeakWindow reports whether t falls inside a configured peak window
// (evaluated in optimization.timezone; weekdays only when
// peakWeekdaysOnly).
func inPeakWindow(t time.Time, cfg config.OptimizationConfig) bool {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := t.In(loc)
	if cfg.PeakWeekdaysOnly {
		wd := local.Weekday()
		if wd == time.Saturday || wd == time.Sunday {
			return false
		}
	}
	minutes := local.Hour()*60 + local.Minute()
	for _, window := range cfg.PeakHours {
		start, end, ok := parseHourWindow(window)
		if !ok {
			continue
		}
		if minutes >= start && minutes < end {
			return true
		}
	}
	return false
}

// parseHourWindow parses "HH:MM-HH:MM" into minutes.
func parseHourWindow(window string) (int, int, bool) {
	parts := strings.SplitN(window, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, ok1 := parseHourMinute(parts[0])
	end, ok2 := parseHourMinute(parts[1])
	if !ok1 || !ok2 || start >= end {
		return 0, 0, false
	}
	return start, end, true
}

// parseHourMinute parses "HH:MM" into minutes since midnight.
func parseHourMinute(s string) (int, bool) {
	parts := strings.SplitN(strings.TrimSpace(s), ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	var h, m int
	if _, err := fmt.Sscanf(parts[0], "%d", &h); err != nil {
		return 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &m); err != nil {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// ruleIdleService is R3 — idle-service scale-to-zero (admin-only;
// architecture 13.3).
func ruleIdleService(ctx context.Context, rc *ruleContext) ([]ComputedRecommendation, error) {
	// The card-type seam is required (AD14); nil -> skip the rule.
	if rc.cardTypes == nil {
		return nil, nil
	}
	services, err := rc.services.ActiveServices(ctx)
	if err != nil {
		return nil, err
	}
	global, err := rc.services.GlobalAutoscalingPolicy(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := rc.requests.CountByServiceSince(ctx, rc.windowEnd.AddDate(0, 0, -rc.cfg.R3.IdleWindowDays))
	if err != nil {
		return nil, err
	}
	cardTypes, err := rc.cardTypes.ListCardTypes(ctx)
	if err != nil {
		return nil, err
	}
	fleetCards := map[string]bool{}
	for _, ct := range cardTypes {
		fleetCards[ct.CardType] = true
	}
	var out []ComputedRecommendation
	for _, svc := range services {
		// Effective autoscaling: the service JSON overrides the global
		// singleton; empty {} = inherit.
		enabled, scaleToZero, err := effectiveAutoscaling(svc.AutoscalingJSON, global)
		if err != nil {
			return nil, err
		}
		if enabled && scaleToZero {
			continue // already scales to zero
		}
		// Idle: requests/day below the threshold.
		windowDays := rc.cfg.R3.IdleWindowDays
		if windowDays <= 0 {
			windowDays = 1
		}
		requestsPerDay := float64(counts[svc.ID]) / float64(windowDays)
		if requestsPerDay >= float64(rc.cfg.R3.IdleRequestsPerDay) {
			continue
		}
		// Card gate (AD14): the card must be priced in config and
		// present in the fleet.
		hourlyCost, ok := cardHourlyCostOf(rc.cfg.CardHourlyCosts, svc.AcceleratorType)
		if !ok || !fleetCards[svc.AcceleratorType] {
			continue
		}
		saving := float64(svc.Replicas) * hourlyCost * 24 * 30
		suggestion := fmt.Sprintf("Enable scale-to-zero on service %s", svc.Name)
		inputs := map[string]any{
			"service_id":            svc.ID,
			"service_name":          svc.Name,
			"replicas":              svc.Replicas,
			"accelerator_type":      svc.AcceleratorType,
			"hourly_cost":           hourlyCost,
			"idle_requests_per_day": round4(requestsPerDay),
			"idle_window_days":      rc.cfg.R3.IdleWindowDays,
		}
		arithmetic := fmt.Sprintf("%d replicas x %s/hour x 24 x 30 = %s %s/month",
			svc.Replicas, formatAmount(hourlyCost), rc.cfg.Currency, formatAmount(saving))
		out = append(out, ComputedRecommendation{
			Rule:                   RuleIdleService,
			TargetType:             TargetService,
			TargetID:               svc.ID,
			TargetDisplay:          svc.Name,
			OrganizationID:         svc.OrganizationID,
			ServiceID:              svc.ID,
			Suggestion:             suggestion,
			EstimatedMonthlySaving: round2(saving),
			Inputs:                 inputs,
			Arithmetic:             arithmetic,
			ActingPageAdmin:        ActingLink{Route: "/admin/autoscaling", Label: "Enable scale-to-zero"},
		})
	}
	return out, nil
}

// effectiveAutoscaling resolves the service's effective policy: the
// per-service JSON overrides the global singleton; empty {} inherits.
func effectiveAutoscaling(autoscalingJSON string, global *PolicyRow) (enabled bool, scaleToZero bool, err error) {
	if global != nil {
		enabled, scaleToZero = global.Enabled, global.ScaleToZero
	}
	if strings.TrimSpace(autoscalingJSON) == "" || autoscalingJSON == "{}" {
		return enabled, scaleToZero, nil
	}
	var policy struct {
		Enabled     bool `json:"enabled"`
		ScaleToZero bool `json:"scale_to_zero"`
	}
	if err := json.Unmarshal([]byte(autoscalingJSON), &policy); err != nil {
		return false, false, err
	}
	return policy.Enabled, policy.ScaleToZero, nil
}

// ruleCache is R4 — cache-hit improvement (per org, per model;
// architecture 13.3).
func ruleCache(ctx context.Context, rc *ruleContext) ([]ComputedRecommendation, error) {
	groups, err := rc.charges.ChargeGroups(ctx, rc.windowStart)
	if err != nil {
		return nil, err
	}
	models, err := rc.catalog.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	engines, err := rc.catalog.ImageEngines(ctx)
	if err != nil {
		return nil, err
	}
	services, err := rc.services.ActiveServices(ctx)
	if err != nil {
		return nil, err
	}
	modelByID := map[string]ModelRow{}
	for _, m := range models {
		modelByID[m.ID] = m
	}
	// Engine gate (AD15): models served by a prefix-cache engine.
	cacheCapable := map[string]bool{}
	for _, svc := range services {
		if engines[svc.ImageID] != "" {
			for _, engine := range rc.cfg.PrefixCacheEngines {
				if engines[svc.ImageID] == engine {
					cacheCapable[svc.ModelID] = true
				}
			}
		}
	}
	usage := rollupByOrgModel(groups)
	var out []ComputedRecommendation
	for _, u := range usage {
		if u.PromptTokens < rc.cfg.R4.MinPromptTokens {
			continue
		}
		hitRate := float64(u.CachedTokens) / float64(u.PromptTokens)
		if hitRate >= rc.cfg.R4.MaxCacheHitRate {
			continue
		}
		if !cacheCapable[u.ModelID] {
			continue
		}
		modelRow, ok := modelByID[u.ModelID]
		if !ok {
			continue
		}
		var total float64
		var discountTokens float64
		var perCard []map[string]any
		for card, g := range u.byCard {
			rates, err := applicableRates(ctx, rc, u.ModelID, card)
			if err != nil {
				return nil, err
			}
			if rates == nil {
				continue
			}
			discount := rates.Input - rates.Cached
			if discount <= 0 {
				continue
			}
			cacheMiss := g.PromptTokens - g.CachedTokens
			assumedCached := rc.cfg.R4.DefaultHitAssumption * float64(cacheMiss)
			saving := assumedCached / 1e6 * discount
			total += saving
			discountTokens += float64(cacheMiss) * discount
			perCard = append(perCard, map[string]any{
				"card":                     card,
				"prompt_tokens":            g.PromptTokens,
				"cached_tokens":            g.CachedTokens,
				"input_price_per_million":  rates.Input,
				"cached_price_per_million": rates.Cached,
				"assumed_cached_tokens":    round2(assumedCached),
				"saving":                   round2(saving),
			})
		}
		if total <= 0 {
			continue
		}
		// The token-weighted average discount across cards.
		weightedDiscount := 0.0
		if miss := float64(u.PromptTokens - u.CachedTokens); miss > 0 {
			weightedDiscount = discountTokens / miss
		}
		suggestion := fmt.Sprintf("Structure prompts on %s for prefix caching", modelRow.Name)
		inputs := map[string]any{
			"window_days":      rc.cfg.WindowDays,
			"prompt_tokens":    u.PromptTokens,
			"cached_tokens":    u.CachedTokens,
			"cache_hit_rate":   round4(hitRate),
			"assumed_hit_rate": rc.cfg.R4.DefaultHitAssumption,
			"per_card":         perCard,
		}
		arithmetic := fmt.Sprintf("%s x %dM uncached prompt x %s discount = %s %s/month",
			formatAmount(rc.cfg.R4.DefaultHitAssumption),
			(u.PromptTokens-u.CachedTokens)/1e6,
			formatPrice(rc.cfg.Currency)+formatAmount(weightedDiscount),
			rc.cfg.Currency, formatAmount(total))
		out = append(out, ComputedRecommendation{
			Rule:                   RuleCache,
			TargetType:             TargetOrgModel,
			TargetID:               u.OrganizationID + "/" + u.ModelID,
			TargetDisplay:          modelRow.Name,
			OrganizationID:         u.OrganizationID,
			ModelID:                u.ModelID,
			Suggestion:             suggestion,
			EstimatedMonthlySaving: round2(total),
			Inputs:                 inputs,
			Arithmetic:             arithmetic,
			ActingPageAdmin:        ActingLink{Route: "/admin/pricing", Label: "View pricing"},
			ActingPageUser:         ActingLink{Route: "/pricing", Label: "View pricing"},
		})
	}
	return out, nil
}

// round2 rounds to 2 decimal places (the stored precision).
func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

// round4 rounds to 4 decimal places (ratio inputs).
func round4(v float64) float64 {
	return float64(int64(v*10000+0.5)) / 10000
}

// formatAmount renders a money-ish value with 2 decimals.
func formatAmount(v float64) string {
	return fmt.Sprintf("%.2f", v)
}

// formatPrice renders the currency symbol prefix.
func formatPrice(currency string) string {
	switch currency {
	case "CNY":
		return "¥"
	case "USD":
		return "$"
	default:
		return currency + " "
	}
}
