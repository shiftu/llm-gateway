// Package store wraps the SQLite database that holds providers, aliases and
// request_logs per plan §2 (Revision 2 schema). Schema versioning via
// PRAGMA user_version + an ordered migrations slice; idempotent re-apply
// for safe restarts (plan F-9).
//
// DSN enables WAL, busy_timeout=5s, foreign_keys=ON, synchronous=NORMAL
// (plan F-4) so concurrent log writes from the HTTP path don't collide
// with MCP CRUD, and ON DELETE CASCADE works on aliases when a provider
// is removed.
//
// API keys are stored plaintext in v0.1 (file-permission-protected via
// the parent token file's 0600 dir). Encryption-at-rest lands in Task 4.5
// per CEO review A-3.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	schemaVersion           = 2
	DefaultAnthropicVersion = "2023-06-01"
)

var (
	ErrNotFound  = errors.New("store: not found")
	ErrDuplicate = errors.New("store: already exists")
)

// Store is the gateway's persistence root. Hold one per process.
type Store struct {
	db *sql.DB
}

// Open accepts a filesystem path or ":memory:". Applies migrations on
// success. Caller is responsible for Close.
//
// For ":memory:" we pin MaxOpenConns=1 because modernc.org/sqlite (like
// every other SQLite binding) gives each pooled connection its OWN in-mem
// database — so a second concurrent connection would observe an empty
// schema. Production (file-backed WAL) uses the default pool and relies on
// busy_timeout + WAL for concurrent writers.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err := applyMigrations(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) DB() *sql.DB  { return s.db }

func buildDSN(path string) string {
	pragmas := []string{
		"_pragma=foreign_keys(1)",
		"_pragma=journal_mode(WAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=synchronous(NORMAL)",
	}
	return path + "?" + strings.Join(pragmas, "&")
}

