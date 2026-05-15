package encrypt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	mk, err := NewFromBytes(make([]byte, keyLen))
	if err != nil {
		t.Fatal(err)
	}
	secret := "sk-deepseek-12345"
	enc, err := mk.Encrypt([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncrypted(enc) {
		t.Fatal("expected encrypted value to have prefix")
	}
	dec, err := mk.Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if string(dec) != secret {
		t.Fatalf("got %q, want %q", dec, secret)
	}
}

func TestDecryptWrongKey(t *testing.T) {
	mk1, _ := NewFromBytes(make([]byte, keyLen))
	mk2 := deriveKey("different-key-entirely")

	enc, err := mk1.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = mk2.Decrypt(enc)
	if err == nil {
		t.Fatal("expected decryption error with wrong key")
	}
}

func TestDecryptTamperedCiphertext(t *testing.T) {
	mk, _ := NewFromBytes(make([]byte, keyLen))
	enc, err := mk.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte in the base64 payload
	tampered := enc[:len(prefix)+4] + string(enc[len(prefix)+4]^0xff) + enc[len(prefix)+5:]
	_, err = mk.Decrypt(tampered)
	if err == nil {
		t.Fatal("expected decryption error with tampered data")
	}
}

func TestDecryptPlaintextPassthrough(t *testing.T) {
	mk, _ := NewFromBytes(make([]byte, keyLen))
	// v0.1 plaintext key should pass through unchanged
	dec, err := mk.Decrypt("sk-plain-key-123")
	if err != nil {
		t.Fatal(err)
	}
	if string(dec) != "sk-plain-key-123" {
		t.Fatalf("got %q, want passthrough", dec)
	}
}

func TestNewFromEnvOrFile(t *testing.T) {
	t.Setenv("LLM_GATEWAY_MASTER_KEY", "test-env-key")
	mk, err := NewFromEnvOrFile("/tmp/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	// Should use env, not file
	enc, err := mk.Encrypt([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	dec, err := mk.Decrypt(enc)
	if err != nil || string(dec) != "hello" {
		t.Fatalf("round trip failed: %v %q", err, dec)
	}
}

func TestNewFromEnvOrFile_NoEnv_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	keyContent := "file-based-key\n"
	keyFile := filepath.Join(dir, "master.key")
	if err := os.WriteFile(keyFile, []byte(keyContent), 0o600); err != nil {
		t.Fatal(err)
	}
	// Unset env to force file read
	os.Unsetenv("LLM_GATEWAY_MASTER_KEY")

	mk, err := NewFromEnvOrFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := mk.Encrypt([]byte("test"))
	if err != nil {
		t.Fatal(err)
	}
	dec, err := mk.Decrypt(enc)
	if string(dec) != "test" {
		t.Fatalf("got %q, want test", dec)
	}
}

func TestNewFromEnvOrFile_NoKey_ReturnsError(t *testing.T) {
	os.Unsetenv("LLM_GATEWAY_MASTER_KEY")
	_, err := NewFromEnvOrFile("/tmp/nonexistent-dir-xyz")
	if err != ErrNoMasterKey {
		t.Fatalf("got %v, want ErrNoMasterKey", err)
	}
}

func TestGenerateAndWrite(t *testing.T) {
	dir := t.TempDir()
	os.Unsetenv("LLM_GATEWAY_MASTER_KEY")

	mk1, path1, err := GenerateAndWrite(dir)
	if err != nil {
		t.Fatal(err)
	}
	if path1 != filepath.Join(dir, "master.key") {
		t.Fatalf("got path %q", path1)
	}

	// Second call should NOT overwrite — read existing key
	mk2, path2, err := GenerateAndWrite(dir)
	if err != nil {
		t.Fatal(err)
	}
	if path2 != path1 {
		t.Fatal("path changed on second call")
	}

	// Both keys should encrypt/decrypt the same way
	enc, _ := mk1.Encrypt([]byte("hello"))
	dec, err := mk2.Decrypt(enc)
	if err != nil || string(dec) != "hello" {
		t.Fatalf("keys differ: %v %q", err, dec)
	}
}

func TestIsEncrypted(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"lgw_enc:v1:AAAA", true},
		{"sk-plain-key", false},
		{"", false},
		{"lgw_enc:v1:", false}, // prefix only, nothing after
	}
	for _, tt := range tests {
		if got := IsEncrypted(tt.s); got != tt.want {
			t.Errorf("IsEncrypted(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
}
