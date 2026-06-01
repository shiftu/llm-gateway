package store

import (
	"database/sql"
	"errors"
	"time"
)

// usage.go — CRUD for usage_counters and model_costs.
//
// usage_counters uses the SQLite "UPSERT-with-increment" pattern: a single
// statement that either inserts a fresh (api_key_id, day_utc) row or
// atomically increments the existing one. This makes ReserveQuota /
// CommitUsage safe under concurrent inbound load (T10 middleware) without
// needing an explicit transaction per request.
//
// model_costs is time-versioned: SetModelCost stamps an effective_from
// boundary; GetModelCost(asOf) picks the most recent row with
// effective_from <= asOf, enabling price changes without rewriting history.

// UsageCounter mirrors a row in usage_counters. day_utc is the ms-epoch of
// 00:00 UTC for the day — keeps the schema TZ-agnostic.
type UsageCounter struct {
	APIKeyID        string
	DayUTC          int64
	RequestCount    int64
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CostUSDMicros   int64
}

// ReserveQuota atomically adds deltaRequests to the (api_key_id, day_utc)
// counter, inserting the row at zero if it didn't exist. Returns the new
// counter state so the quota middleware can decide whether to reject the
// reservation post-increment (compare-and-rollback pattern).
//
// Caller contract: deltaRequests is typically 1. If the post-reserve
// counter exceeds the configured max_requests quota, the caller should
// either issue a compensating decrement OR accept that the user briefly
// saw their N+1th request rejected with a quota error AFTER the row was
// bumped — both are acceptable per CEO-F7 (counters are best-effort,
// quotas are gates not contracts).
func (s *Store) ReserveQuota(apiKeyID string, dayUTC int64, deltaRequests int64) (UsageCounter, error) {
	row := s.db.QueryRow(`
		INSERT INTO usage_counters
			(api_key_id, day_utc, request_count, input_tokens, output_tokens, reasoning_tokens, cost_usd_micros)
			VALUES (?, ?, ?, 0, 0, 0, 0)
		ON CONFLICT(api_key_id, day_utc) DO UPDATE SET
			request_count = request_count + ?
		RETURNING request_count, input_tokens, output_tokens, reasoning_tokens, cost_usd_micros`,
		apiKeyID, dayUTC, deltaRequests, deltaRequests,
	)
	c := UsageCounter{APIKeyID: apiKeyID, DayUTC: dayUTC}
	if err := row.Scan(&c.RequestCount, &c.InputTokens, &c.OutputTokens, &c.ReasoningTokens, &c.CostUSDMicros); err != nil {
		return UsageCounter{}, err
	}
	return c, nil
}

// CommitUsage adds token + cost deltas to the counter, seeding the row if
// reserve was skipped. Called after the upstream response is parsed for
// real token counts — typically alongside the request_logs INSERT.
func (s *Store) CommitUsage(apiKeyID string, dayUTC int64, input, output, reasoning int64, costMicros int64) error {
	_, err := s.db.Exec(`
		INSERT INTO usage_counters
			(api_key_id, day_utc, request_count, input_tokens, output_tokens, reasoning_tokens, cost_usd_micros)
			VALUES (?, ?, 0, ?, ?, ?, ?)
		ON CONFLICT(api_key_id, day_utc) DO UPDATE SET
			input_tokens     = input_tokens     + ?,
			output_tokens    = output_tokens    + ?,
			reasoning_tokens = reasoning_tokens + ?,
			cost_usd_micros  = cost_usd_micros  + ?`,
		apiKeyID, dayUTC, input, output, reasoning, costMicros,
		input, output, reasoning, costMicros,
	)
	return err
}