// migrations[i] takes the schema from version i to version i+1.
var migrations = []string{
	// v0 -> v1: initial schema
	`
	CREATE TABLE providers (
	  name              TEXT PRIMARY KEY,
	  kind              TEXT NOT NULL,
	  openai_base_url   TEXT,
	  anthropic_base_url TEXT,
	  api_key           TEXT NOT NULL,
	  anthropic_version TEXT NOT NULL DEFAULT '2023-06-01',
	  is_default        INTEGER NOT NULL DEFAULT 0,
	  created_at        INTEGER NOT NULL,
	  CHECK (openai_base_url IS NOT NULL OR anthropic_base_url IS NOT NULL)
	);
	CREATE TABLE model_aliases (
	  alias          TEXT PRIMARY KEY,
	  provider_name  TEXT NOT NULL REFERENCES providers(name) ON DELETE CASCADE,
	  upstream_model TEXT NOT NULL,
	  created_at     INTEGER NOT NULL
	);
	CREATE TABLE request_logs (
	  id                INTEGER PRIMARY KEY AUTOINCREMENT,
	  ts                INTEGER NOT NULL,
	  client_model      TEXT NOT NULL,
	  resolved_model    TEXT NOT NULL,
	  provider_name     TEXT NOT NULL,
	  prompt_tokens     INTEGER,
	  completion_tokens INTEGER,
	  total_tokens      INTEGER,
	  latency_ms        INTEGER,
	  status            TEXT NOT NULL,
	  error_msg         TEXT,
	  prompt_excerpt    TEXT
	);
	CREATE INDEX idx_logs_ts ON request_logs(ts DESC);
	CREATE INDEX idx_logs_provider ON request_logs(provider_name);
	`,
	// v1 -> v2: multi-tenancy (Plan R3 FINAL — halt+redo to multi-tenant v0.1)
	// Adds: teams, api_keys, usage_counters, quotas, model_costs.
	// Extends: providers (team_id, fallback_eligible) + request_logs
	//   (api_key_id, team_id, provider_owner_team_id, cost_usd_micros,
	//    input_tokens, output_tokens, reasoning_tokens).
	// Recreates: model_aliases with composite PK (alias, team_id) — SQLite
	//   cannot ALTER PK, so this uses CREATE _v2 → copy → DROP → RENAME
	//   per Eng review F-R3-ENG-9.
	`
	CREATE TABLE teams (
	  id          TEXT PRIMARY KEY,
	  slug        TEXT NOT NULL UNIQUE,
	  name        TEXT NOT NULL,
	  created_at  INTEGER NOT NULL
	);

	CREATE TABLE api_keys (
	  id                 TEXT PRIMARY KEY,
	  team_id            TEXT NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
	  hash               TEXT NOT NULL,
	  prefix             TEXT NOT NULL,
	  created_for_label  TEXT,
	  scope              TEXT NOT NULL,
	  revoked_at         INTEGER,
	  expires_at         INTEGER,
	  last_used_at       INTEGER,
	  created_at         INTEGER NOT NULL,
	  CHECK (scope IN ('inbound','mcp_super','mcp_admin','mcp_auditor','mcp_billing'))
	);
	CREATE UNIQUE INDEX idx_api_keys_prefix ON api_keys(prefix);
	CREATE INDEX idx_api_keys_team ON api_keys(team_id);

	CREATE TABLE usage_counters (
	  api_key_id       TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
	  day_utc          INTEGER NOT NULL,
	  request_count    INTEGER NOT NULL DEFAULT 0,
	  input_tokens     INTEGER NOT NULL DEFAULT 0,
	  output_tokens    INTEGER NOT NULL DEFAULT 0,
	  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
	  cost_usd_micros  INTEGER NOT NULL DEFAULT 0,
	  PRIMARY KEY (api_key_id, day_utc)
	);

	CREATE TABLE quotas (
	  scope_kind     TEXT NOT NULL,
	  scope_id       TEXT NOT NULL,
	  window         TEXT NOT NULL,
	  max_requests   INTEGER,
	  max_tokens     INTEGER,
	  max_usd_micros INTEGER,
	  PRIMARY KEY (scope_kind, scope_id, window),
	  CHECK (scope_kind IN ('team','key')),
	  CHECK (window IN ('month','day','minute'))
	);

	CREATE TABLE model_costs (
	  provider              TEXT NOT NULL,
	  model                 TEXT NOT NULL,
	  usd_per_input_1k      REAL NOT NULL,
	  usd_per_output_1k     REAL NOT NULL,
	  usd_per_reasoning_1k  REAL,
	  effective_from        INTEGER NOT NULL,
	  PRIMARY KEY (provider, model, effective_from)
	);

	ALTER TABLE providers ADD COLUMN team_id TEXT REFERENCES teams(id) ON DELETE CASCADE;
	ALTER TABLE providers ADD COLUMN fallback_eligible INTEGER NOT NULL DEFAULT 0;

	ALTER TABLE request_logs ADD COLUMN api_key_id TEXT REFERENCES api_keys(id) ON DELETE SET NULL;
	ALTER TABLE request_logs ADD COLUMN team_id TEXT;
	ALTER TABLE request_logs ADD COLUMN provider_owner_team_id TEXT;
	ALTER TABLE request_logs ADD COLUMN cost_usd_micros INTEGER;
	ALTER TABLE request_logs ADD COLUMN input_tokens INTEGER;
	ALTER TABLE request_logs ADD COLUMN output_tokens INTEGER;
	ALTER TABLE request_logs ADD COLUMN reasoning_tokens INTEGER;

	CREATE INDEX idx_logs_team ON request_logs(team_id);
	CREATE INDEX idx_logs_key ON request_logs(api_key_id);

	CREATE TABLE model_aliases_v2 (
	  alias          TEXT NOT NULL,
	  team_id        TEXT REFERENCES teams(id) ON DELETE CASCADE,
	  provider_name  TEXT NOT NULL REFERENCES providers(name) ON DELETE CASCADE,
	  upstream_model TEXT NOT NULL,
	  created_at     INTEGER NOT NULL,
	  PRIMARY KEY (alias, team_id)
	);
	INSERT INTO model_aliases_v2 (alias, team_id, provider_name, upstream_model, created_at)
	  SELECT alias, NULL, provider_name, upstream_model, created_at FROM model_aliases;
	DROP TABLE model_aliases;
	ALTER TABLE model_aliases_v2 RENAME TO model_aliases;
	CREATE UNIQUE INDEX idx_aliases_global_alias ON model_aliases(alias) WHERE team_id IS NULL
	`,
}

// SQLite has a long-standing quirk: NULL columns in a PRIMARY KEY are
// treated as distinct (against the SQL standard), so PRIMARY KEY (alias,
// team_id) does NOT prevent two rows with (alias='x', team_id=NULL). The
// partial UNIQUE INDEX above closes that gap for global aliases. Team-
// scoped rows (team_id IS NOT NULL) are covered by the composite PK
// directly. SetAlias's upsert uses ON CONFLICT(alias) WHERE team_id IS
// NULL to target the partial index when inserting a global alias.

