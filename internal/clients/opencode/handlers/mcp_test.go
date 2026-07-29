package handlers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/asset"
	"github.com/sleuth-io/sx/v2/internal/metadata"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return result
}

func TestOpenCodeMCPHandler_ConfigOnlyLocal_Install(t *testing.T) {
	targetBase := t.TempDir()

	meta := &metadata.Metadata{
		Asset: metadata.Asset{
			Name:    "demo-mcp",
			Version: "1.0.0",
			Type:    asset.TypeMCP,
		},
		MCP: &metadata.MCPConfig{
			Command: "npx",
			Args:    []string{"-y", "@example/demo-mcp"},
			Env:     map[string]string{"TOKEN": "secret"},
		},
	}

	zipData := createTestZip(t, map[string]string{
		"metadata.toml": `[asset]
name = "demo-mcp"
version = "1.0.0"
type = "mcp"

[mcp]
command = "npx"
args = ["-y", "@example/demo-mcp"]
`,
	})

	h := NewMCPHandler(meta)
	if err := h.Install(context.Background(), zipData, targetBase); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	cfg := readJSON(t, filepath.Join(targetBase, ConfigFile))

	if cfg["$schema"] != "https://opencode.ai/config.json" {
		t.Errorf("expected $schema reference, got %v", cfg["$schema"])
	}

	mcp, ok := cfg["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp key missing or wrong shape: %#v", cfg["mcp"])
	}
	server, ok := mcp["demo-mcp"].(map[string]any)
	if !ok {
		t.Fatalf("demo-mcp entry missing")
	}

	if server["type"] != "local" {
		t.Errorf("type = %v, want local", server["type"])
	}
	if server["enabled"] != true {
		t.Errorf("enabled = %v, want true", server["enabled"])
	}
	cmd, ok := server["command"].([]any)
	if !ok || len(cmd) != 3 {
		t.Fatalf("command array wrong: %#v", server["command"])
	}
	if cmd[0] != "npx" || cmd[1] != "-y" || cmd[2] != "@example/demo-mcp" {
		t.Errorf("command array contents wrong: %v", cmd)
	}
	env, ok := server["environment"].(map[string]any)
	if !ok {
		t.Fatalf("environment missing")
	}
	if env["TOKEN"] != "secret" {
		t.Errorf("env TOKEN = %v, want secret", env["TOKEN"])
	}

	installed, msg := h.VerifyInstalled(targetBase)
	if !installed {
		t.Errorf("Should be installed: %s", msg)
	}
}

func TestOpenCodeMCPHandler_RemoteInstall(t *testing.T) {
	targetBase := t.TempDir()

	meta := &metadata.Metadata{
		Asset: metadata.Asset{
			Name:    "remote-mcp",
			Version: "1.0.0",
			Type:    asset.TypeMCP,
		},
		MCP: &metadata.MCPConfig{
			Transport: "sse",
			URL:       "https://example.com/mcp/sse",
		},
	}

	zipData := createTestZip(t, map[string]string{
		"metadata.toml": `[asset]
name = "remote-mcp"
version = "1.0.0"
type = "mcp"

[mcp]
transport = "sse"
url = "https://example.com/mcp/sse"
`,
	})

	h := NewMCPHandler(meta)
	if err := h.Install(context.Background(), zipData, targetBase); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	cfg := readJSON(t, filepath.Join(targetBase, ConfigFile))
	mcp := cfg["mcp"].(map[string]any)
	server := mcp["remote-mcp"].(map[string]any)

	if server["type"] != "remote" {
		t.Errorf("type = %v, want remote", server["type"])
	}
	if server["url"] != "https://example.com/mcp/sse" {
		t.Errorf("url = %v, want https://example.com/mcp/sse", server["url"])
	}
	if _, hasCmd := server["command"]; hasCmd {
		t.Errorf("remote should not have command")
	}
}

