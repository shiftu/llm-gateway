package store

import (
	"errors"
	"testing"

	"github.com/panda/llm-gateway/internal/encrypt"
)

func newTestKEK(t *testing.T) *encrypt.KEK {
	t.Helper()
	return encrypt.NewKEKFromString("test-kek-passphrase")
}

func TestMasterKey_AddAndGetActive(t *testing.T) {
	s := openTest(t)
	kek := newTestKEK(t)

	mk, err := encrypt.GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey: %v", err)
	}
	encB64, err := kek.EncryptMasterKey(mk)
	if err != nil {
		t.Fatalf("EncryptMasterKey: %v", err)
	}

	id, err := s.AddMasterKey("test-label", encB64)
	if err != nil {
		t.Fatalf("AddMasterKey: %v", err)
	}
	if id == "" {
		t.Fatalf("AddMasterKey returned empty id")
	}

	got, err := s.GetActiveMasterKey(kek)
	if err != nil {
		t.Fatalf("GetActiveMasterKey: %v", err)
	}

	// Re-encrypt something with original key, decrypt with got — confirms same key bytes
	plaintext := []byte("round-trip-test")
	ciphertext, err := mk.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt with original: %v", err)
	}
	recovered, err := got.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt with retrieved key: %v", err)
	}
	if string(recovered) != string(plaintext) {
		t.Fatalf("key round-trip failed: want %q, got %q", plaintext, recovered)
	}
}

func TestMasterKey_RetireKey(t *testing.T) {
	s := openTest(t)
	kek := newTestKEK(t)

	mk, err := encrypt.GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey: %v", err)
	}
	encB64, err := kek.EncryptMasterKey(mk)
	if err != nil {
		t.Fatalf("EncryptMasterKey: %v", err)
	}

	id, err := s.AddMasterKey("retire-me", encB64)
	if err != nil {
		t.Fatalf("AddMasterKey: %v", err)
	}

	if err := s.RetireMasterKey(id); err != nil {
		t.Fatalf("RetireMasterKey: %v", err)
	}

	_, err = s.GetActiveMasterKey(kek)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after retire, got: %v", err)
	}
}

func TestMasterKey_RetireNonExistent(t *testing.T) {
	s := openTest(t)
	err := s.RetireMasterKey("no-such-id")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing id, got: %v", err)
	}
}

func TestMasterKey_GetActive_NoKeys(t *testing.T) {
	s := openTest(t)
	kek := newTestKEK(t)
	_, err := s.GetActiveMasterKey(kek)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound when no keys, got: %v", err)
	}
}

func TestMasterKey_ListMasterKeys(t *testing.T) {
	s := openTest(t)
	kek := newTestKEK(t)

	addKey := func(label string) string {
		mk, err := encrypt.GenerateMasterKey()
		if err != nil {
			t.Fatalf("GenerateMasterKey: %v", err)
		}
		encB64, err := kek.EncryptMasterKey(mk)
		if err != nil {
			t.Fatalf("EncryptMasterKey: %v", err)
		}
		id, err := s.AddMasterKey(label, encB64)
		if err != nil {
			t.Fatalf("AddMasterKey(%q): %v", label, err)
		}
		return id
	}

	id1 := addKey("key-one")
	id2 := addKey("key-two")

	// Retire id1
	if err := s.RetireMasterKey(id1); err != nil {
		t.Fatalf("RetireMasterKey: %v", err)
	}

	keys, err := s.ListMasterKeys()
	if err != nil {
		t.Fatalf("ListMasterKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("want 2 keys, got %d", len(keys))
	}

	byID := make(map[string]MasterKeyInfo)
	for _, k := range keys {
		byID[k.ID] = k
	}

	k1, ok := byID[id1]
	if !ok {
		t.Fatalf("id1 not in list")
	}
	if k1.Active {
		t.Errorf("id1 should be inactive (retired)")
	}
	if k1.Label != "key-one" {
		t.Errorf("id1 label: want %q, got %q", "key-one", k1.Label)
	}

	k2, ok := byID[id2]
	if !ok {
		t.Fatalf("id2 not in list")
	}
	if !k2.Active {
		t.Errorf("id2 should be active")
	}
	if k2.Label != "key-two" {
		t.Errorf("id2 label: want %q, got %q", "key-two", k2.Label)
	}
}
