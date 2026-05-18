package router

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/panda/llm-gateway/internal/store"
)

// ErrNoRoute means the gateway has no way to fulfil the request: no alias
// matched the inbound model name and no default provider is set. The HTTP
// handler turns this into a 404 with an actionable error.fix per F-DX-05.
var ErrNoRoute = errors.New("router: no route for model — add an alias or set a default provider")

// Route is a snapshot of the resolution decision. ViaAlias is populated when
// the match came from model_aliases; ViaRule when matched by a routing rule;
// both empty for the default-provider path. Cognitive is populated only
// when the matched alias is in cognitive mode (v0.3 T3).
type Route struct {
	Provider      store.Provider
	UpstreamModel string
	ViaAlias      string
	ViaRule       string
	Cognitive     CognitiveTrace
}

// CognitiveTrace records the score-and-pick decision for an alias in
// cognitive mode. T5 explain-trace renders this verbatim so agents can
// audit why a provider won. Candidates is sorted descending by Total.
type CognitiveTrace struct {
	Weights    Weights
	Candidates []Candidate
}

// Candidate is one provider's evaluation under the cognitive resolver.
type Candidate struct {
	ProviderName string
	Inputs       ScoreInputs
	Breakdown    Breakdown
}

// ScoreInputsResolver supplies the per-provider observations the cognitive
// resolver feeds into Score. Production wires this to the model_costs
// store + health Manager snapshot; tests pass a stub.
type ScoreInputsResolver func(p store.Provider) ScoreInputs

// Router resolves inbound model names against a Store. Cheap to construct;
// holds no state of its own.
type Router struct {
	store *store.Store
	score ScoreInputsResolver // nil → cognitive aliases degrade to static
}

// New wires the router to its backing store. Cognitive aliases will degrade
// to static behavior (use the named provider) since no scorer is wired.
func New(s *store.Store) *Router { return &Router{store: s} }

// NewWithScorer wires the router with both a store and a per-provider
// scoring-input resolver. Required for cognitive aliases to actually score
// candidates; without it, cognitive aliases route like static.
func NewWithScorer(s *store.Store, sc ScoreInputsResolver) *Router {
	return &Router{store: s, score: sc}
}

// Resolve picks the upstream Provider + model for a client-requested model name.
// Equivalent to ResolveForTeam with an empty teamID (global-only lookup).
func (r *Router) Resolve(clientModel string) (Route, error) {
	return r.ResolveForTeam(clientModel, "")
}

// ResolveForTeam resolves clientModel with team-aware priority:
//  1. Team-scoped alias  (when teamID != "")
//  2. Global alias       (team_id IS NULL)
//  3. Routing rules      (team-scoped then global, by priority)
//  4. Global default provider (pass clientModel through)
func (r *Router) ResolveForTeam(clientModel, teamID string) (Route, error) {
	// Tier 1: Alias hit
	if alias, err := r.store.ResolveAliasForTeam(clientModel, teamID); err == nil {
		if alias.Mode == "cognitive" && r.score != nil {
			return r.resolveCognitive(alias, teamID)
		}
		p, perr := r.store.GetProvider(alias.ProviderName)
		if perr != nil {
			if errors.Is(perr, store.ErrNotFound) {
				return Route{}, fmt.Errorf("alias %q points to missing provider %q: %w", clientModel, alias.ProviderName, ErrNoRoute)
			}
			return Route{}, perr
		}
		return Route{Provider: p, UpstreamModel: alias.UpstreamModel, ViaAlias: clientModel}, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return Route{}, err
	}

	// Tier 2: Routing rules (conditional dispatch by model name or provider kind)
	if route, ok, err := r.matchRoutingRules(clientModel, teamID); err != nil {
		return Route{}, err
	} else if ok {
		return route, nil
	}

	// Tier 3: Default provider fallback
	def, err := r.store.GetDefaultProvider()
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Route{}, ErrNoRoute
		}
		return Route{}, err
	}
	return Route{Provider: def, UpstreamModel: clientModel}, nil
}

