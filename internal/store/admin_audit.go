package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AdminAudit mirrors a row in the admin_audit table (immutable append-only).
type AdminAudit struct {
	ID         int64
	Ts         time.Time
	APIKeyID   string
	TeamID     string
	Action     string
	TargetType string
	TargetID   string
	Detail     string // JSON blob
	IPAddress  string
	PrevHash   string // SHA-256 of the previous entry (empty for first row)
	EntryHash  string // SHA-256 of this entry's fields
}

// computeAuditHash returns SHA-256(prevHash + tsMs + action + targetType + targetID + detail).
// All fields joined with "|" as separator.
func computeAuditHash(prevHash string, tsMs int64, action, targetType, targetID, detail string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%s|%s|%s|%s", prevHash, tsMs, action, targetType, targetID, detail)
	return hex.EncodeToString(h.Sum(nil))
}

// latestAuditHash returns the entry_hash of the most recent row WHERE entry_hash IS NOT NULL.
// Returns "" if no hashed rows exist yet.
func latestAuditHash(tx *sql.Tx) string {
	var h string
	err := tx.QueryRow(`SELECT entry_hash FROM admin_audit WHERE entry_hash IS NOT NULL ORDER BY id DESC LIMIT 1`).Scan(&h)
	if err != nil {
		return ""
	}
	return h
}

// LogAdminAction appends an audit entry with a chained SHA-256 hash. Always succeeds (best-effort).
func (s *Store) LogAdminAction(action, targetType, targetID string, detail any) error {
	var detailJSON string
	if detail != nil {
		b, err := json.Marshal(detail)
		if err != nil {
			detailJSON = fmt.Sprintf(`{"error":"marshal failed: %v"}`, err)
		} else {
			detailJSON = string(b)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: log admin action begin tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()
	prevHash := latestAuditHash(tx)
	entryHash := computeAuditHash(prevHash, now, action, targetType, targetID, detailJSON)

	_, err = tx.Exec(`INSERT INTO admin_audit (ts, api_key_id, team_id, action, target_type, target_id, detail, ip_address, prev_hash, entry_hash)
		VALUES (?, NULL, NULL, ?, ?, ?, ?, '', ?, ?)`,
		now, action, targetType, targetID, detailJSON, nullable(prevHash), entryHash)
	if err != nil {
		return fmt.Errorf("store: log admin action: %w", err)
	}
	return tx.Commit()
}

// AuditLogFilter defines query parameters for listing audit logs.
type AuditLogFilter struct {
	Since      time.Time // only rows after this timestamp
	Action     string    // filter by action name (exact match)
	TargetType string    // filter by target type
	Limit      int       // max rows to return (default 100)
	Offset     int       // pagination offset
}

// ListAdminAuditLogs returns audit entries matching the filter, newest first.
func (s *Store) ListAdminAuditLogs(f AuditLogFilter) ([]AdminAudit, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}

	var conditions []string
	var args []any

	if !f.Since.IsZero() {
		conditions = append(conditions, "ts >= ?")
		args = append(args, f.Since.UnixMilli())
	}
	if f.Action != "" {
		conditions = append(conditions, "action = ?")
		args = append(args, f.Action)
	}
	if f.TargetType != "" {
		conditions = append(conditions, "target_type = ?")
		args = append(args, f.TargetType)
	}

	query := `SELECT id, ts, api_key_id, team_id, action, target_type, target_id, detail, ip_address, prev_hash, entry_hash
		FROM admin_audit`
	if len(conditions) > 0 {
		query += " WHERE " + joinConds(conditions, " AND ")
	}
	query += " ORDER BY ts DESC LIMIT ? OFFSET ?"
	args = append(args, f.Limit, f.Offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AdminAudit
	for rows.Next() {
		var a AdminAudit
		var tsMs int64
		var apiKeyID, teamID, targetType, targetID, detail, ipAddr sql.NullString
		var prevHash, entryHash sql.NullString
		if err := rows.Scan(&a.ID, &tsMs, &apiKeyID, &teamID, &a.Action,
			&targetType, &targetID, &detail, &ipAddr, &prevHash, &entryHash); err != nil {
			return nil, err
		}
		a.Ts = time.UnixMilli(tsMs)
		a.APIKeyID = apiKeyID.String
		a.TeamID = teamID.String
		a.TargetType = targetType.String
		a.TargetID = targetID.String
		a.Detail = detail.String
		a.IPAddress = ipAddr.String
		a.PrevHash = prevHash.String
		a.EntryHash = entryHash.String
		out = append(out, a)
	}
	return out, rows.Err()
}

// PruneAuditLog deletes audit entries older than beforeDays days.
// Returns the number of deleted rows.
func (s *Store) PruneAuditLog(beforeDays int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -beforeDays).UnixMilli()
	res, err := s.db.Exec(`DELETE FROM admin_audit WHERE ts < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// AuditChainResult summarises a VerifyAuditChain run.
type AuditChainResult struct {
	TotalRows    int   `json:"total_rows"`
	HashedRows   int   `json:"hashed_rows"`
	InvalidRows  int   `json:"invalid_rows"`
	FirstInvalid int64 `json:"first_invalid_id,omitempty"` // row ID of first bad entry
	OK           bool  `json:"ok"`
}

// VerifyAuditChain reads all hashed rows in ascending ID order, recomputes each
// hash, and compares it to the stored entry_hash. Returns counts and OK status.
func (s *Store) VerifyAuditChain() (AuditChainResult, error) {
	// Count total rows.
	var totalRows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM admin_audit`).Scan(&totalRows); err != nil {
		return AuditChainResult{}, fmt.Errorf("store: verify audit chain count: %w", err)
	}

	rows, err := s.db.Query(`SELECT id, ts, action, target_type, target_id, detail, prev_hash, entry_hash
		FROM admin_audit WHERE entry_hash IS NOT NULL ORDER BY id ASC`)
	if err != nil {
		return AuditChainResult{}, fmt.Errorf("store: verify audit chain query: %w", err)
	}
	defer rows.Close()

	var res AuditChainResult
	res.TotalRows = totalRows

	for rows.Next() {
		var id, tsMs int64
		var action, storedEntryHash string
		var targetType, targetID, detail, prevHash sql.NullString
		if err := rows.Scan(&id, &tsMs, &action, &targetType, &targetID, &detail, &prevHash, &storedEntryHash); err != nil {
			return AuditChainResult{}, fmt.Errorf("store: verify audit chain scan: %w", err)
		}
		res.HashedRows++

		computed := computeAuditHash(prevHash.String, tsMs, action, targetType.String, targetID.String, detail.String)
		if computed != storedEntryHash {
			res.InvalidRows++
			if res.FirstInvalid == 0 {
				res.FirstInvalid = id
			}
		}
	}
	if err := rows.Err(); err != nil {
		return AuditChainResult{}, fmt.Errorf("store: verify audit chain rows: %w", err)
	}

	res.OK = res.InvalidRows == 0
	return res, nil
}

// joinConds joins condition strings with sep.
func joinConds(conds []string, sep string) string {
	return strings.Join(conds, sep)
}
