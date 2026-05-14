package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// teams.go + api_keys.go (combined here, in one file for now per the
// existing convention of co-locating CRUD on related domains). Covers:
//   - Team CRUD
//   - APIKey issue/verify/revoke/lookup/list/touch
//
// Both are introduced in T4 second half (Plan R3 FINAL). Provider /
// model_alias / request_log CRUD remains in store.go to keep churn small.

// Team mirrors a row in the teams table.
type Team struct {
	ID        string
	Slug      string
	Name      string
	CreatedAt time.Time
}

// slugRe enforces a lowercase, kebab/underscore-friendly slug between 2 and
// 64 chars. Restrictive on purpose: slugs land in MCP tool inputs and CLI
// flags, so any character that would force shell-quoting is rejected. If a
// caller needs richer naming they should use the Name field (free-form).
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

// AddTeam inserts a new team with a freshly-minted tm_<8 hex> ID. The slug
// must satisfy slugRe and be globally UNIQUE; the name is free-form.
func (s *Store) AddTeam(slug, name string) (Team, error) {
	if !slugRe.MatchString(slug) {
		return Team{}, fmt.Errorf("store: invalid slug %q (want %s)", slug, slugRe.String())
	}
	if strings.TrimSpace(name) == "" {
		return Team{}, fmt.Errorf("store: team name must not be empty")
	}
	id, err := newID("tm")
	if err != nil {
		return Team{}, err
	}
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(
		`INSERT INTO teams (id, slug, name, created_at) VALUES (?, ?, ?, ?)`,
		id, slug, name, now,
	); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return Team{}, ErrDuplicate
		}
		return Team{}, err
	}
	return Team{ID: id, Slug: slug, Name: name, CreatedAt: time.UnixMilli(now)}, nil
}

func (s *Store) GetTeam(id string) (Team, error) {
	row := s.db.QueryRow(
		`SELECT id, slug, name, created_at FROM teams WHERE id = ?`, id,
	)
	return scanTeam(row)
}

func (s *Store) GetTeamBySlug(slug string) (Team, error) {
	row := s.db.QueryRow(
		`SELECT id, slug, name, created_at FROM teams WHERE slug = ?`, slug,
	)
	return scanTeam(row)
}

