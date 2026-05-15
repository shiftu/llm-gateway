package store

import (
	"testing"

	"github.com/panda/llm-gateway/internal/encrypt"
)

// testEncryptor wraps encrypt.MasterKey for store-level integration tests.
type testEncryptor struct {
	mk *encrypt.MasterKey
}

func (t *testEncryptor) Encrypt(plaintext []byte) (string, error) {
	return t.mk.Encrypt(plaintext)
}
func (t *testEncryptor) Decrypt(encoded string) ([]byte, error) {
	return t.mk.Decrypt(encoded)
}
func (t *testEncryptor) IsEncryptedMethod(s string) bool {
	return encrypt.IsEncrypted(s)
}

func TestAddProviderEncryptsKey(t *testing.T) {
	s := openTest(t)
	mk, err := encrypt.NewFromBytes(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s.SetEncryptor(&testEncryptor{mk: mk})

	err = s.AddProvider(Provider{
		Name: "enc-test", Kind: "deepseek",
		OpenAIBaseURL: "https://api.deepseek.com",
		APIKey:        "sk-secret-key-123",
		IsDefault:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify raw DB has encrypted prefix
	var raw string
	err = s.db.QueryRow(`SELECT api_key FROM providers WHERE name = ?`, "enc-test").Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if !encrypt.IsEncrypted(raw) {
		t.Fatalf("expected encrypted prefix in DB, got: %s", raw)
	}

	// Verify GetProvider returns plaintext
	p, err := s.GetProvider("enc-test")
	if err != nil {
		t.Fatal(err)
	}
	if p.APIKey != "sk-secret-key-123" {
		t.Fatalf("expected plaintext key from GetProvider, got: %s", p.APIKey)
	}

	// Verify ListProviders returns plaintext
	provs, err := s.ListProviders()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range provs {
		if p.Name == "enc-test" {
			found = true
			if p.APIKey != "sk-secret-key-123" {
				t.Fatalf("ListProviders returned encrypted key: %s", p.APIKey)
			}
		}
	}
	if !found {
		t.Fatal("enc-test provider not found in ListProviders")
	}
}

func TestAddProviderPlaintextCompat(t *testing.T) {
	s := openTest(t)
	// No encryptor set — should store plaintext
	err := s.AddProvider(Provider{
		Name: "plain", Kind: "deepseek",
		OpenAIBaseURL: "https://api.deepseek.com",
		APIKey:        "sk-plain-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.GetProvider("plain")
	if err != nil {
		t.Fatal(err)
	}
	if p.APIKey != "sk-plain-key" {
		t.Fatalf("expected plaintext, got: %s", p.APIKey)
	}
}

func TestAddProviderWithEncryptorReadsOldPlaintext(t *testing.T) {
	s := openTest(t)
	// 1) Insert plaintext without encryptor
	err := s.AddProvider(Provider{
		Name: "legacy", Kind: "deepseek",
		OpenAIBaseURL: "https://api.deepseek.com",
		APIKey:        "sk-legacy-key",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2) Now inject encryptor (simulates v0.1 → v0.2 upgrade)
	mk, err := encrypt.NewFromBytes(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s.SetEncryptor(&testEncryptor{mk: mk})

	// 3) Read should still return plaintext (v0.1 compat)
	p, err := s.GetProvider("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if p.APIKey != "sk-legacy-key" {
		t.Fatalf("expected plaintext passthrough, got: %s", p.APIKey)
	}
}