// SumDayRequests returns the total request_count for a key over a range of
// day_utc values [fromDayUTC, toDayUTC). Used by the quota middleware for
// month-window best-effort checks. Returns 0 if no rows exist (not ErrNotFound).
func (s *Store) SumDayRequests(apiKeyID string, fromDayUTC, toDayUTC int64) (int64, error) {
	var total int64
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(request_count), 0) FROM usage_counters
		 WHERE api_key_id = ? AND day_utc >= ? AND day_utc < ?`,
		apiKeyID, fromDayUTC, toDayUTC,
	).Scan(&total)
	return total, err
}

func (s *Store) GetUsageCounter(apiKeyID string, dayUTC int64) (UsageCounter, error) {
	row := s.db.QueryRow(
		`SELECT api_key_id, day_utc, request_count, input_tokens, output_tokens, reasoning_tokens, cost_usd_micros
		 FROM usage_counters WHERE api_key_id = ? AND day_utc = ?`,
		apiKeyID, dayUTC,
	)
	var c UsageCounter
	err := row.Scan(&c.APIKeyID, &c.DayUTC, &c.RequestCount,
		&c.InputTokens, &c.OutputTokens, &c.ReasoningTokens, &c.CostUSDMicros)
	if errors.Is(err, sql.ErrNoRows) {
		return UsageCounter{}, ErrNotFound
	}
	if err != nil {
		return UsageCounter{}, err
	}
	return c, nil
}

// --- ModelCost ---

// ModelCost mirrors a row in model_costs. Pricing is per 1k tokens in USD
// (matching the published provider rate cards); effective_from establishes
// the time boundary for historical accuracy in cost reconciliation.
type ModelCost struct {
	Provider          string
	Model             string
	USDPerInput1k     float64
	USDPerOutput1k    float64
	USDPerReasoning1k *float64 // nil = same as output (most providers)
	USDPerCached1k    *float64 // nil = no cache discount, cached tokens billed at input rate
	EffectiveFrom     time.Time
}

func (s *Store) SetModelCost(c ModelCost) error {
	_, err := s.db.Exec(`
		INSERT INTO model_costs
			(provider, model, usd_per_input_1k, usd_per_output_1k, usd_per_reasoning_1k, usd_per_cached_1k, effective_from)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, model, effective_from) DO UPDATE SET
			usd_per_input_1k     = excluded.usd_per_input_1k,
			usd_per_output_1k    = excluded.usd_per_output_1k,
			usd_per_reasoning_1k = excluded.usd_per_reasoning_1k,
			usd_per_cached_1k    = excluded.usd_per_cached_1k`,
		c.Provider, c.Model, c.USDPerInput1k, c.USDPerOutput1k,
		nullFloat64(c.USDPerReasoning1k), nullFloat64(c.USDPerCached1k),
		c.EffectiveFrom.UnixMilli(),
	)
	return err
}

// GetModelCost picks the most recent pricing row with effective_from <= asOf.
// Returns ErrNotFound if no row was effective at that point in time.
func (s *Store) GetModelCost(provider, model string, asOf time.Time) (ModelCost, error) {
	row := s.db.QueryRow(`
		SELECT provider, model, usd_per_input_1k, usd_per_output_1k, usd_per_reasoning_1k, usd_per_cached_1k, effective_from
		FROM model_costs
		WHERE provider = ? AND model = ? AND effective_from <= ?
		ORDER BY effective_from DESC LIMIT 1`,
		provider, model, asOf.UnixMilli(),
	)
	return scanModelCost(row)
}

func (s *Store) ListModelCosts() ([]ModelCost, error) {
	rows, err := s.db.Query(`
		SELECT provider, model, usd_per_input_1k, usd_per_output_1k, usd_per_reasoning_1k, usd_per_cached_1k, effective_from
		FROM model_costs ORDER BY provider ASC, model ASC, effective_from ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelCost
	for rows.Next() {
		c, err := scanModelCost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanModelCost(r rowScanner) (ModelCost, error) {
	var c ModelCost
	var reasoning, cached sql.NullFloat64
	var effective int64
	err := r.Scan(&c.Provider, &c.Model, &c.USDPerInput1k, &c.USDPerOutput1k, &reasoning, &cached, &effective)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelCost{}, ErrNotFound
	}
	if err != nil {
		return ModelCost{}, err
	}
	if reasoning.Valid {
		v := reasoning.Float64
		c.USDPerReasoning1k = &v
	}
	if cached.Valid {
		v := cached.Float64
		c.USDPerCached1k = &v
	}
	c.EffectiveFrom = time.UnixMilli(effective)
	return c, nil
}

func nullFloat64(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}
