package encrypt

import (
	"crypto/rand"
	"fmt"
	"time"
)

// KEK is a Key Encryption Key used to protect master keys stored in the DB.
// Provided via LLM_GATEWAY_KEK environment variable.
type KEK struct {
	key *MasterKey // reuse MasterKey AEAD for key wrapping
}

// NewKEKFromString derives a KEK from a passphrase via SHA-256.
func NewKEKFromString(passphrase string) *KEK {
	return &KEK{key: deriveKey(passphrase)}
}

// EncryptMasterKey encrypts a MasterKey's raw bytes with the KEK.
// Returns the lgw_enc:v1:-prefixed ciphertext string.
func (k *KEK) EncryptMasterKey(mk *MasterKey) (string, error) {
	encrypted, err := k.key.Encrypt(mk.rawBytes())
	if err != nil {
		return "", fmt.Errorf("encrypt: wrap master key: %w", err)
	}
	return encrypted, nil
}

// DecryptMasterKey decrypts a wrapped master key string produced by EncryptMasterKey.
func (k *KEK) DecryptMasterKey(encryptedStr string) (*MasterKey, error) {
	raw, err := k.key.Decrypt(encryptedStr)
	if err != nil {
		return nil, fmt.Errorf("encrypt: unwrap master key: %w", err)
	}
	return NewFromBytes(raw)
}

// GenerateMasterKey creates a new random 32-byte MasterKey.
func GenerateMasterKey() (*MasterKey, error) {
	raw := make([]byte, keyLen)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("encrypt: generate master key: %w", err)
	}
	return NewFromBytes(raw)
}

// MasterKeyRecord is the DB-serializable form of a master key entry.
type MasterKeyRecord struct {
	ID        string
	KeyEncB64 string     // encrypted with KEK
	Label     string
	CreatedAt time.Time
	RetiredAt *time.Time // nil = active
}

// rawBytes returns the underlying key bytes. Used only for key wrapping.
func (mk *MasterKey) rawBytes() []byte {
	b := make([]byte, keyLen)
	copy(b, mk.key[:])
	return b
}