// applyMigrations brings the DB schema up to schemaVersion in a single
// transaction (Eng review F-R3-ENG-8). A failure mid-migration rolls back
// cleanly so the schema never lands in a partial v(n) → v(n+1) state.
// Idempotent: if user_version already equals schemaVersion the function
// returns without opening a txn (avoids spurious BEGIN/COMMIT pairs in
// tests that re-invoke applyMigrations on an already-migrated DB).
func applyMigrations(db *sql.DB) error {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if v >= schemaVersion {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration txn: %w", err)
	}
	defer tx.Rollback()

	for i := v; i < schemaVersion; i++ {
		for _, stmt := range strings.Split(migrations[i], ";") {
			s := strings.TrimSpace(stmt)
			if s == "" {
				continue
			}
			if _, err := tx.Exec(s); err != nil {
				return fmt.Errorf("migration v%d→v%d failed at stmt %q: %w",
					i, i+1, firstLine(s), err)
			}
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("bump user_version: %w", err)
	}
	return tx.Commit()
}

// firstLine returns the first non-empty line of s — used in migration
// error messages so the failing statement is identifiable without dumping
// the entire CREATE/ALTER block.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if len(trimmed) > 80 {
				return trimmed[:77] + "..."
			}
			return trimmed
		}
	}
	return ""
}

// Provider mirrors a row in the providers table. v0.1 lets the caller pass
// plaintext APIKey; v0.2 will swap to encrypted storage transparently.
type Provider struct {
	Name             string
	Kind             string
	OpenAIBaseURL    string
	AnthropicBaseURL string
	APIKey           string
	AnthropicVersion string
	IsDefault        bool
	CreatedAt        time.Time
}

