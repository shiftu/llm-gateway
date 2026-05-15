// Package encrypt provides at-rest encryption for provider API keys using
// XChaCha20-Poly1305 AEAD. The master key is sourced from either the
// LLM_GATEWAY_MASTER_KEY environment variable or a file at
// ~/.config/llm-gateway/master.key (created by `init` if absent).
//
// Encrypted values are stored as: "lgw_enc:v1:" + base64(nonce(24) || ciphertext)
// so they can be distinguished from plaintext at a glance.
package encrypt

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	prefix  = "lgw_enc:v1:"
	keyLen  = 32 // XChaCha20-Poly1305 key size
	nonceLen = 24 // XChaCha20 nonce size
)

// ErrNoMasterKey is returned when no master key can be found.
var ErrNoMasterKey = errors.New("encrypt: LLM_GATEWAY_MASTER_KEY not set and no master.key file found")

// MasterKey wraps a 32-byte XChaCha20-Poly1305 key.
type MasterKey struct {
	key [keyLen]byte
}

// NewFromBytes constructs a MasterKey from raw bytes (must be exactly 32).
func NewFromBytes(b []byte) (*MasterKey, error) {
	if len(b) != keyLen {
		return nil, fmt.Errorf("encrypt: key must be %d bytes, got %d", keyLen, len(b))
	}
	mk := &MasterKey{}
	copy(mk.key[:], b)
	return mk, nil
}

// NewFromEnv reads LLM_GATEWAY_MASTER_KEY, derives a 32-byte key via SHA-256.
func NewFromEnv() (*MasterKey, error) {
	v := os.Getenv("LLM_GATEWAY_MASTER_KEY")
	if v == "" {
		return nil, ErrNoMasterKey
	}
	return deriveKey(v), nil
}

// NewFromEnvOrFile tries env first, then reads master.key from cfgDir.
// File contents are base64url-encoded raw 32 bytes (as written by GenerateAndWrite).
func NewFromEnvOrFile(cfgDir string) (*MasterKey, error) {
	if v := os.Getenv("LLM_GATEWAY_MASTER_KEY"); v != "" {
		return deriveKey(v), nil
	}
	path := filepath.Join(cfgDir, "master.key")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrNoMasterKey
	}
	raw := trimNewlines(string(data))
	if len(raw) == 0 {
		return nil, ErrNoMasterKey
	}
	// Try base64url decode first (file written by GenerateAndWrite)
	if decoded, err := base64.RawURLEncoding.DecodeString(raw); err == nil && len(decoded) == keyLen {
		return NewFromBytes(decoded)
	}
	// Fallback: treat as passphrase, derive via SHA-256 (for operator-provided keys)
	return deriveKey(raw), nil
}

// GenerateAndWrite creates a 32-byte random key, writes it to master.key (0600),
// and returns the MasterKey. If the file already exists it is NOT overwritten —
// the existing key is read instead.
func GenerateAndWrite(cfgDir string) (*MasterKey, string, error) {
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return nil, "", err
	}
	path := filepath.Join(cfgDir, "master.key")

	// Don't overwrite an existing key
	if _, err := os.Stat(path); err == nil {
		mk, readErr := NewFromEnvOrFile(cfgDir)
		if readErr != nil {
			return nil, "", readErr
		}
		return mk, path, nil
	}

	raw := make([]byte, keyLen)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("encrypt: generating key: %w", err)
	}
	// Store as base64url for human readability
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
		return nil, "", fmt.Errorf("encrypt: writing master.key: %w", err)
	}

	mk, _ := NewFromBytes(raw)
	return mk, path, nil
}

// Encrypt encrypts plaintext and returns a prefixed base64 string.
func (mk *MasterKey) Encrypt(plaintext []byte) (string, error) {
	aead, err := chacha20poly1305.NewX(mk.key[:])
	if err != nil {
		return "", fmt.Errorf("encrypt: creating AEAD: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("encrypt: generating nonce: %w", err)
	}
	ciphertext := aead.Seal(nonce, nonce, plaintext, nil)
	// ciphertext now = nonce || encrypted_payload
	return prefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a prefixed base64 string and returns plaintext.
func (mk *MasterKey) Decrypt(encoded string) ([]byte, error) {
	if !IsEncrypted(encoded) {
		// Not encrypted — plaintext passthrough for v0.1 migration
		return []byte(encoded), nil
	}
	b64 := encoded[len(prefix):]
	ciphertext, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("encrypt: decoding base64: %w", err)
	}
	if len(ciphertext) < nonceLen {
		return nil, errors.New("encrypt: ciphertext too short")
	}

	aead, err := chacha20poly1305.NewX(mk.key[:])
	if err != nil {
		return nil, fmt.Errorf("encrypt: creating AEAD: %w", err)
	}
	nonce := ciphertext[:nonceLen]
	payload := ciphertext[nonceLen:]
	plaintext, err := aead.Open(nil, nonce, payload, nil)
	if err != nil {
		return nil, fmt.Errorf("encrypt: decryption failed (wrong key or tampered data)")
	}
	return plaintext, nil
}

// IsEncrypted returns true if s starts with the lgw_enc:v1: prefix.
func IsEncrypted(s string) bool {
	return len(s) > len(prefix) && s[:len(prefix)] == prefix
}

// IsEncryptedMethod is the method-form for the store.encryptor interface.
func (mk *MasterKey) IsEncryptedMethod(s string) bool { return IsEncrypted(s) }

// deriveKey hashes the input string with SHA-256 to produce a 32-byte key.
func deriveKey(s string) *MasterKey {
	h := sha256.Sum256([]byte(s))
	mk := &MasterKey{}
	copy(mk.key[:], h[:])
	return mk
}

func trimNewlines(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
