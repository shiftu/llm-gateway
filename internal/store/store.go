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
	schemaVersion           = 1
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
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		return nil, err
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
}

func applyMigrations(db *sql.DB) error {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	for i := v; i < schemaVersion; i++ {
		for _, stmt := range strings.Split(migrations[i], ";") {
			s := strings.TrimSpace(stmt)
			if s == "" {
				continue
			}
			if _, err := db.Exec(s); err != nil {
				return fmt.Errorf("migration v%d→v%d: %w", i, i+1, err)
			}
		}
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return nil
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

// SetAlias is upsert — re-setting an existing alias overwrites its provider
// and upstream model. Returns FK error if provider_name doesn't exist.
func (s *Store) SetAlias(alias, providerName, upstreamModel string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.Exec(`INSERT INTO model_aliases (alias, provider_name, upstream_model, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(alias) DO UPDATE SET
			provider_name = excluded.provider_name,
			upstream_model = excluded.upstream_model`,
		alias, providerName, upstreamModel, now)
	return err
}

func (s *Store) ResolveAlias(alias string) (Alias, error) {
	row := s.db.QueryRow(`SELECT alias, provider_name, upstream_model, created_at
		FROM model_aliases WHERE alias = ?`, alias)
	var a Alias
	var createdAt int64
	if err := row.Scan(&a.Alias, &a.ProviderName, &a.UpstreamModel, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Alias{}, ErrNotFound
		}
		return Alias{}, err
	}
	a.CreatedAt = time.UnixMilli(createdAt)
	return a, nil
}

func (s *Store) ListAliases() ([]Alias, error) {
	rows, err := s.db.Query(`SELECT alias, provider_name, upstream_model, created_at
		FROM model_aliases ORDER BY alias ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alias
	for rows.Next() {
		var a Alias
		var createdAt int64
		if err := rows.Scan(&a.Alias, &a.ProviderName, &a.UpstreamModel, &createdAt); err != nil {
			return nil, err
		}
		a.CreatedAt = time.UnixMilli(createdAt)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) RemoveAlias(alias string) error {
	res, err := s.db.Exec(`DELETE FROM model_aliases WHERE alias = ?`, alias)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
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
}

func (s *Store) LogRequest(r RequestLog) (int64, error) {
	if r.Ts.IsZero() {
		r.Ts = time.Now()
	}
	res, err := s.db.Exec(`INSERT INTO request_logs
		(ts, client_model, resolved_model, provider_name,
		 prompt_tokens, completion_tokens, total_tokens, latency_ms,
		 status, error_msg, prompt_excerpt)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Ts.UnixMilli(), r.ClientModel, r.ResolvedModel, r.ProviderName,
		r.PromptTokens, r.CompletionTokens, r.TotalTokens, r.LatencyMs,
		r.Status, nullable(r.ErrorMsg), nullable(r.PromptExcerpt))
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
