package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/panda/llm-gateway/internal/encrypt"
)

// MasterKeyInfo is the public (non-secret) view of a master_keys row.
type MasterKeyInfo struct {
	ID        string
	Label     string
	CreatedAt time.Time
	Active    bool // retired_at IS NULL
}

// AddMasterKey stores a new master key record (pre-encrypted with KEK by the caller).
// Returns the generated UUID.
func (s *Store) AddMasterKey(label, keyEncB64 string) (string, error) {
	id := uuid.New().String()
	now := time.Now().UnixMilli()
	_, err := s.db.Exec(
		`INSERT INTO master_keys (id, key_enc_b64, label, created_at) VALUES (?, ?, ?, ?)`,
		id, keyEncB64, nullable(label), now,
	)
	if err != nil {
		return "", fmt.Errorf("store: add master key: %w", err)
	}
	return id, nil
}

// GetActiveMasterKey returns the most recently created non-retired master key,
// decrypts it with the provided KEK, and returns the MasterKey.
// Returns ErrNotFound when no active key exists.
func (s *Store) GetActiveMasterKey(kek *encrypt.KEK) (*encrypt.MasterKey, error) {
	var keyEncB64 string
	err := s.db.QueryRow(
		`SELECT key_enc_b64 FROM master_keys WHERE retired_at IS NULL ORDER BY created_at DESC LIMIT 1`,
	).Scan(&keyEncB64)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get active master key: %w", err)
	}
	return kek.DecryptMasterKey(keyEncB64)
}

// RetireMasterKey sets retired_at = now for the given key ID.
// Returns ErrNotFound if the ID does not exist or is already retired.
func (s *Store) RetireMasterKey(id string) error {
	res, err := s.db.Exec(
		`UPDATE master_keys SET retired_at = ? WHERE id = ? AND retired_at IS NULL`,
		time.Now().UnixMilli(), id,
	)
	if err != nil {
		return fmt.Errorf("store: retire master key: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListMasterKeys returns all key records (without decrypting), newest first.
func (s *Store) ListMasterKeys() ([]MasterKeyInfo, error) {
	rows, err := s.db.Query(
		`SELECT id, COALESCE(label,''), created_at, retired_at IS NULL FROM master_keys ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list master keys: %w", err)
	}
	defer rows.Close()
	var out []MasterKeyInfo
	for rows.Next() {
		var info MasterKeyInfo
		var createdAt int64
		var active bool
		if err := rows.Scan(&info.ID, &info.Label, &createdAt, &active); err != nil {
			return nil, fmt.Errorf("store: scan master key: %w", err)
		}
		info.CreatedAt = time.UnixMilli(createdAt)
		info.Active = active
		out = append(out, info)
	}
	return out, rows.Err()
}

// RekeyProviders re-encrypts all provider API keys using the active master key.
// The store's current encryptor (old key) is used to decrypt; the new active
// key encrypts. Returns the count of rekeyed providers.
func (s *Store) RekeyProviders(kek *encrypt.KEK) (int, error) {
	newMK, err := s.GetActiveMasterKey(kek)
	if err != nil {
		return 0, fmt.Errorf("store: rekey providers: get active key: %w", err)
	}
	providers, err := s.ListProviders()
	if err != nil {
		return 0, fmt.Errorf("store: rekey providers: list: %w", err)
	}
	count := 0
	for _, p := range providers {
		if s.enc == nil || !s.enc.IsEncryptedMethod(p.APIKey) {
			continue
		}
		raw, err := s.enc.Decrypt(p.APIKey)
		if err != nil {
			return count, fmt.Errorf("store: rekey %q: decrypt: %w", p.Name, err)
		}
		reencrypted, err := newMK.Encrypt(raw)
		if err != nil {
			return count, fmt.Errorf("store: rekey %q: encrypt: %w", p.Name, err)
		}
		if _, err := s.db.Exec(`UPDATE providers SET api_key = ? WHERE name = ?`, reencrypted, p.Name); err != nil {
			return count, fmt.Errorf("store: rekey %q: update: %w", p.Name, err)
		}
		count++
	}
	s.SetEncryptor(newMK)
	return count, nil
}
