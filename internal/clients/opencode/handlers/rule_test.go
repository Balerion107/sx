package handlers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/asset"
	"github.com/sleuth-io/sx/v2/internal/metadata"
)

func TestOpenCodeRuleHandler_InstallWritesFileAndRegistersInstruction(t *testing.T) {
	targetBase := t.TempDir()
	registerPath := filepath.Join(DirRules, "go-style.md")

	meta := &metadata.Metadata{
		Asset: metadata.Asset{
			Name:    "go-style",
			Version: "1.0.0",
			Type:    asset.TypeRule,
		},
		Rule: &metadata.RuleConfig{PromptFile: "RULE.md"},
	}

	zipData := createTestZip(t, map[string]string{
		"metadata.toml": `[asset]
name = "go-style"
type = "rule"
version = "1.0.0"

[rule]
prompt-file = "RULE.md"
`,
		"RULE.md": "# Go Style\n\nUse tabs.\n",
	})

	h := NewRuleHandler(meta, registerPath)
	if err := h.Install(context.Background(), zipData, targetBase); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	ruleFile := filepath.Join(targetBase, DirRules, "go-style.md")
	body, err := os.ReadFile(ruleFile)
	if err != nil {
		t.Fatalf("Rule file should exist: %v", err)
	}
	if string(body) == "" {
		t.Error("Rule file should not be empty")
	}

	// opencode.json should now reference the rule path under `instructions`.
	configBytes, err := os.ReadFile(filepath.Join(targetBase, ConfigFile))
	if err != nil {
		t.Fatalf("opencode.json should exist after install: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(configBytes, &raw); err != nil {
		t.Fatalf("opencode.json should be valid JSON: %v", err)
	}
	instr, ok := raw["instructions"].([]any)
	if !ok {
		t.Fatalf("instructions should be an array, got %T", raw["instructions"])
	}
	if len(instr) != 1 || instr[0] != registerPath {
		t.Errorf("instructions should contain %q, got %v", registerPath, instr)
	}

	installed, msg := h.VerifyInstalled(targetBase)
	if !installed {
		t.Errorf("Should report installed: %s", msg)
	}
}

func TestOpenCodeRuleHandler_Remove(t *testing.T) {
	targetBase := t.TempDir()
	registerPath := filepath.Join(DirRules, "secrets.md")

	meta := &metadata.Metadata{
		Asset: metadata.Asset{
			Name:    "secrets",
			Version: "1.0.0",
			Type:    asset.TypeRule,
		},
		Rule: &metadata.RuleConfig{PromptFile: "RULE.md"},
	}

	zipData := createTestZip(t, map[string]string{
		"RULE.md": "no secrets in code",
	})

	h := NewRuleHandler(meta, registerPath)
	if err := h.Install(context.Background(), zipData, targetBase); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	if err := h.Remove(context.Background(), targetBase); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	ruleFile := filepath.Join(targetBase, DirRules, "secrets.md")
	if _, err := os.Stat(ruleFile); !os.IsNotExist(err) {
		t.Error("Rule file should be removed")
	}

	configBytes, err := os.ReadFile(filepath.Join(targetBase, ConfigFile))
	if err != nil {
		t.Fatalf("opencode.json should still exist after remove: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(configBytes, &raw); err != nil {
		t.Fatalf("opencode.json should be valid JSON: %v", err)
	}
	if instr, ok := raw["instructions"].([]any); ok {
		strs := make([]string, 0, len(instr))
		for _, v := range instr {
			if s, ok := v.(string); ok {
				strs = append(strs, s)
			}
		}
		if slices.Contains(strs, registerPath) {
			t.Errorf("instructions should no longer contain %q, got %v", registerPath, strs)
		}
	}
}

func TestOpenCodeRuleHandler_FallsBackToLowercaseRuleMd(t *testing.T) {
	targetBase := t.TempDir()

	meta := &metadata.Metadata{
		Asset: metadata.Asset{
			Name:    "lower",
			Version: "1.0.0",
			Type:    asset.TypeRule,
		},
		// No Rule config: handler should default to RULE.md, then fall back.
	}

	zipData := createTestZip(t, map[string]string{
		"rule.md": "lowercase ok",
	})

	h := NewRuleHandler(meta, "rules/lower.md")
	if err := h.Install(context.Background(), zipData, targetBase); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(targetBase, DirRules, "lower.md"))
	if err != nil {
		t.Fatalf("Rule file should exist: %v", err)
	}
	if string(body) != "lowercase ok" {
		t.Errorf("Rule content mismatch: got %q", string(body))
	}
}

func TestOpenCodeRuleHandler_ExplicitPromptFileMissingSurfacesName(t *testing.T) {
	targetBase := t.TempDir()

	meta := &metadata.Metadata{
		Asset: metadata.Asset{
			Name:    "policy",
			Version: "1.0.0",
			Type:    asset.TypeRule,
		},
		// Explicitly configured prompt-file. The lowercase rule.md
		// fallback only applies when the configured file is the default
		// RULE.md — for an explicit filename the install must surface
		// the configured name in the error so asset authors can find
		// the missing file in their package.
		Rule: &metadata.RuleConfig{PromptFile: "policy.md"},
	}

	zipData := createTestZip(t, map[string]string{
		"metadata.toml": `[asset]
name = "policy"
version = "1.0.0"
type = "rule"

[rule]
prompt-file = "policy.md"
`,
		"rule.md": "ignored fallback content",
	})

	h := NewRuleHandler(meta, "rules/policy.md")
	err := h.Install(context.Background(), zipData, targetBase)
	if err == nil {
		t.Fatal("Install should fail when explicit prompt-file is missing")
	}
	if !strings.Contains(err.Error(), "policy.md") {
		t.Errorf("error should mention the configured filename %q, got: %v", "policy.md", err)
	}
}

func TestAddInstruction_DeduplicatesEntries(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "opencode.json")

	if err := AddInstruction(configPath, "rules/a.md"); err != nil {
		t.Fatalf("first AddInstruction failed: %v", err)
	}
	if err := AddInstruction(configPath, "rules/a.md"); err != nil {
		t.Fatalf("second AddInstruction failed: %v", err)
	}

	cfg, err := ReadOpenCodeConfig(configPath)
	if err != nil {
		t.Fatalf("ReadOpenCodeConfig failed: %v", err)
	}
	if got := len(cfg.Instructions); got != 1 {
		t.Errorf("Expected 1 instruction after dedup, got %d (%v)", got, cfg.Instructions)
	}
}

