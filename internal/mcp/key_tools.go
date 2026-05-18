package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/panda/llm-gateway/internal/encrypt"
	"github.com/panda/llm-gateway/internal/store"
)

// RegisterKeyTools adds the 4 master-key management tools to s (v0.3 T17).
// All tools require mcp_super scope.
func RegisterKeyTools(s *mcpserver.MCPServer, st *store.Store) {
	s.AddTool(
		mcplib.NewTool("rotate_master_key",
			mcplib.WithDescription("Generate a new master key, encrypt it with the KEK (LLM_GATEWAY_KEK), and store it in the DB. Does not re-encrypt existing provider keys — run rekey_providers afterwards."),
			mcplib.WithString("label", mcplib.Description("Optional human-readable label for this key, e.g. 'rotation-2026-05'")),
		),
		rotateMasterKeyHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("rekey_providers",
			mcplib.WithDescription("Re-encrypt all provider API keys using the current active master key. Requires LLM_GATEWAY_KEK to be set."),
		),
		rekeyProvidersHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("retire_master_key",
			mcplib.WithDescription("Mark a master key as retired by ID. Retired keys are no longer returned as the active key."),
			mcplib.WithString("key_id", mcplib.Required(), mcplib.Description("UUID of the master key to retire")),
		),
		retireMasterKeyHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("list_master_keys",
			mcplib.WithDescription("List all master key records (IDs, labels, active status). Does not return key material."),
		),
		listMasterKeysHandler(st),
	)
}

func rotateMasterKeyHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "rotate_master_key"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		kekStr := os.Getenv("LLM_GATEWAY_KEK")
		if kekStr == "" {
			return mcplib.NewToolResultError("LLM_GATEWAY_KEK not set — cannot rotate master key"), nil
		}
		kek := encrypt.NewKEKFromString(kekStr)

		newMK, err := encrypt.GenerateMasterKey()
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("generate master key: %v", err)), nil
		}
		encB64, err := kek.EncryptMasterKey(newMK)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("encrypt master key: %v", err)), nil
		}
		label := req.GetString("label", "")
		id, err := st.AddMasterKey(label, encB64)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("store master key: %v", err)), nil
		}
		out, _ := json.Marshal(map[string]any{"key_id": id, "status": "rotated"})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func rekeyProvidersHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "rekey_providers"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		kekStr := os.Getenv("LLM_GATEWAY_KEK")
		if kekStr == "" {
			return mcplib.NewToolResultError("LLM_GATEWAY_KEK not set — cannot rekey providers"), nil
		}
		kek := encrypt.NewKEKFromString(kekStr)
		n, err := st.RekeyProviders(kek)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return mcplib.NewToolResultError("no active master key found — run rotate_master_key first"), nil
			}
			return mcplib.NewToolResultError(fmt.Sprintf("rekey_providers failed: %v", err)), nil
		}
		out, _ := json.Marshal(map[string]any{"rekeyed": n, "status": "ok"})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func retireMasterKeyHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "retire_master_key"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		keyID := req.GetString("key_id", "")
		if keyID == "" {
			return mcplib.NewToolResultError("required: key_id"), nil
		}
		if err := st.RetireMasterKey(keyID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return mcplib.NewToolResultError("master key not found or already retired: " + keyID), nil
			}
			return mcplib.NewToolResultError(fmt.Sprintf("retire_master_key failed: %v", err)), nil
		}
		out, _ := json.Marshal(map[string]any{"status": "retired", "key_id": keyID})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func listMasterKeysHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "list_master_keys"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		keys, err := st.ListMasterKeys()
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("list_master_keys failed: %v", err)), nil
		}
		type row struct {
			ID        string `json:"id"`
			Label     string `json:"label,omitempty"`
			CreatedAt int64  `json:"created_at"`
			Active    bool   `json:"active"`
		}
		out := make([]row, len(keys))
		for i, k := range keys {
			out[i] = row{
				ID:        k.ID,
				Label:     k.Label,
				CreatedAt: k.CreatedAt.UnixMilli(),
				Active:    k.Active,
			}
		}
		b, _ := json.Marshal(out)
		return mcplib.NewToolResultText(string(b)), nil
	}
}
