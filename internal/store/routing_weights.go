package store

import (
	"database/sql"
	"errors"
	"time"
)

// RoutingWeights configures how the cognitive router scores providers for a
// given team. Values are the v0.3 T2 dimensions: cost, latency, quality,
// health. Per issue #11, weights are configurable per team with the
// defaults below applying when no row exists.
type RoutingWeights struct {
	TeamID  string
	Cost    float64
	Latency float64
	Quality float64
	Health  float64
}

// Default routing weights (issue #11 acceptance).
const (
	DefaultRoutingWeightCost    = 0.4
	DefaultRoutingWeightLatency = 0.3
	DefaultRoutingWeightQuality = 0.2
	DefaultRoutingWeightHealth  = 0.1
)

func defaultRoutingWeights(teamID string) RoutingWeights {
	return RoutingWeights{
		TeamID:  teamID,
		Cost:    DefaultRoutingWeightCost,
		Latency: DefaultRoutingWeightLatency,
		Quality: DefaultRoutingWeightQuality,
		Health:  DefaultRoutingWeightHealth,
	}
}

// GetRoutingWeights returns the per-team weights, or the v0.3 defaults when
// no row is present. The caller (router) always gets usable weights — no
// "not found" branch to handle.
func (s *Store) GetRoutingWeights(teamID string) (RoutingWeights, error) {
	row := s.db.QueryRow(`SELECT cost, latency, quality, health
		FROM routing_weights WHERE team_id = ?`, teamID)
	w := RoutingWeights{TeamID: teamID}
	err := row.Scan(&w.Cost, &w.Latency, &w.Quality, &w.Health)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultRoutingWeights(teamID), nil
	}
	if err != nil {
		return RoutingWeights{}, err
	}
	return w, nil
}

// SetRoutingWeights upserts the per-team weights. Weight-change is observed
// on the next routing decision (no cache, no restart) per issue #11.
func (s *Store) SetRoutingWeights(w RoutingWeights) error {
	_, err := s.db.Exec(`INSERT INTO routing_weights
		(team_id, cost, latency, quality, health, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(team_id) DO UPDATE SET
		  cost = excluded.cost,
		  latency = excluded.latency,
		  quality = excluded.quality,
		  health = excluded.health,
		  updated_at = excluded.updated_at`,
		w.TeamID, w.Cost, w.Latency, w.Quality, w.Health, time.Now().UnixMilli())
	return err
}
