package store

import (
	"database/sql"
	"errors"
)

// ProviderCapability mirrors a row in the provider_capabilities table.
type ProviderCapability struct {
	ID           int64
	ProviderName string
	Capability   string
	Value        string
	UpdatedAt    string
}

// SetProviderCapability upserts a capability for a provider. Value is an
// optional string (e.g., "true", "128k", "reasoning=extended").
func (s *Store) SetProviderCapability(providerName, capability, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO provider_capabilities (provider_name, capability, value, updated_at)
		VALUES (?, ?, ?, datetime('now'))
		ON CONFLICT(provider_name, capability) DO UPDATE SET
			value      = excluded.value,
			updated_at = datetime('now')`,
		providerName, capability, value)
	return err
}

// ListProviderCapabilities returns all capabilities for a provider.
// Returns empty slice (not nil) when no capabilities are set.
func (s *Store) ListProviderCapabilities(providerName string) ([]ProviderCapability, error) {
	rows, err := s.db.Query(`
		SELECT id, provider_name, capability, value, updated_at
		FROM provider_capabilities
		WHERE provider_name = ?
		ORDER BY capability ASC`, providerName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ProviderCapability{} // never nil
	for rows.Next() {
		var c ProviderCapability
		if err := rows.Scan(&c.ID, &c.ProviderName, &c.Capability, &c.Value, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteProviderCapability removes a single capability from a provider.
// Returns ErrNotFound when the (provider_name, capability) pair does not exist.
func (s *Store) DeleteProviderCapability(providerName, capability string) error {
	res, err := s.db.Exec(`
		DELETE FROM provider_capabilities
		WHERE provider_name = ? AND capability = ?`,
		providerName, capability)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// HasCapabilityInStore checks whether a provider has a specific capability set.
// Returns (true, value, nil) when found, (false, "", nil) when absent.
func (s *Store) HasCapabilityInStore(providerName, capability string) (bool, string, error) {
	var value string
	err := s.db.QueryRow(`
		SELECT value FROM provider_capabilities
		WHERE provider_name = ? AND capability = ?`,
		providerName, capability).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return true, value, nil
}