func (s *Store) ListTeams() ([]Team, error) {
	rows, err := s.db.Query(
		`SELECT id, slug, name, created_at FROM teams ORDER BY slug ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Team
	for rows.Next() {
		t, err := scanTeam(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RemoveTeam fails with ErrNotFound if id is unknown. If any api_keys still
// reference the team, the FK is ON DELETE RESTRICT (ENG-F11) so SQLite
// returns a constraint error — surfaced to the caller verbatim.
func (s *Store) RemoveTeam(id string) error {
	res, err := s.db.Exec(`DELETE FROM teams WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanTeam(r rowScanner) (Team, error) {
	var t Team
	var createdAt int64
	err := r.Scan(&t.ID, &t.Slug, &t.Name, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Team{}, ErrNotFound
	}
	if err != nil {
		return Team{}, err
	}
	t.CreatedAt = time.UnixMilli(createdAt)
	return t, nil
}

// --- APIKey ---

// APIKey mirrors a row in api_keys. The Hash column is the bcrypt digest of
// the full plaintext token (cost 10). Prefix is the leading 20 chars
// ("lgw_" + 16 base32 chars) and is stored for O(1) lookup; the rest of the
// plaintext is unrecoverable post-issuance.
type APIKey struct {
	ID              string
	TeamID          string
	Hash            string
	Prefix          string
	CreatedForLabel string
	Scope           string
	RevokedAt       *time.Time
	ExpiresAt       *time.Time
	LastUsedAt      *time.Time
	CreatedAt       time.Time
}

// validScopes mirrors the CHECK constraint on api_keys.scope. Pre-validating
// at the Go layer gives a clearer error than the SQLite "CHECK constraint
// failed" string would surface.
var validScopes = map[string]bool{
	"inbound":     true,
	"mcp_super":   true,
	"mcp_admin":   true,
	"mcp_auditor": true,
	"mcp_billing": true,
}

// bcryptCost matches plan §RBAC bcrypt cost=10 (~100ms verify on commodity
// CPU). High enough to be hostile to offline attacks if state.db leaks;
// low enough to keep auth latency tolerable.
const bcryptCost = 10

// IssueAPIKey mints a new lgw_<16char-prefix><32char-secret> token, stores
// its bcrypt hash + prefix, and returns the plaintext ONCE. Plaintext is
// not retrievable afterward — losing it means revoke + reissue. The 16-char
// prefix is collision-checked via UNIQUE INDEX (ENG-F5); on the
// astronomically unlikely UNIQUE violation we regenerate up to 8 times
// before surfacing the error.
//
// Scope must be one of the 5-tier enum; team must exist (FK NOT NULL).
func (s *Store) IssueAPIKey(teamID, scope, label string) (APIKey, string, error) {
	if !validScopes[scope] {
		return APIKey{}, "", fmt.Errorf("store: invalid scope %q (want one of inbound/mcp_super/mcp_admin/mcp_auditor/mcp_billing)", scope)
	}
	id, err := newID("ak")
	if err != nil {
		return APIKey{}, "", err
	}

	now := time.Now().UnixMilli()
	const maxAttempts = 8
	for attempt := 0; attempt < maxAttempts; attempt++ {
		prefix, secret, err := newTokenParts()
		if err != nil {
			return APIKey{}, "", err
		}
		plaintext := prefix + secret
		hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcryptCost)
		if err != nil {
			return APIKey{}, "", fmt.Errorf("bcrypt: %w", err)
		}
		_, err = s.db.Exec(
			`INSERT INTO api_keys
				(id, team_id, hash, prefix, created_for_label, scope, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, teamID, string(hash), prefix, nullable(label), scope, now,
		)
		if err == nil {
			return APIKey{
				ID: id, TeamID: teamID, Hash: string(hash), Prefix: prefix,
				CreatedForLabel: label, Scope: scope,
				CreatedAt: time.UnixMilli(now),
			}, plaintext, nil
		}
		msg := err.Error()
		// Prefix collision (ENG-F5): regenerate and retry.
		if strings.Contains(msg, "UNIQUE constraint failed: api_keys.prefix") {
			continue
		}
		// FK violation (team doesn't exist) / scope CHECK / anything else:
		// surface to caller as-is.
		return APIKey{}, "", err
	}
	return APIKey{}, "", errors.New("store: prefix collision after 8 attempts (entropy source broken?)")
}

// LookupAPIKeyByPrefix returns the unrevoked, unexpired key matching prefix,
// or ErrNotFound. Used by the inbound auth path before bcrypt verification.
// Revoked or expired rows are filtered here so callers don't have to re-check.
func (s *Store) LookupAPIKeyByPrefix(prefix string) (APIKey, error) {
	row := s.db.QueryRow(
		`SELECT id, team_id, hash, prefix, created_for_label, scope,
			revoked_at, expires_at, last_used_at, created_at
			FROM api_keys
			WHERE prefix = ?
			  AND revoked_at IS NULL
			  AND (expires_at IS NULL OR expires_at > ?)`,
		prefix, time.Now().UnixMilli(),
	)
	return scanAPIKey(row)
}

// GetAPIKey returns the row by primary key id regardless of revoked/expired
// state. Used by MCP admin tools and audit views.
func (s *Store) GetAPIKey(id string) (APIKey, error) {
	row := s.db.QueryRow(
		`SELECT id, team_id, hash, prefix, created_for_label, scope,
			revoked_at, expires_at, last_used_at, created_at
			FROM api_keys WHERE id = ?`,
		id,
	)
	return scanAPIKey(row)
}

// VerifyAPIKey is the canonical auth chokepoint: looks up by prefix then
// bcrypt-compares the full plaintext. Returns ErrNotFound for any failure
// mode (unknown prefix, revoked, expired, hash mismatch) so the caller
// cannot distinguish "unknown key" from "wrong secret" via timing.
//
// Constant-time bcrypt compare is provided by golang.org/x/crypto/bcrypt.
func (s *Store) VerifyAPIKey(plaintext string) (APIKey, error) {
	if len(plaintext) < 20 || !strings.HasPrefix(plaintext, "lgw_") {
		return APIKey{}, ErrNotFound
	}
	prefix := plaintext[:20]
	ak, err := s.LookupAPIKeyByPrefix(prefix)
	if err != nil {
		return APIKey{}, ErrNotFound
	}
	if err := bcrypt.CompareHashAndPassword([]byte(ak.Hash), []byte(plaintext)); err != nil {
		return APIKey{}, ErrNotFound
	}
	return ak, nil
}

// RevokeAPIKey stamps revoked_at = now(). Idempotent: re-revoking an already-
// revoked key updates the timestamp without erroring. Missing id → ErrNotFound.
func (s *Store) RevokeAPIKey(id string) error {
	now := time.Now().UnixMilli()
	res, err := s.db.Exec(
		`UPDATE api_keys SET revoked_at = ? WHERE id = ?`,
		now, id,
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

func (s *Store) ListAPIKeysByTeam(teamID string) ([]APIKey, error) {
	rows, err := s.db.Query(
		`SELECT id, team_id, hash, prefix, created_for_label, scope,
			revoked_at, expires_at, last_used_at, created_at
			FROM api_keys WHERE team_id = ? ORDER BY created_at ASC`,
		teamID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// TouchAPIKeyLastUsed records when a key was last successfully presented
// to the inbound path. Best-effort: errors are returned but the auth path
// can ignore them (telemetry only).
func (s *Store) TouchAPIKeyLastUsed(id string, when time.Time) error {
	res, err := s.db.Exec(
		`UPDATE api_keys SET last_used_at = ? WHERE id = ?`,
		when.UnixMilli(), id,
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

func scanAPIKey(r rowScanner) (APIKey, error) {
	var k APIKey
	var label sql.NullString
	var revoked, expires, lastUsed sql.NullInt64
	var createdAt int64
	err := r.Scan(
		&k.ID, &k.TeamID, &k.Hash, &k.Prefix, &label, &k.Scope,
		&revoked, &expires, &lastUsed, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, err
	}
	k.CreatedForLabel = label.String
	k.CreatedAt = time.UnixMilli(createdAt)
	if revoked.Valid {
		t := time.UnixMilli(revoked.Int64)
		k.RevokedAt = &t
	}
	if expires.Valid {
		t := time.UnixMilli(expires.Int64)
		k.ExpiresAt = &t
	}
	if lastUsed.Valid {
		t := time.UnixMilli(lastUsed.Int64)
		k.LastUsedAt = &t
	}
	return k, nil
}

// --- ID + token helpers ---

// newID returns "<prefix>_<8 hex>" using crypto/rand. 32 bits of entropy
// gives ~65k IDs at 50% collision probability — overkill for teams/keys
// which are scoped to a single gateway instance.
func newID(prefix string) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// newTokenParts splits the token into the stored prefix and the unrecoverable
// secret tail. The prefix is 20 chars total ("lgw_" + 16 base32 chars), the
// secret is another 32 base32 chars. Combined entropy ≈ 80 bits in prefix +
// 160 bits in secret = 240-bit token, which keeps bcrypt's effective cost
// limit (72-byte ceiling) honoured.
func newTokenParts() (prefix, secret string, err error) {
	// 16 base32 chars = 10 bytes of entropy
	var pb [10]byte
	if _, err := rand.Read(pb[:]); err != nil {
		return "", "", err
	}
	// 32 base32 chars = 20 bytes
	var sb [20]byte
	if _, err := rand.Read(sb[:]); err != nil {
		return "", "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	prefix = "lgw_" + strings.ToLower(enc.EncodeToString(pb[:]))
	secret = strings.ToLower(enc.EncodeToString(sb[:]))
	return prefix, secret, nil
}
