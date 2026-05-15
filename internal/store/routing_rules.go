package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// RoutingRule mirrors a row in the routing_rules table.
type RoutingRule struct {
	ID            int64
	TeamID        string // empty = global rule
	Priority      int
	MatchField    string // "model" | "kind"
	MatchOp       string // "prefix" | "equals" | "regex"
	MatchValue    string
	ProviderName  string
	UpstreamModel string // empty = use client model verbatim
	IsActive      bool
	CreatedAt     time.Time
}

// SetRoutingRule inserts a new routing rule or updates an existing one.
// If rule.ID > 0 the existing row is updated; otherwise a new row is inserted.
func (s *Store) SetRoutingRule(rule RoutingRule) (RoutingRule, error) {
	now := time.Now().UnixMilli()

	if rule.MatchField == "" {
		rule.MatchField = "model"
	}
	if rule.MatchOp == "" {
		rule.MatchOp = "prefix"
	}

	if rule.ID > 0 {
		_, err := s.db.Exec(`UPDATE routing_rules
			SET team_id=?, priority=?, match_field=?, match_op=?, match_value=?,
			    provider_name=?, upstream_model=?, is_active=?
			WHERE id=?`,
			nullable(rule.TeamID), rule.Priority, rule.MatchField, rule.MatchOp,
			rule.MatchValue, rule.ProviderName, nullable(rule.UpstreamModel),
			boolInt(rule.IsActive), rule.ID)
		if err != nil {
			return rule, fmt.Errorf("store: update routing rule: %w", err)
		}
		return rule, nil
	}

	res, err := s.db.Exec(`INSERT INTO routing_rules
		(team_id, priority, match_field, match_op, match_value,
		 provider_name, upstream_model, is_active, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullable(rule.TeamID), rule.Priority, rule.MatchField, rule.MatchOp,
		rule.MatchValue, rule.ProviderName, nullable(rule.UpstreamModel),
		boolInt(rule.IsActive), now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") ||
			strings.Contains(err.Error(), "FOREIGN KEY") {
			return rule, fmt.Errorf("store: insert routing rule: %w", err)
		}
		return rule, fmt.Errorf("store: insert routing rule: %w", err)
	}
	rule.ID, _ = res.LastInsertId()
	rule.CreatedAt = time.UnixMilli(now)
	return rule, nil
}

// ListRoutingRules returns routing rules filtered by teamID.
// Pass empty teamID to list global rules only; pass "all" to list all rules.
func (s *Store) ListRoutingRules(teamID string) ([]RoutingRule, error) {
	var query string
	var args []any

	if teamID == "all" {
		query = `SELECT id, team_id, priority, match_field, match_op, match_value,
			provider_name, upstream_model, is_active, created_at
			FROM routing_rules ORDER BY priority ASC, id ASC`
	} else if teamID == "" {
		query = `SELECT id, team_id, priority, match_field, match_op, match_value,
			provider_name, upstream_model, is_active, created_at
			FROM routing_rules WHERE team_id IS NULL ORDER BY priority ASC, id ASC`
	} else {
		query = `SELECT id, team_id, priority, match_field, match_op, match_value,
			provider_name, upstream_model, is_active, created_at
			FROM routing_rules WHERE team_id = ? ORDER BY priority ASC, id ASC`
		args = append(args, teamID)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RoutingRule
	for rows.Next() {
		r, err := scanRoutingRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRoutingRule removes a routing rule by ID.
func (s *Store) DeleteRoutingRule(id int64) error {
	res, err := s.db.Exec(`DELETE FROM routing_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MatchRoutingRules returns active routing rules matching the given teamID,
// ordered by priority ASC. Used by the router to find applicable rules.
func (s *Store) MatchRoutingRules(teamID string) ([]RoutingRule, error) {
	query := `SELECT id, team_id, priority, match_field, match_op, match_value,
		provider_name, upstream_model, is_active, created_at
		FROM routing_rules
		WHERE is_active = 1 AND (team_id IS NULL OR team_id = ?)
		ORDER BY priority ASC, id ASC`
	rows, err := s.db.Query(query, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RoutingRule
	for rows.Next() {
		r, err := scanRoutingRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanRoutingRule(r rowScanner) (RoutingRule, error) {
	var rule RoutingRule
	var teamID, upstreamModel sql.NullString
	var isActive, createdAt int64

	err := r.Scan(&rule.ID, &teamID, &rule.Priority, &rule.MatchField,
		&rule.MatchOp, &rule.MatchValue, &rule.ProviderName,
		&upstreamModel, &isActive, &createdAt)
	if err != nil {
		return RoutingRule{}, err
	}

	rule.TeamID = teamID.String
	rule.UpstreamModel = upstreamModel.String
	rule.IsActive = isActive == 1
	rule.CreatedAt = time.UnixMilli(createdAt)
	return rule, nil
}