func TestOpenCodeMCPHandler_PreservesOtherFields(t *testing.T) {
	targetBase := t.TempDir()
	configPath := filepath.Join(targetBase, ConfigFile)

	// Pre-populate config with unrelated user settings
	initial := `{
  "$schema": "https://opencode.ai/config.json",
  "theme": "tokyonight",
  "model": "anthropic/claude-sonnet-4-20250514",
  "mcp": {
    "existing": {"type": "local", "enabled": true, "command": ["foo"]}
  }
}`
	if err := os.WriteFile(configPath, []byte(initial), 0644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	meta := &metadata.Metadata{
		Asset: metadata.Asset{Name: "new-mcp", Version: "1.0.0", Type: asset.TypeMCP},
		MCP:   &metadata.MCPConfig{Command: "node", Args: []string{"server.js"}},
	}
	zipData := createTestZip(t, map[string]string{
		"metadata.toml": `[asset]
name = "new-mcp"
version = "1.0.0"
type = "mcp"

[mcp]
command = "node"
args = ["server.js"]
`,
	})

	h := NewMCPHandler(meta)
	if err := h.Install(context.Background(), zipData, targetBase); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	cfg := readJSON(t, configPath)
	if cfg["theme"] != "tokyonight" {
		t.Errorf("theme was clobbered: %v", cfg["theme"])
	}
	if cfg["model"] != "anthropic/claude-sonnet-4-20250514" {
		t.Errorf("model was clobbered: %v", cfg["model"])
	}
	mcp := cfg["mcp"].(map[string]any)
	if _, ok := mcp["existing"]; !ok {
		t.Errorf("existing MCP entry was removed")
	}
	if _, ok := mcp["new-mcp"]; !ok {
		t.Errorf("new MCP entry was not added")
	}
}

func TestOpenCodeMCPHandler_RemoveNoConfig(t *testing.T) {
	targetBase := t.TempDir()

	meta := &metadata.Metadata{
		Asset: metadata.Asset{Name: "ghost", Version: "1.0.0", Type: asset.TypeMCP},
		MCP:   &metadata.MCPConfig{Command: "x"},
	}
	h := NewMCPHandler(meta)
	if err := h.Remove(context.Background(), targetBase); err != nil {
		t.Fatalf("Remove on empty target should be a no-op, got: %v", err)
	}

	// And we must not have created an opencode.json on the side
	if _, err := os.Stat(filepath.Join(targetBase, ConfigFile)); !os.IsNotExist(err) {
		t.Errorf("Remove should not materialize a config file when none existed (err=%v)", err)
	}
}

func TestOpenCodeMCPHandler_RemoteInstall_DropsEnv(t *testing.T) {
	targetBase := t.TempDir()

	meta := &metadata.Metadata{
		Asset: metadata.Asset{
			Name:    "remote-mcp",
			Version: "1.0.0",
			Type:    asset.TypeMCP,
		},
		MCP: &metadata.MCPConfig{
			Transport: "sse",
			URL:       "https://example.com/mcp/sse",
			Env:       map[string]string{"TOKEN": "unsupported-on-remote"},
		},
	}

	zipData := createTestZip(t, map[string]string{
		"metadata.toml": `[asset]
name = "remote-mcp"
version = "1.0.0"
type = "mcp"

[mcp]
transport = "sse"
url = "https://example.com/mcp/sse"
`,
	})

	h := NewMCPHandler(meta)
	if err := h.Install(context.Background(), zipData, targetBase); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	cfg := readJSON(t, filepath.Join(targetBase, ConfigFile))
	server := cfg["mcp"].(map[string]any)["remote-mcp"].(map[string]any)

	// OpenCode's remote MCP schema has no `environment` field. sx must
	// not emit it, even when the metadata carries env vars — there is
	// nowhere to put them on a remote entry.
	if _, hasEnv := server["environment"]; hasEnv {
		t.Errorf("remote MCP should not write environment field, got: %v", server["environment"])
	}
}

func TestOpenCodeMCPHandler_RemoveLastMCP(t *testing.T) {
	targetBase := t.TempDir()
	configPath := filepath.Join(targetBase, ConfigFile)

	initial := `{
  "$schema": "https://opencode.ai/config.json",
  "theme": "tokyonight",
  "mcp": {
    "only": {"type": "local", "enabled": true, "command": ["foo"]}
  }
}`
	if err := os.WriteFile(configPath, []byte(initial), 0644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	meta := &metadata.Metadata{
		Asset: metadata.Asset{Name: "only", Version: "1.0.0", Type: asset.TypeMCP},
		MCP:   &metadata.MCPConfig{Command: "foo"},
	}
	h := NewMCPHandler(meta)
	if err := h.Remove(context.Background(), targetBase); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	cfg := readJSON(t, configPath)
	// Unrelated user fields must survive.
	if cfg["theme"] != "tokyonight" {
		t.Errorf("theme was clobbered when removing last MCP: %v", cfg["theme"])
	}
	// The mcp map can either be absent or empty; sx writes it as absent
	// when no entries remain. Either is acceptable to OpenCode; pin the
	// current behavior so a future change to it is intentional.
	if mcp, ok := cfg["mcp"]; ok {
		if m, isMap := mcp.(map[string]any); !isMap || len(m) != 0 {
			t.Errorf("mcp should be absent or empty after removing last entry, got: %#v", mcp)
		}
	}
}

func TestOpenCodeMCPHandler_Remove(t *testing.T) {
	targetBase := t.TempDir()
	configPath := filepath.Join(targetBase, ConfigFile)

	initial := `{
  "mcp": {
    "keep": {"type": "local", "enabled": true, "command": ["foo"]},
    "remove-me": {"type": "local", "enabled": true, "command": ["bar"]}
  }
}`
	if err := os.WriteFile(configPath, []byte(initial), 0644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	meta := &metadata.Metadata{
		Asset: metadata.Asset{Name: "remove-me", Version: "1.0.0", Type: asset.TypeMCP},
		MCP:   &metadata.MCPConfig{Command: "bar"},
	}
	h := NewMCPHandler(meta)
	if err := h.Remove(context.Background(), targetBase); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	cfg := readJSON(t, configPath)
	mcp := cfg["mcp"].(map[string]any)
	if _, ok := mcp["remove-me"]; ok {
		t.Error("remove-me should be gone")
	}
	if _, ok := mcp["keep"]; !ok {
		t.Error("keep should still exist")
	}
}

// TestReadOpenCodeConfig_RejectsMalformedMCPType prevents a regression
// where a user's malformed `mcp` value (string instead of object) was
// silently dropped by a failed type assertion, and the next write
// overwrote it. Now we surface a typed error so callers can fail loudly.
func TestReadOpenCodeConfig_RejectsMalformedMCPType(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(configPath, []byte(`{"mcp": "garbage"}`), 0644); err != nil {
		t.Fatalf("seed write failed: %v", err)
	}

	_, err := ReadOpenCodeConfig(configPath)
	if err == nil {
		t.Fatal("expected error for malformed mcp value")
	}
	if !strings.Contains(err.Error(), "`mcp` must be an object") {
		t.Errorf("error should explain the type mismatch, got: %v", err)
	}
}

// TestReadOpenCodeConfig_RejectsMalformedInstructionsType mirrors the
// mcp test for the `instructions` key.
func TestReadOpenCodeConfig_RejectsMalformedInstructionsType(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(configPath, []byte(`{"instructions": "single-string"}`), 0644); err != nil {
		t.Fatalf("seed write failed: %v", err)
	}

	_, err := ReadOpenCodeConfig(configPath)
	if err == nil {
		t.Fatal("expected error for malformed instructions value")
	}
	if !strings.Contains(err.Error(), "`instructions` must be an array") {
		t.Errorf("error should explain the type mismatch, got: %v", err)
	}
}

// TestReadOpenCodeConfig_RejectsNonStringInstructionEntry guards against
// silently dropping non-string entries inside the instructions array —
// the previous code would drop them and let the next write delete them.
func TestReadOpenCodeConfig_RejectsNonStringInstructionEntry(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(configPath, []byte(`{"instructions": ["ok.md", 42]}`), 0644); err != nil {
		t.Fatalf("seed write failed: %v", err)
	}

	_, err := ReadOpenCodeConfig(configPath)
	if err == nil {
		t.Fatal("expected error for non-string instruction entry")
	}
	if !strings.Contains(err.Error(), "`instructions[1]`") {
		t.Errorf("error should point at the offending index, got: %v", err)
	}
}

// TestReadOpenCodeConfig_TreatsNullAsAbsent pins the convention that a
// JSON null at either key is treated as "key absent" rather than a
// malformed value — that matches the standard JSON convention and lets
// users opt out of a key without having to delete it.
func TestReadOpenCodeConfig_TreatsNullAsAbsent(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(configPath, []byte(`{"mcp": null, "instructions": null}`), 0644); err != nil {
		t.Fatalf("seed write failed: %v", err)
	}

	cfg, err := ReadOpenCodeConfig(configPath)
	if err != nil {
		t.Fatalf("null values should be treated as absent, got error: %v", err)
	}
	if len(cfg.MCP) != 0 {
		t.Errorf("MCP should be empty for null, got: %v", cfg.MCP)
	}
	if len(cfg.Instructions) != 0 {
		t.Errorf("Instructions should be empty for null, got: %v", cfg.Instructions)
	}
}