// matchRoutingRules evaluates active routing rules for the given team.
// Returns (route, true, nil) on first match, (zero, false, nil) if no rule matches.
func (r *Router) matchRoutingRules(clientModel, teamID string) (Route, bool, error) {
	rules, err := r.store.MatchRoutingRules(teamID)
	if err != nil {
		return Route{}, false, err
	}

	// Load all providers once for kind-matching
	providers, err := r.store.ListProviders()
	if err != nil {
		return Route{}, false, err
	}
	providerByKind := make(map[string][]store.Provider) // kind -> providers
	for _, p := range providers {
		providerByKind[p.Kind] = append(providerByKind[p.Kind], p)
	}

	for _, rule := range rules {
		matched := false
		switch rule.MatchField {
		case "model":
			matched = matchValue(rule.MatchOp, rule.MatchValue, clientModel)
		case "kind":
			// Match if any provider of this kind exists AND the rule's
			// provider_name is one of those providers
			if _, ok := providerByKind[rule.MatchValue]; ok {
				matched = true
			}
		}

		if !matched {
			continue
		}

		// Resolve the rule's target provider
		p, perr := r.store.GetProvider(rule.ProviderName)
		if perr != nil {
			if errors.Is(perr, store.ErrNotFound) {
				continue // skip broken rule, try next
			}
			return Route{}, false, perr
		}

		upstream := clientModel
		if rule.UpstreamModel != "" {
			upstream = rule.UpstreamModel
		}
		return Route{Provider: p, UpstreamModel: upstream, ViaRule: ruleName(rule)}, true, nil
	}

	return Route{}, false, nil
}

// matchValue evaluates a match operation against a target string.
func matchValue(op, pattern, target string) bool {
	switch op {
	case "equals":
		return target == pattern
	case "prefix":
		return strings.HasPrefix(target, pattern)
	case "regex":
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false // invalid regex = no match
		}
		return re.MatchString(target)
	default:
		return false
	}
}

func ruleName(r store.RoutingRule) string {
	return fmt.Sprintf("rule#%d(%s/%s/%s)", r.ID, r.MatchField, r.MatchOp, r.MatchValue)
}

// resolveCognitive picks the highest-scoring eligible provider for a
// cognitive-mode alias. v0.3 T3 eligibility = all configured providers;
// T11 will narrow this via the capability registry. Weights come from the
// per-team routing_weights (defaults applied when missing).
//
// Returns Route with the chosen provider, the alias's UpstreamModel, and a
// CognitiveTrace (candidates sorted by Total desc) so T5 explain-trace can
// surface the decision to agents.
func (r *Router) resolveCognitive(alias store.Alias, teamID string) (Route, error) {
	candidates, err := r.store.ListProviders()
	if err != nil {
		return Route{}, err
	}
	if len(candidates) == 0 {
		return Route{}, fmt.Errorf("cognitive alias %q has no candidate providers: %w", alias.Alias, ErrNoRoute)
	}
	storeWeights, err := r.store.GetRoutingWeights(teamID)
	if err != nil {
		return Route{}, err
	}
	weights := Weights{
		Cost:    storeWeights.Cost,
		Latency: storeWeights.Latency,
		Quality: storeWeights.Quality,
		Health:  storeWeights.Health,
	}

	cands := make([]Candidate, 0, len(candidates))
	for _, p := range candidates {
		inputs := r.score(p)
		br := Score(inputs, weights)
		cands = append(cands, Candidate{ProviderName: p.Name, Inputs: inputs, Breakdown: br})
	}
	// Sort descending by Total.
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].Breakdown.Total > cands[j].Breakdown.Total
	})

	winnerName := cands[0].ProviderName
	if cands[0].Breakdown.Total == 0 {
		// All candidates are unhealthy (per T2 short-circuit). Fail loudly
		// rather than silently routing to a dead provider.
		return Route{}, fmt.Errorf("cognitive alias %q has no healthy candidates: %w", alias.Alias, ErrNoRoute)
	}
	winnerProvider, err := r.store.GetProvider(winnerName)
	if err != nil {
		return Route{}, err
	}
	return Route{
		Provider:      winnerProvider,
		UpstreamModel: alias.UpstreamModel,
		ViaAlias:      alias.Alias,
		Cognitive: CognitiveTrace{
			Weights:    weights,
			Candidates: cands,
		},
	}, nil
}
