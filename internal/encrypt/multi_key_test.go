package encrypt

import (
	"bytes"
	"testing"
)

func TestKEK_EncryptDecryptMasterKey(t *testing.T) {
	kek := NewKEKFromString("test-passphrase")

	mk, err := NewFromBytes(make([]byte, keyLen))
	if err != nil {
		t.Fatalf("NewFromBytes: %v", err)
	}

	encB64, err := kek.EncryptMasterKey(mk)
	if err != nil {
		t.Fatalf("EncryptMasterKey: %v", err)
	}
	if encB64 == "" {
		t.Fatalf("EncryptMasterKey returned empty string")
	}

	got, err := kek.DecryptMasterKey(encB64)
	if err != nil {
		t.Fatalf("DecryptMasterKey: %v", err)
	}
	if !bytes.Equal(mk.rawBytes(), got.rawBytes()) {
		t.Fatalf("round-trip mismatch: want %x, got %x", mk.rawBytes(), got.rawBytes())
	}
}

func TestKEK_RoundTrip(t *testing.T) {
	kek := NewKEKFromString("another-passphrase")

	mk, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey: %v", err)
	}

	original := mk.rawBytes()

	encB64, err := kek.EncryptMasterKey(mk)
	if err != nil {
		t.Fatalf("EncryptMasterKey: %v", err)
	}

	recovered, err := kek.DecryptMasterKey(encB64)
	if err != nil {
		t.Fatalf("DecryptMasterKey: %v", err)
	}

	if !bytes.Equal(original, recovered.rawBytes()) {
		t.Fatalf("key bytes differ after round-trip: want %x, got %x", original, recovered.rawBytes())
	}
}

func TestKEK_WrongKEKFails(t *testing.T) {
	kek1 := NewKEKFromString("kek-one")
	kek2 := NewKEKFromString("kek-two")

	mk, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey: %v", err)
	}

	encB64, err := kek1.EncryptMasterKey(mk)
	if err != nil {
		t.Fatalf("EncryptMasterKey: %v", err)
	}

	_, err = kek2.DecryptMasterKey(encB64)
	if err == nil {
		t.Fatalf("expected error when decrypting with wrong KEK")
	}
}

func TestGenerateMasterKey_Random(t *testing.T) {
	mk1, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey: %v", err)
	}
	mk2, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey: %v", err)
	}
	if bytes.Equal(mk1.rawBytes(), mk2.rawBytes()) {
		t.Fatalf("two generated keys should differ")
	}
}
