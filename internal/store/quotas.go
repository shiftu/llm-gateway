package store

import (
	"database/sql"
	"errors"
)

// quotas.go — CRUD for the quotas table. The schema enforces enum values
// at the DB layer via CHECK constraints; the Go API surfaces those errors
// verbatim rather than re-validating, so the source of truth stays in one
// place (the migration).
//
// Used by the quota middleware (T10) to gate inbound traffic by team or
// per-key limits across {month, day, minute} windows. Any of MaxRequests /
// MaxTokens / MaxUSDMicros may be nil meaning "unlimited on this dimension".

type Quota struct {
	ScopeKind    string // 'team' | 'key'
	ScopeID      string
	Window       string // 'month' | 'day' | 'minute'
	MaxRequests  *int64
	MaxTokens    *int64
	MaxUSDMicros *int64
}

// SetQuota upserts on (scope_kind, scope_id, window). Re-setting overwrites
// all three limit columns — pass the FULL desired quota each time rather
// than expecting field-level merging. Bad scope_kind / window values land
// at the DB CHECK and surface as a generic SQL error (this is fine: the
// CLI / MCP layer already validates input before reaching the store).
func (s *Store) SetQuota(q Quota) error {
	_, err := s.db.Exec(`
		INSERT INTO quotas (scope_kind, scope_id, window, max_requests, max_tokens, max_usd_micros)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope_kind, scope_id, window) DO UPDATE SET
			max_requests   = excluded.max_requests,
			max_tokens     = excluded.max_tokens,
			max_usd_micros = excluded.max_usd_micros`,
		q.ScopeKind, q.ScopeID, q.Window,
		nullInt64(q.MaxRequests), nullInt64(q.MaxTokens), nullInt64(q.MaxUSDMicros),
	)
	return err
}

func (s *Store) GetQuota(scopeKind, scopeID, window string) (Quota, error) {
	row := s.db.QueryRow(
		`SELECT scope_kind, scope_id, window, max_requests, max_tokens, max_usd_micros
		 FROM quotas WHERE scope_kind = ? AND scope_id = ? AND window = ?`,
		scopeKind, scopeID, window,
	)
	return scanQuota(row)
}

// ListQuotasForScope returns every window-row owned by (scopeKind, scopeID).
// Used by the quota middleware to load all of a team's limits at once, and
// by MCP tooling to render a quota summary.
func (s *Store) ListQuotasForScope(scopeKind, scopeID string) ([]Quota, error) {
	rows, err := s.db.Query(
		`SELECT scope_kind, scope_id, window, max_requests, max_tokens, max_usd_micros
		 FROM quotas WHERE scope_kind = ? AND scope_id = ? ORDER BY window ASC`,
		scopeKind, scopeID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Quota
	for rows.Next() {
		q, err := scanQuota(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (s *Store) DeleteQuota(scopeKind, scopeID, window string) error {
	res, err := s.db.Exec(
		`DELETE FROM quotas WHERE scope_kind = ? AND scope_id = ? AND window = ?`,
		scopeKind, scopeID, window,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanQuota(r rowScanner) (Quota, error) {
	var q Quota
	var maxReq, maxTok, maxUSD sql.NullInt64
	err := r.Scan(&q.ScopeKind, &q.ScopeID, &q.Window, &maxReq, &maxTok, &maxUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return Quota{}, ErrNotFound
	}
	if err != nil {
		return Quota{}, err
	}
	if maxReq.Valid {
		v := maxReq.Int64
		q.MaxRequests = &v
	}
	if maxTok.Valid {
		v := maxTok.Int64
		q.MaxTokens = &v
	}
	if maxUSD.Valid {
		v := maxUSD.Int64
		q.MaxUSDMicros = &v
	}
	return q, nil
}

// nullInt64 maps *int64 (nil = "unlimited") to a SQL value (NULL or the
// integer). Mirrors the `nullable` helper for strings in store.go.
func nullInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
