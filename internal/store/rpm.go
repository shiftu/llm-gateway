package store

import (
	"fmt"
)

// rpm_buckets stores per-minute RPM counters when LLM_GATEWAY_PERSIST_RPM=1.
// This table is separate from usage_counters to avoid polluting day/month
// aggregates.

// GetAndIncrementRPM atomically increments the request counter for the given
// (api_key_id, minute) bucket and returns the NEW count after increment.
//
// It uses a single UPSERT:
//   - If no row exists → INSERT with request_count=1
//   - If row exists    → UPDATE request_count = request_count + 1
//
// The caller compares the returned count against maxRequests to decide
// whether to reject.
func (s *Store) GetAndIncrementRPM(keyID string, nowMinute int64) (int64, error) {
	var count int64
	err := s.db.QueryRow(`
		INSERT INTO rpm_buckets (api_key_id, minute_utc, request_count)
		VALUES (?, ?, 1)
		ON CONFLICT(api_key_id, minute_utc) DO UPDATE SET request_count = request_count + 1
		RETURNING request_count`,
		keyID, nowMinute,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: get-and-increment RPM: %w", err)
	}
	return count, nil
}

// GetRPMCount reads the current RPM count for the given (keyID, minute)
// bucket without incrementing. Returns 0 if no bucket exists.
func (s *Store) GetRPMCount(keyID string, nowMinute int64) (int64, error) {
	var count int64
	err := s.db.QueryRow(
		`SELECT request_count FROM rpm_buckets WHERE api_key_id = ? AND minute_utc = ?`,
		keyID, nowMinute,
	).Scan(&count)
	if err != nil {
		return 0, nil // no row = 0
	}
	return count, nil
}
