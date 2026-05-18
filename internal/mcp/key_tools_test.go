package mcp

import (
	"encoding/json"
	"testing"

	"github.com/panda/llm-gateway/internal/encrypt"
	"github.com/panda/llm-gateway/internal/store"
)

// setupKeyStore returns a store and a KEK with one pre-loaded active master key,
// and injects that master key as the store encryptor.
func setupKeyStore(t *testing.T) (*store.Store, *encrypt.KEK) {
	t.Helper()
	st := openTestStore(t)
	kek := encrypt.NewKEKFromString("test-kek-for-mcp")

	mk, err := encrypt.GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey: %v", err)
	}
	encB64, err := kek.EncryptMasterKey(mk)
	if err != nil {
		t.Fatalf("EncryptMasterKey: %v", err)
	}
	if _, err := st.AddMasterKey("initial", encB64); err != nil {
		t.Fatalf("AddMasterKey: %v", err)
	}
	st.SetEncryptor(mk)
	return st, kek
}

// --- rotate_master_key ---

func TestRotateMasterKey_NoKEK_ReturnsError(t *testing.T) {
	st := openTestStore(t)
	h := rotateMasterKeyHandler(st)

	t.Setenv("LLM_GATEWAY_KEK", "")
	res, err := h(ctxWithScope("mcp_super"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error when KEK not set, got success")
	}
}

func TestRotateMasterKey_Success(t *testing.T) {
	st := openTestStore(t)
	h := rotateMasterKeyHandler(st)

	t.Setenv("LLM_GATEWAY_KEK", "my-test-kek")
	res, err := h(ctxWithScope("mcp_super"), callTool(map[string]any{"label": "test-rotation"}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got tool error: %v", res.Content)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["status"] != "rotated" {
		t.Errorf("status: want rotated, got %v", out["status"])
	}
	if out["key_id"] == "" {
		t.Errorf("key_id must be non-empty")
	}

	// Confirm it was persisted
	keys, err := st.ListMasterKeys()
	if err != nil {
		t.Fatalf("ListMasterKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("want 1 key, got %d", len(keys))
	}
	if keys[0].Label != "test-rotation" {
		t.Errorf("label: want %q, got %q", "test-rotation", keys[0].Label)
	}
}

func TestRotateMasterKey_ACL_AdminDenied(t *testing.T) {
	st := openTestStore(t)
	h := rotateMasterKeyHandler(st)

	t.Setenv("LLM_GATEWAY_KEK", "kek")
	res, err := h(ctxWithScope("mcp_admin"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("mcp_admin should be denied rotate_master_key")
	}
}

// --- rekey_providers ---

func TestRekeyProviders_NoKEK_ReturnsError(t *testing.T) {
	st := openTestStore(t)
	h := rekeyProvidersHandler(st)

	t.Setenv("LLM_GATEWAY_KEK", "")
	res, err := h(ctxWithScope("mcp_super"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error when KEK not set")
	}
}

func TestRekeyProviders_NoActiveKey_ReturnsError(t *testing.T) {
	st := openTestStore(t)
	h := rekeyProvidersHandler(st)

	t.Setenv("LLM_GATEWAY_KEK", "kek-val")
	res, err := h(ctxWithScope("mcp_super"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error when no active master key exists")
	}
}

func TestRekeyProviders_NoEncryptedProviders_ReturnsZero(t *testing.T) {
	st, _ := setupKeyStore(t)
	h := rekeyProvidersHandler(st)

	// Add a plaintext provider (no encryptor set yet for this provider)
	st.SetEncryptor(nil)
	if err := st.AddProvider(store.Provider{
		Name: "plain-prov", Kind: "deepseek",
		OpenAIBaseURL: "https://api.deepseek.com",
		APIKey:        "sk-plain",
	}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}

	t.Setenv("LLM_GATEWAY_KEK", "test-kek-for-mcp")
	res, err := h(ctxWithScope("mcp_super"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got: %v", res.Content)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["rekeyed"].(float64) != 0 {
		t.Errorf("want rekeyed=0 (no encrypted providers), got %v", out["rekeyed"])
	}
}

// --- retire_master_key ---

func TestRetireMasterKey_Success(t *testing.T) {
	st, kek := setupKeyStore(t)
	h := retireMasterKeyHandler(st)

	keys, err := st.ListMasterKeys()
	if err != nil {
		t.Fatalf("ListMasterKeys: %v", err)
	}
	if len(keys) == 0 {
		t.Fatalf("expected at least one key")
	}
	keyID := keys[0].ID

	res, err := h(ctxWithScope("mcp_super"), callTool(map[string]any{"key_id": keyID}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got: %v", res.Content)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["status"] != "retired" {
		t.Errorf("status: want retired, got %v", out["status"])
	}

	// Confirm it is now inactive
	_, getErr := st.GetActiveMasterKey(kek)
	if getErr == nil {
		t.Errorf("expected ErrNotFound after retiring only key")
	}
}

func TestRetireMasterKey_NotFound(t *testing.T) {
	st := openTestStore(t)
	h := retireMasterKeyHandler(st)

	res, err := h(ctxWithScope("mcp_super"), callTool(map[string]any{"key_id": "no-such-uuid"}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error for missing key_id")
	}
}

func TestRetireMasterKey_MissingParam(t *testing.T) {
	st := openTestStore(t)
	h := retireMasterKeyHandler(st)

	res, err := h(ctxWithScope("mcp_super"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error for missing key_id param")
	}
}

// --- list_master_keys ---

func TestListMasterKeys_Empty(t *testing.T) {
	st := openTestStore(t)
	h := listMasterKeysHandler(st)

	res, err := h(ctxWithScope("mcp_super"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success: %v", res.Content)
	}

	var out []any
	if err := json.Unmarshal([]byte(textOf(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("want empty array, got %d items", len(out))
	}
}

func TestListMasterKeys_WithKeys(t *testing.T) {
	st, kek := setupKeyStore(t)
	h := listMasterKeysHandler(st)

	// Add a second key
	mk2, err := encrypt.GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey: %v", err)
	}
	enc2, err := kek.EncryptMasterKey(mk2)
	if err != nil {
		t.Fatalf("EncryptMasterKey: %v", err)
	}
	if _, err := st.AddMasterKey("second", enc2); err != nil {
		t.Fatalf("AddMasterKey: %v", err)
	}

	res, err := h(ctxWithScope("mcp_super"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success: %v", res.Content)
	}

	var out []map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 keys, got %d", len(out))
	}
	for _, k := range out {
		if k["id"] == nil || k["id"] == "" {
			t.Errorf("key missing id: %v", k)
		}
		if _, ok := k["active"]; !ok {
			t.Errorf("key missing active field: %v", k)
		}
	}
}

func TestListMasterKeys_ACL_AdminDenied(t *testing.T) {
	st := openTestStore(t)
	h := listMasterKeysHandler(st)

	res, err := h(ctxWithScope("mcp_admin"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("mcp_admin should be denied list_master_keys")
	}
}
