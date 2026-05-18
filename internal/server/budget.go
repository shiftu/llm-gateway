package server

import (
	"fmt"
	"net/http"
	"time"

	authpkg "github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/store"
)

// BudgetMW enforces per-team USD budget limits before forwarding requests.
// Only inbound LLM traffic (/v1/*) is checked; /mcp is skipped.
// Legacy __legacy__ tokens bypass budget checks (no team context).
type BudgetMW struct {
	st *store.Store
}

// NewBudgetMW creates a budget middleware backed by the given store.
func NewBudgetMW(st *store.Store) *BudgetMW {
	return &BudgetMW{st: st}
}

// Middleware returns an http.Handler that checks both day and month budgets
// before calling next. Returns 429 when either period's hard cap is breached.
func (b *BudgetMW) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			next.ServeHTTP(w, r)
			return
		}
		ak, ok := authpkg.APIKeyFromContext(r.Context())
		if !ok || ak.ID == "__legacy__" || ak.TeamID == "" {
			next.ServeHTTP(w, r)
			return
		}

		now := time.Now().UTC()
		for _, period := range []string{"day", "month"} {
			status, err := b.st.CheckBudget(ak.TeamID, period, now)
			if err != nil || !status.HardBreached {
				continue
			}
			writeBudgetError(w, period, status.LimitUSD, status.SpentUSD)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func writeBudgetError(w http.ResponseWriter, period string, limitUSD, spentUSD float64) {
	writeStructuredError(w, http.StatusTooManyRequests, "budget_exceeded",
		fmt.Sprintf("%s budget of $%.4f exceeded (spent $%.4f)", period, limitUSD, spentUSD),
		"contact your gateway admin to increase the budget or wait for the period to reset")
}
