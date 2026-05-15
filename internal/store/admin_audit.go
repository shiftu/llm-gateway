package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
}

// LogAdminAction appends an audit entry. Always succeeds (best-effort).
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

	now := time.Now().UnixMilli()
	_, err := s.db.Exec(`INSERT INTO admin_audit (ts, api_key_id, team_id, action, target_type, target_id, detail, ip_address)
		VALUES (?, NULL, NULL, ?, ?, ?, ?, '')`,
		now, action, targetType, targetID, detailJSON)
	if err != nil {
		return fmt.Errorf("store: log admin action: %w", err)
	}
	return nil
}

// AuditLogFilter defines query parameters for listing audit logs.
type AuditLogFilter struct {
	Since       time.Time // only rows after this timestamp
	Action      string    // filter by action name (exact match)
	TargetType  string    // filter by target type
	Limit       int       // max rows to return (default 100)
	Offset      int       // pagination offset
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

	query := `SELECT id, ts, api_key_id, team_id, action, target_type, target_id, detail, ip_address
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
		if err := rows.Scan(&a.ID, &tsMs, &apiKeyID, &teamID, &a.Action,
			&targetType, &targetID, &detail, &ipAddr); err != nil {
			return nil, err
		}
		a.Ts = time.UnixMilli(tsMs)
		a.APIKeyID = apiKeyID.String
		a.TeamID = teamID.String
		a.TargetType = targetType.String
		a.TargetID = targetID.String
		a.Detail = detail.String
		a.IPAddress = ipAddr.String
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

// joinConds joins condition strings with sep.
func joinConds(conds []string, sep string) string {
	result := conds[0]
	for _, c := range conds[1:] {
		result += sep + c
	}
	return result
}
