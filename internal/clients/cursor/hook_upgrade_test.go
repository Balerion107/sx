package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/clients/cursor/handlers"
	"github.com/sleuth-io/sx/v2/internal/clipath"
)

// The duplicate this guards against needs a *stale absolute* path, which is what
// an app move or a CLI reinstall leaves behind. A bare-prefix predicate does not
// recognize it as sx's own, so installing again appends a second hook instead of
// rewriting the first — the install then runs twice per prompt and the stale
// entry keeps failing. Both legacy forms are seeded so first-match-wins is
// covered too.
func TestInstallBeforeSubmitPromptHookUpgradesInPlace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cursorDir := filepath.Join(home, handlers.ConfigDir)
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(cursorDir, "hooks.json")

	// Seed the legacy form, alongside somebody else's hook that must survive.
	seed := map[string]any{
		"version": 1,
		"hooks": map[string]any{
			"beforeSubmitPrompt": []any{
				map[string]any{"command": "/previous/location/sx install --hook-mode --client=cursor"},
				map[string]any{"command": "sx install --hook-mode --client=cursor"},
				map[string]any{"command": "my-own-linter --check"},
			},
		},
	}
	data, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Client{}
	if err := c.installBeforeSubmitPromptHook(); err != nil {
		t.Fatalf("installBeforeSubmitPromptHook: %v", err)
	}

	out, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("hooks.json is not valid JSON after install: %v", err)
	}
	hooks, _ := got["hooks"].(map[string]any)
	entries, _ := hooks["beforeSubmitPrompt"].([]any)

	managed := 0
	foreignKept := false
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		cmd, _ := m["command"].(string)
		if clipath.Managed(cmd, "install") {
			managed++
		}
		if cmd == "my-own-linter --check" {
			foreignKept = true
		}
	}
	if managed != 1 {
		t.Fatalf("want exactly one sx install hook after upgrade, got %d in %v", managed, entries)
	}
	if !foreignKept {
		t.Fatal("an unrelated hook was dropped")
	}
}