func TestAddInstruction_PreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "opencode.json")

	// Pre-write a config with a field sx doesn't model.
	if err := os.WriteFile(configPath, []byte(`{"$schema":"https://opencode.ai/config.json","model":"anthropic/claude-sonnet-4-6"}`), 0644); err != nil {
		t.Fatalf("seed write failed: %v", err)
	}

	if err := AddInstruction(configPath, "rules/foo.md"); err != nil {
		t.Fatalf("AddInstruction failed: %v", err)
	}

	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if raw["model"] != "anthropic/claude-sonnet-4-6" {
		t.Errorf("model should be preserved, got %v", raw["model"])
	}
}

// TestAddInstruction_DuplicateIsByteForByteNoop pins the polite-write
// behavior: an AddInstruction call for a path already in the file must
// not touch the file (no mtime change, no key-order normalization, no
// $schema injection). This prevents a stray no-op install from
// producing a churn-only git diff in the user's checked-in config.
func TestAddInstruction_DuplicateIsByteForByteNoop(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "opencode.json")

	// Hand-formatted config with non-alphabetical key order and no
	// $schema: if AddInstruction re-writes the file on a no-op, both
	// would change.
	original := `{
  "theme": "tokyonight",
  "instructions": [
    "rules/already-here.md"
  ]
}`
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("seed write failed: %v", err)
	}

	if err := AddInstruction(configPath, "rules/already-here.md"); err != nil {
		t.Fatalf("AddInstruction failed: %v", err)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(got) != original {
		t.Errorf("AddInstruction on duplicate should leave file untouched.\nwant:\n%s\n got:\n%s", original, string(got))
	}
}

// TestRemoveInstruction_MissingIsByteForByteNoop mirrors the Add case:
// removing an unregistered instruction must leave the file untouched.
func TestRemoveInstruction_MissingIsByteForByteNoop(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "opencode.json")

	original := `{
  "theme": "tokyonight",
  "instructions": [
    "rules/other.md"
  ]
}`
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("seed write failed: %v", err)
	}

	if err := RemoveInstruction(configPath, "rules/never-installed.md"); err != nil {
		t.Fatalf("RemoveInstruction failed: %v", err)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(got) != original {
		t.Errorf("RemoveInstruction on missing entry should leave file untouched.\nwant:\n%s\n got:\n%s", original, string(got))
	}
}

// TestConfigFilePath_PrefersJsoncWhenOnlyJsoncExists verifies sx writes
// back to the user's existing opencode.jsonc rather than materializing a
// separate opencode.json next to it.
func TestConfigFilePath_PrefersJsoncWhenOnlyJsoncExists(t *testing.T) {
	dir := t.TempDir()
	jsoncPath := filepath.Join(dir, ConfigFileJSONC)
	if err := os.WriteFile(jsoncPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("seed jsonc failed: %v", err)
	}

	got := ConfigFilePath(dir)
	if got != jsoncPath {
		t.Errorf("ConfigFilePath should prefer existing .jsonc, got %s", got)
	}
}

// TestConfigFilePath_PrefersJsonWhenBothExist documents the tie-break:
// if both files exist, sx writes to .json. (Both existing is already a
// broken setup; sx doesn't try to merge or repair it.)
func TestConfigFilePath_PrefersJsonWhenBothExist(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, ConfigFile)
	jsoncPath := filepath.Join(dir, ConfigFileJSONC)
	if err := os.WriteFile(jsonPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("seed json failed: %v", err)
	}
	if err := os.WriteFile(jsoncPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("seed jsonc failed: %v", err)
	}

	got := ConfigFilePath(dir)
	if got != jsonPath {
		t.Errorf("ConfigFilePath should prefer .json when both exist, got %s", got)
	}
}

// TestConfigFilePath_DefaultsToJsonWhenNeitherExists pins the
// new-install behavior — fresh targets get .json.
func TestConfigFilePath_DefaultsToJsonWhenNeitherExists(t *testing.T) {
	dir := t.TempDir()
	got := ConfigFilePath(dir)
	if got != filepath.Join(dir, ConfigFile) {
		t.Errorf("ConfigFilePath should default to .json when neither exists, got %s", got)
	}
}