func (s *Store) AddProvider(p Provider) error {
	av := p.AnthropicVersion
	if av == "" {
		av = DefaultAnthropicVersion
	}
	now := time.Now().UnixMilli()
	_, err := s.db.Exec(`INSERT INTO providers
		(name, kind, openai_base_url, anthropic_base_url, api_key,
		 anthropic_version, is_default, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Kind, nullable(p.OpenAIBaseURL), nullable(p.AnthropicBaseURL),
		p.APIKey, av, boolInt(p.IsDefault), now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return ErrDuplicate
		}
		return err
	}
	return nil
}

func (s *Store) GetProvider(name string) (Provider, error) {
	row := s.db.QueryRow(`SELECT name, kind, openai_base_url, anthropic_base_url,
		api_key, anthropic_version, is_default, created_at
		FROM providers WHERE name = ?`, name)
	return scanProvider(row)
}

func (s *Store) ListProviders() ([]Provider, error) {
	rows, err := s.db.Query(`SELECT name, kind, openai_base_url, anthropic_base_url,
		api_key, anthropic_version, is_default, created_at
		FROM providers ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) RemoveProvider(name string) error {
	res, err := s.db.Exec(`DELETE FROM providers WHERE name = ?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetDefaultProvider returns the row marked is_default=1, or ErrNotFound
// if none. Used by the router to pass through unknown model names to a
// fallback provider.
func (s *Store) GetDefaultProvider() (Provider, error) {
	row := s.db.QueryRow(`SELECT name, kind, openai_base_url, anthropic_base_url,
		api_key, anthropic_version, is_default, created_at
		FROM providers WHERE is_default = 1 LIMIT 1`)
	return scanProvider(row)
}

// SetDefaultProvider clears the previous default in one transaction so
// the providers table never has two rows with is_default=1.
func (s *Store) SetDefaultProvider(name string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE providers SET is_default = 0`); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE providers SET is_default = 1 WHERE name = ?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// Alias mirrors a row in model_aliases.
type Alias struct {
	Alias         string
	ProviderName  string
	UpstreamModel string
	CreatedAt     time.Time
}

// SetAlias is upsert for GLOBAL aliases (team_id IS NULL). Re-setting an
// existing alias overwrites its provider and upstream model. Returns FK
// error if provider_name doesn't exist. Team-scoped aliases land via the
// future SetAliasForTeam(alias, teamID, ...) method introduced in T9 MCP
// tools — keeping the v1-shape signature here for backward compat with
// router + tests that predate multi-tenancy.
func (s *Store) SetAlias(alias, providerName, upstreamModel string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.Exec(`INSERT INTO model_aliases (alias, team_id, provider_name, upstream_model, created_at)
		VALUES (?, NULL, ?, ?, ?)
		ON CONFLICT(alias) WHERE team_id IS NULL DO UPDATE SET
			provider_name = excluded.provider_name,
			upstream_model = excluded.upstream_model`,
		alias, providerName, upstreamModel, now)
	return err
}

func (s *Store) ResolveAlias(alias string) (Alias, error) {
	row := s.db.QueryRow(`SELECT alias, provider_name, upstream_model, created_at
		FROM model_aliases WHERE alias = ? AND team_id IS NULL`, alias)
	return scanAlias(row)
}

// ResolveAliasForTeam resolves an alias by checking team-scoped aliases first,
// then falling back to global aliases (team_id IS NULL).
func (s *Store) ResolveAliasForTeam(alias, teamID string) (Alias, error) {
	if teamID != "" {
		row := s.db.QueryRow(`SELECT alias, provider_name, upstream_model, created_at
			FROM model_aliases WHERE alias = ? AND team_id = ?`, alias, teamID)
		if a, err := scanAlias(row); err == nil {
			return a, nil
		} else if !errors.Is(err, ErrNotFound) {
			return Alias{}, err
		}
	}
	return s.ResolveAlias(alias)
}

func (s *Store) ListAliases() ([]Alias, error) {
	rows, err := s.db.Query(`SELECT alias, provider_name, upstream_model, created_at
		FROM model_aliases WHERE team_id IS NULL ORDER BY alias ASC`)
	if err != nil {
		return nil, err
	}
	return collectAliases(rows)
}

// ListAliasesForTeam returns aliases scoped to a specific team, ordered by alias.
func (s *Store) ListAliasesForTeam(teamID string) ([]Alias, error) {
	rows, err := s.db.Query(`SELECT alias, provider_name, upstream_model, created_at
		FROM model_aliases WHERE team_id = ? ORDER BY alias ASC`, teamID)
	if err != nil {
		return nil, err
	}
	return collectAliases(rows)
}

func (s *Store) RemoveAlias(alias string) error {
	res, err := s.db.Exec(`DELETE FROM model_aliases WHERE alias = ? AND team_id IS NULL`, alias)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveAliasForTeam deletes a team-scoped alias.
func (s *Store) RemoveAliasForTeam(alias, teamID string) error {
	res, err := s.db.Exec(`DELETE FROM model_aliases WHERE alias = ? AND team_id = ?`, alias, teamID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAliasForTeam is upsert for team-scoped aliases.
func (s *Store) SetAliasForTeam(alias, teamID, providerName, upstreamModel string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.Exec(`INSERT INTO model_aliases (alias, team_id, provider_name, upstream_model, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(alias, team_id) DO UPDATE SET
			provider_name = excluded.provider_name,
			upstream_model = excluded.upstream_model`,
		alias, teamID, providerName, upstreamModel, now)
	return err
}

// RequestLog mirrors a row in request_logs. The HTTP handler writes one per
// completed request (success or upstream error).
type RequestLog struct {
	ID               int64
	Ts               time.Time
	ClientModel      string
	ResolvedModel    string
	ProviderName     string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	LatencyMs        int
	Status           string // "ok" | "upstream_error" | "timeout" | "abort"
	ErrorMsg         string
	PromptExcerpt    string
	// v2 tenancy columns — empty string when not set
	APIKeyID string
	TeamID   string
}

// ListRequestLogsFilter controls which rows ListRequestLogs returns.
// Zero values mean "no filter". Limit defaults to 50; max 200.
type ListRequestLogsFilter struct {
	TeamID   string // internal UUID (already resolved from slug by the caller)
	APIKeyID string // ak_xxxx
	Limit    int
}

func (s *Store) LogRequest(r RequestLog) (int64, error) {
	if r.Ts.IsZero() {
		r.Ts = time.Now()
	}
	res, err := s.db.Exec(`INSERT INTO request_logs
		(ts, client_model, resolved_model, provider_name,
		 prompt_tokens, completion_tokens, total_tokens, latency_ms,
		 status, error_msg, prompt_excerpt, api_key_id, team_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Ts.UnixMilli(), r.ClientModel, r.ResolvedModel, r.ProviderName,
		r.PromptTokens, r.CompletionTokens, r.TotalTokens, r.LatencyMs,
		r.Status, nullable(r.ErrorMsg), nullable(r.PromptExcerpt),
		nullable(r.APIKeyID), nullable(r.TeamID))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) TailLogs(n int) ([]RequestLog, error) {
	rows, err := s.db.Query(`SELECT id, ts, client_model, resolved_model, provider_name,
		prompt_tokens, completion_tokens, total_tokens, latency_ms,
		status, error_msg, prompt_excerpt
		FROM request_logs ORDER BY ts DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RequestLog
	for rows.Next() {
		r, err := scanLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRequestLog(id int64) (RequestLog, error) {
	row := s.db.QueryRow(`SELECT id, ts, client_model, resolved_model, provider_name,
		prompt_tokens, completion_tokens, total_tokens, latency_ms,
		status, error_msg, prompt_excerpt
		FROM request_logs WHERE id = ?`, id)
	r, err := scanLog(row)
	if errors.Is(err, sql.ErrNoRows) {
		return RequestLog{}, ErrNotFound
	}
	return r, err
}

// ListRequestLogs returns logs newest-first, optionally filtered by team or
// key. Limit defaults to 50 and is capped at 200.
func (s *Store) ListRequestLogs(f ListRequestLogsFilter) ([]RequestLog, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	q := `SELECT id, ts, client_model, resolved_model, provider_name,
		prompt_tokens, completion_tokens, total_tokens, latency_ms,
		status, error_msg, prompt_excerpt,
		COALESCE(api_key_id, '') AS api_key_id,
		COALESCE(team_id, '') AS team_id
		FROM request_logs`

	var args []any
	var where []string
	if f.TeamID != "" {
		where = append(where, "team_id = ?")
		args = append(args, f.TeamID)
	}
	if f.APIKeyID != "" {
		where = append(where, "api_key_id = ?")
		args = append(args, f.APIKeyID)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY ts DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RequestLog
	for rows.Next() {
		var rl RequestLog
		var ts int64
		var errMsg, excerpt sql.NullString
		var promptT, completionT, totalT, latency sql.NullInt64
		err := rows.Scan(
			&rl.ID, &ts, &rl.ClientModel, &rl.ResolvedModel, &rl.ProviderName,
			&promptT, &completionT, &totalT, &latency,
			&rl.Status, &errMsg, &excerpt,
			&rl.APIKeyID, &rl.TeamID,
		)
		if err != nil {
			return nil, err
		}
		rl.Ts = time.UnixMilli(ts)
		rl.PromptTokens = int(promptT.Int64)
		rl.CompletionTokens = int(completionT.Int64)
		rl.TotalTokens = int(totalT.Int64)
		rl.LatencyMs = int(latency.Int64)
		rl.ErrorMsg = errMsg.String
		rl.PromptExcerpt = excerpt.String
		out = append(out, rl)
	}
	return out, rows.Err()
}

// --- helpers ---

type rowScanner interface {
	Scan(...any) error
}

func scanProvider(r rowScanner) (Provider, error) {
	var p Provider
	var openai, anthropic sql.NullString
	var isDefault, createdAt int64
	err := r.Scan(&p.Name, &p.Kind, &openai, &anthropic, &p.APIKey,
		&p.AnthropicVersion, &isDefault, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, ErrNotFound
	}
	if err != nil {
		return Provider{}, err
	}
	p.OpenAIBaseURL = openai.String
	p.AnthropicBaseURL = anthropic.String
	p.IsDefault = isDefault == 1
	p.CreatedAt = time.UnixMilli(createdAt)
	return p, nil
}

func scanLog(r rowScanner) (RequestLog, error) {
	var rl RequestLog
	var ts int64
	var errMsg, excerpt sql.NullString
	var promptT, completionT, totalT, latency sql.NullInt64
	err := r.Scan(&rl.ID, &ts, &rl.ClientModel, &rl.ResolvedModel, &rl.ProviderName,
		&promptT, &completionT, &totalT, &latency,
		&rl.Status, &errMsg, &excerpt)
	if err != nil {
		return rl, err
	}
	rl.Ts = time.UnixMilli(ts)
	rl.PromptTokens = int(promptT.Int64)
	rl.CompletionTokens = int(completionT.Int64)
	rl.TotalTokens = int(totalT.Int64)
	rl.LatencyMs = int(latency.Int64)
	rl.ErrorMsg = errMsg.String
	rl.PromptExcerpt = excerpt.String
	return rl, nil
}

// nullable converts an empty string to SQL NULL so CHECK constraints and
// nullable columns behave as documented; non-empty strings pass through.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func scanAlias(r rowScanner) (Alias, error) {
	var a Alias
	var createdAt int64
	if err := r.Scan(&a.Alias, &a.ProviderName, &a.UpstreamModel, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Alias{}, ErrNotFound
		}
		return Alias{}, err
	}
	a.CreatedAt = time.UnixMilli(createdAt)
	return a, nil
}

func collectAliases(rows *sql.Rows) ([]Alias, error) {
	defer rows.Close()
	var out []Alias
	for rows.Next() {
		a, err := scanAlias(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
