package store

import "database/sql"

// FallbackPolicy configures how the gateway responds to a trigger condition
// on a particular provider response. TeamID="" means global (applies to all
// teams unless overridden by a team-scoped policy for the same trigger).
type FallbackPolicy struct {
	ID             int64
	TeamID         string // "" = global
	Trigger        string // quota_exhausted|http_5xx|http_429|latency_exceeded
	ThresholdMs    int    // for latency_exceeded; ignored for other triggers
	Action         string // next_best|specific_provider
	TargetProvider string // for action=specific_provider
	MaxChainDepth  int    // max fallback hops (default 3)
}

// SetFallbackPolicy upserts a fallback policy for (team_id, trigger). Inserts
// if no row exists for the pair, updates otherwise. Returns the row id.
func (s *Store) SetFallbackPolicy(p FallbackPolicy) (int64, error) {
	if p.MaxChainDepth <= 0 {
		p.MaxChainDepth = 3
	}
	res, err := s.db.Exec(`
		INSERT INTO fallback_policies
		  (team_id, trigger, threshold_ms, action, target_provider, max_chain_depth)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(team_id, trigger) DO UPDATE SET
		  threshold_ms    = excluded.threshold_ms,
		  action          = excluded.action,
		  target_provider = excluded.target_provider,
		  max_chain_depth = excluded.max_chain_depth`,
		p.TeamID, p.Trigger, p.ThresholdMs, p.Action, p.TargetProvider, p.MaxChainDepth,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RemoveFallbackPolicy deletes a policy by id.
func (s *Store) RemoveFallbackPolicy(id int64) error {
	res, err := s.db.Exec(`DELETE FROM fallback_policies WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListFallbackPolicies returns all policies relevant to teamID: team-scoped
// policies for teamID plus global policies, ordered by id.
func (s *Store) ListFallbackPolicies(teamID string) ([]FallbackPolicy, error) {
	rows, err := s.db.Query(`
		SELECT id, team_id, trigger, threshold_ms, action, target_provider, max_chain_depth
		FROM fallback_policies
		WHERE team_id = ? OR team_id = ''
		ORDER BY id`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FallbackPolicy
	for rows.Next() {
		var p FallbackPolicy
		if err := rows.Scan(&p.ID, &p.TeamID, &p.Trigger, &p.ThresholdMs,
			&p.Action, &p.TargetProvider, &p.MaxChainDepth); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetEffectiveFallbackPolicy looks up the active policy for (trigger, teamID).
// Team-scoped policy wins over global. Returns (policy, true, nil) when found,
// (zero, false, nil) when no policy is configured.
func (s *Store) GetEffectiveFallbackPolicy(trigger, teamID string) (FallbackPolicy, bool, error) {
	// Try team-scoped first, then global.
	row := s.db.QueryRow(`
		SELECT id, team_id, trigger, threshold_ms, action, target_provider, max_chain_depth
		FROM fallback_policies
		WHERE trigger = ? AND (team_id = ? OR team_id = '')
		ORDER BY CASE WHEN team_id != '' THEN 0 ELSE 1 END
		LIMIT 1`, trigger, teamID)
	var p FallbackPolicy
	err := row.Scan(&p.ID, &p.TeamID, &p.Trigger, &p.ThresholdMs,
		&p.Action, &p.TargetProvider, &p.MaxChainDepth)
	if err != nil {
		if err == sql.ErrNoRows {
			return FallbackPolicy{}, false, nil
		}
		return FallbackPolicy{}, false, err
	}
	return p, true, nil
}
