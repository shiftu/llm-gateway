package store

import (
	"database/sql"
	"errors"
	"time"
)

// Budget mirrors a row in the budgets table.
type Budget struct {
	TeamID           string
	Period           string  // "day" | "month"
	USDLimit         float64
	SoftThresholdPct int
	HardCapAction    string // "warn" | "block"
	UpdatedAt        time.Time
}

// BudgetStatus is the result of a CheckBudget call.
type BudgetStatus struct {
	SpentUSD     float64
	LimitUSD     float64
	UsedPct      float64
	SoftBreached bool // >= soft_threshold_pct
	HardBreached bool // >= 100% AND hard_cap_action='block'
	Action       string
}

// SetBudget upserts a budget for a team+period.
func (s *Store) SetBudget(b Budget) error {
	_, err := s.db.Exec(`
		INSERT INTO budgets
			(team_id, period, usd_limit, soft_threshold_pct, hard_cap_action, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(team_id, period) DO UPDATE SET
			usd_limit          = excluded.usd_limit,
			soft_threshold_pct = excluded.soft_threshold_pct,
			hard_cap_action    = excluded.hard_cap_action,
			updated_at         = excluded.updated_at`,
		b.TeamID, b.Period, b.USDLimit, b.SoftThresholdPct, b.HardCapAction,
		b.UpdatedAt.UnixMilli(),
	)
	return err
}

// GetBudget returns the budget for a team+period, ErrNotFound if absent.
func (s *Store) GetBudget(teamID, period string) (Budget, error) {
	row := s.db.QueryRow(`
		SELECT team_id, period, usd_limit, soft_threshold_pct, hard_cap_action, updated_at
		FROM budgets WHERE team_id = ? AND period = ?`,
		teamID, period,
	)
	return scanBudget(row)
}

// ListBudgets returns all budgets, optionally filtered by teamID (empty = all).
func (s *Store) ListBudgets(teamID string) ([]Budget, error) {
	var rows *sql.Rows
	var err error
	if teamID == "" {
		rows, err = s.db.Query(`
			SELECT team_id, period, usd_limit, soft_threshold_pct, hard_cap_action, updated_at
			FROM budgets ORDER BY team_id ASC, period ASC`)
	} else {
		rows, err = s.db.Query(`
			SELECT team_id, period, usd_limit, soft_threshold_pct, hard_cap_action, updated_at
			FROM budgets WHERE team_id = ? ORDER BY period ASC`,
			teamID,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Budget
	for rows.Next() {
		b, err := scanBudget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CheckBudget computes current spend vs limit for team+period. Returns zero
// BudgetStatus (HardBreached=false) when no budget is configured.
func (s *Store) CheckBudget(teamID, period string, now time.Time) (BudgetStatus, error) {
	b, err := s.GetBudget(teamID, period)
	if errors.Is(err, ErrNotFound) {
		return BudgetStatus{}, nil
	}
	if err != nil {
		return BudgetStatus{}, err
	}

	fromDay, toDay := periodRange(period, now)

	var totalMicros int64
	err = s.db.QueryRow(`
		SELECT COALESCE(SUM(uc.cost_usd_micros), 0)
		FROM usage_counters uc
		JOIN api_keys ak ON uc.api_key_id = ak.id
		WHERE ak.team_id = ?
		  AND uc.day_utc >= ?
		  AND uc.day_utc <= ?`,
		teamID, fromDay, toDay,
	).Scan(&totalMicros)
	if err != nil {
		return BudgetStatus{}, err
	}

	spentUSD := float64(totalMicros) / 1_000_000.0
	usedPct := 0.0
	if b.USDLimit > 0 {
		usedPct = (spentUSD / b.USDLimit) * 100.0
	}

	softBreached := usedPct >= float64(b.SoftThresholdPct)
	hardBreached := usedPct >= 100.0 && b.HardCapAction == "block"

	return BudgetStatus{
		SpentUSD:     spentUSD,
		LimitUSD:     b.USDLimit,
		UsedPct:      usedPct,
		SoftBreached: softBreached,
		HardBreached: hardBreached,
		Action:       b.HardCapAction,
	}, nil
}

// periodRange returns [fromDayUTC, toDayUTC] as ms-epoch start-of-day values
// for the given period around now. Both bounds are inclusive day_utc values.
func periodRange(period string, now time.Time) (int64, int64) {
	y, m, d := now.UTC().Date()
	switch period {
	case "month":
		from := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		// last day of month: first day of next month minus one day
		to := time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC)
		return from.UnixMilli(), to.UnixMilli()
	default: // "day"
		day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		return day.UnixMilli(), day.UnixMilli()
	}
}

func scanBudget(r rowScanner) (Budget, error) {
	var b Budget
	var updatedAt int64
	err := r.Scan(&b.TeamID, &b.Period, &b.USDLimit, &b.SoftThresholdPct, &b.HardCapAction, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Budget{}, ErrNotFound
	}
	if err != nil {
		return Budget{}, err
	}
	b.UpdatedAt = time.UnixMilli(updatedAt)
	return b, nil
}
