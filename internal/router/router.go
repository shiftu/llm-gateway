// Package router resolves an inbound model name into a concrete upstream
// destination (Provider + upstream model name).
//
// Resolution rules (plan §4 Task 5):
//   1. Alias hit       — alias.provider + alias.upstream_model
//   2. Default provider — pass client model through unchanged
//   3. Neither          — ErrNoRoute (caller surfaces F-DX-05 structured error)
//
// Per plan F-2 the router reads the store on every Resolve call — no
// in-memory cache. Per F-3 the returned Route holds an immutable snapshot
// of the Provider row so the caller can finish the request without racing
// against MCP CRUD that mutates the underlying table.
package router

import (
	"errors"
	"fmt"

	"github.com/panda/llm-gateway/internal/store"
)

// ErrNoRoute means the gateway has no way to fulfil the request: no alias
// matched the inbound model name and no default provider is set. The HTTP
// handler turns this into a 404 with an actionable error.fix per F-DX-05.
var ErrNoRoute = errors.New("router: no route for model — add an alias or set a default provider")

// Route is a snapshot of the resolution decision. ViaAlias is populated when
// the match came from model_aliases; empty for the default-provider path.
type Route struct {
	Provider      store.Provider
	UpstreamModel string
	ViaAlias      string
}

// Router resolves inbound model names against a Store. Cheap to construct;
// holds no state of its own.
type Router struct {
	store *store.Store
}

// New wires the router to its backing store.
func New(s *store.Store) *Router { return &Router{store: s} }

// Resolve picks the upstream Provider + model for a client-requested model name.
// See package doc for the priority order.
func (r *Router) Resolve(clientModel string) (Route, error) {
	if alias, err := r.store.ResolveAlias(clientModel); err == nil {
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

	def, err := r.store.GetDefaultProvider()
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Route{}, ErrNoRoute
		}
		return Route{}, err
	}
	return Route{Provider: def, UpstreamModel: clientModel}, nil
}
