package claude_code

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/clipath"
)

// settingsWithHook writes a .claude/settings.json whose named hook array holds
// the given entries, and returns the claude dir.
func settingsWithHook(t *testing.T, event string, entries []any) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"hooks": map[string]any{event: entries}}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// readHookArray returns the named hook array from a written settings.json.
func readHookArray(t *testing.T, claudeDir, event string) []any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}
	hooks, _ := out["hooks"].(map[string]any)
	entries, _ := hooks[event].([]any)
	return entries
}

// commandsIn flattens every command string across all entries.
func commandsIn(entries []any) []string {
	var out []string
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		arr, _ := m["hooks"].([]any)
		for _, h := range arr {
			if hm, ok := h.(map[string]any); ok {
				if c, ok := hm["command"].(string); ok {
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// assertUpdated fails when the stale fixture command survived, which would mean
// the install returned early and the surrounding assertion proved nothing.
func assertUpdated(t *testing.T, entries []any) {
	t.Helper()
	for _, c := range commandsIn(entries) {
		if c == "/previous/sx install --hook-mode --client=claude-code" {
			t.Fatalf("stale command was not updated, so this case exercised nothing: %v", entries)
		}
	}
}

func countManaged(cmds []string) int {
	n := 0
	for _, c := range cmds {
		if clipath.Managed(c, "install") {
			n++
		}
	}
	return n
}

// These are the entry shapes this logic has repeatedly gotten wrong: sx's
// command alone, sharing an entry with a user's hook, carrying a user-set
// matcher, carrying its own timeout, and duplicated. Each must end with exactly
// one managed command and lose nothing of the user's.
func TestInstallSessionStartHookEntryShapes(t *testing.T) {
	cases := []struct {
		name    string
		entries []any
		// assert runs against the resulting SessionStart array.
		assert func(t *testing.T, entries []any)
	}{
		{
			name: "stale command sharing an entry with a user hook",
			entries: []any{
				map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": "/previous/sx install --hook-mode --client=claude-code"},
					map[string]any{"type": "command", "command": "my-linter"},
				}},
			},
			assert: func(t *testing.T, entries []any) {
				assertUpdated(t, entries)
				cmds := commandsIn(entries)
				if countManaged(cmds) != 1 {
					t.Fatalf("want one managed command, got %v", cmds)
				}
				found := false
				for _, c := range cmds {
					if c == "my-linter" {
						found = true
					}
				}
				if !found {
					t.Fatalf("user hook was dropped: %v", cmds)
				}
			},
		},
		{
			name: "user-set matcher must survive",
			entries: []any{
				map[string]any{
					"matcher": "startup",
					// A stale absolute path, deliberately: the bare form is what
					// CommandOrBare produces when no CLI resolves, which would hit
					// the already-current early return and leave this test
					// asserting nothing on CI.
					"hooks": []any{map[string]any{"type": "command", "command": "/previous/sx install --hook-mode --client=claude-code"}},
				},
			},
			assert: func(t *testing.T, entries []any) {
				assertUpdated(t, entries)
				for _, e := range entries {
					m, _ := e.(map[string]any)
					if m["matcher"] == "startup" {
						return
					}
				}
				t.Fatalf("matcher lost; hook would fire more widely than configured: %v", entries)
			},
		},
		{
			name: "user-set timeout on our own command must survive",
			entries: []any{
				map[string]any{"hooks": []any{
					// Stale absolute path for the same reason as the matcher case.
					map[string]any{"type": "command", "command": "/previous/sx install --hook-mode --client=claude-code", "timeout": float64(90)},
				}},
			},
			assert: func(t *testing.T, entries []any) {
				assertUpdated(t, entries)
				for _, e := range entries {
					m, _ := e.(map[string]any)
					arr, _ := m["hooks"].([]any)
					for _, h := range arr {
						hm, _ := h.(map[string]any)
						if hm["timeout"] == float64(90) {
							return
						}
					}
				}
				t.Fatalf("timeout lost: %v", entries)
			},
		},
		{
			name: "a shared entry keeps its own matcher",
			entries: []any{
				map[string]any{
					"matcher": "startup",
					"hooks": []any{
						map[string]any{"type": "command", "command": "/previous/sx install --hook-mode --client=claude-code"},
						map[string]any{"type": "command", "command": "my-linter"},
					},
				},
			},
			assert: func(t *testing.T, entries []any) {
				assertUpdated(t, entries)
				// sx's command must still be present exactly once — the split is
				// worthless if it simply lost it.
				if n := countManaged(commandsIn(entries)); n != 1 {
					t.Fatalf("want sx's command exactly once after the split, got %d in %v", n, entries)
				}
				userOK, ourMatcher := false, ""
				for _, e := range entries {
					m, _ := e.(map[string]any)
					cmds := commandsIn([]any{m})
					if len(cmds) == 1 && cmds[0] == "my-linter" && m["matcher"] == "startup" {
						userOK = true
					}
					if len(cmds) == 1 && countManaged(cmds) == 1 {
						ourMatcher, _ = m["matcher"].(string)
					}
				}
				if !userOK {
					t.Fatalf("user hook lost its matcher or is still sharing with sx: %v", entries)
				}
				// And our split-out copy must inherit that matcher, or the hook
				// starts firing on more session sources than were configured.
				if ourMatcher != "startup" {
					t.Fatalf("sx's split entry did not inherit the user's matcher (got %q): %v", ourMatcher, entries)
				}
			},
		},
		{
			name: "duplicates collapse to one",
			entries: []any{
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "sx install --hook-mode --client=claude-code"}}},
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/previous/sx install --hook-mode --client=claude-code"}}},
			},
			assert: func(t *testing.T, entries []any) {
				if n := countManaged(commandsIn(entries)); n != 1 {
					t.Fatalf("want one managed command after collapse, got %d in %v", n, entries)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := settingsWithHook(t, "SessionStart", tc.entries)
			if err := installSessionStartHook(dir); err != nil {
				t.Fatalf("installSessionStartHook: %v", err)
			}
			tc.assert(t, readHookArray(t, dir, "SessionStart"))
		})
	}
}

// Uninstall has to be the mirror of install: it strips only sx's command, and
// it must actually write the file when that is all it changed — comparing entry
// counts missed exactly this case and left the hook in place.
func TestRemoveSxHooksStripsOnlyOurCommandAndReportsIt(t *testing.T) {
	entries := []any{
		map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": "sx install --hook-mode --client=claude-code"},
			map[string]any{"type": "command", "command": "my-linter"},
		}},
	}
	filtered, removed := removeSxHooks(entries, "sx install --hook-mode --client=claude-code", "install")
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 — the caller writes the file only when this is > 0", removed)
	}
	cmds := commandsIn(filtered)
	if countManaged(cmds) != 0 {
		t.Fatalf("sx command survived uninstall: %v", cmds)
	}
	if len(cmds) != 1 || cmds[0] != "my-linter" {
		t.Fatalf("user hook not preserved: %v", cmds)
	}
}

// PostToolUse carries sx's own matcher, so unlike SessionStart it must be
// re-asserted on update — a stale or hand-edited one otherwise persists and the
// hook reports on the wrong events forever.
func TestInstallUsageReportingHookShapes(t *testing.T) {
	t.Run("re-asserts sx's own matcher", func(t *testing.T) {
		dir := settingsWithHook(t, "PostToolUse", []any{
			map[string]any{
				"matcher": "Bash",
				"hooks":   []any{map[string]any{"type": "command", "command": "/previous/sx report-usage --client=claude-code"}},
			},
		})
		if err := installUsageReportingHook(dir); err != nil {
			t.Fatalf("installUsageReportingHook: %v", err)
		}
		entries := readHookArray(t, dir, "PostToolUse")
		// The stale command must be gone and exactly one of ours left, or the
		// matcher could be "re-asserted" on a freshly appended entry while the
		// stale one lingers.
		cmds := commandsIn(entries)
		managed := 0
		for _, c := range cmds {
			if clipath.Managed(c, "report-usage") {
				managed++
			}
			if c == "/previous/sx report-usage --client=claude-code" {
				t.Fatalf("stale command survived: %v", cmds)
			}
		}
		if managed != 1 {
			t.Fatalf("want exactly one managed command, got %d in %v", managed, cmds)
		}
		for _, e := range entries {
			m, _ := e.(map[string]any)
			if m["matcher"] == postToolUseMatcher {
				return
			}
		}
		t.Fatalf("sx's matcher was not re-asserted: %v", entries)
	})

	t.Run("collapses duplicates and keeps a user hook", func(t *testing.T) {
		dir := settingsWithHook(t, "PostToolUse", []any{
			map[string]any{"hooks": []any{
				map[string]any{"type": "command", "command": "/previous/sx report-usage --client=claude-code"},
				map[string]any{"type": "command", "command": "my-telemetry"},
			}},
			map[string]any{"hooks": []any{
				map[string]any{"type": "command", "command": "sx report-usage --client=claude-code"},
			}},
		})
		if err := installUsageReportingHook(dir); err != nil {
			t.Fatalf("installUsageReportingHook: %v", err)
		}
		cmds := commandsIn(readHookArray(t, dir, "PostToolUse"))
		managed := 0
		userKept := false
		for _, c := range cmds {
			if clipath.Managed(c, "report-usage") {
				managed++
			}
			if c == "my-telemetry" {
				userKept = true
			}
		}
		if managed != 1 {
			t.Fatalf("want one managed report-usage command, got %d in %v", managed, cmds)
		}
		if !userKept {
			t.Fatalf("user hook was dropped: %v", cmds)
		}
	})
}

// A stale matcher on sx's own hook must be corrected even when the command is
// already current — the early return used to discard that.
func TestPostToolUseCorrectsStaleMatcherWithCurrentCommand(t *testing.T) {
	current := clipath.CommandOrBare("report-usage", "--client=claude-code")
	dir := settingsWithHook(t, "PostToolUse", []any{
		map[string]any{
			"matcher": "Bash",
			"hooks":   []any{map[string]any{"type": "command", "command": current}},
		},
	})
	if err := installUsageReportingHook(dir); err != nil {
		t.Fatalf("installUsageReportingHook: %v", err)
	}
	for _, e := range readHookArray(t, dir, "PostToolUse") {
		m, _ := e.(map[string]any)
		if m["matcher"] == postToolUseMatcher {
			return
		}
	}
	t.Fatal("a stale matcher on sx's own hook was left uncorrected")
}

// A duplicate inside a user's matcher'd entry must not pull that matcher onto
// sx's own entry — the mirror of the widening that inheritance prevents.
func TestSessionStartDoesNotNarrowOwnEntryFromDuplicate(t *testing.T) {
	dir := settingsWithHook(t, "SessionStart", []any{
		// sx's own entry, no matcher: fires on every session source.
		map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": "/previous/sx install --hook-mode --client=claude-code"},
		}},
		// A duplicate of ours sharing the user's narrow entry with their own
		// hook. It has to be shared: inheritance only reads a matcher off an
		// entry sx is splitting, so without the user's hook here the entry is a
		// plain duplicate and this test could not fail.
		map[string]any{"matcher": "startup", "hooks": []any{
			map[string]any{"type": "command", "command": "sx install --hook-mode --client=claude-code"},
			map[string]any{"type": "command", "command": "my-linter"},
		}},
	})
	if err := installSessionStartHook(dir); err != nil {
		t.Fatalf("installSessionStartHook: %v", err)
	}
	entries := readHookArray(t, dir, "SessionStart")
	if n := countManaged(commandsIn(entries)); n != 1 {
		t.Fatalf("want one managed command, got %d in %v", n, entries)
	}
	for _, e := range entries {
		m, _ := e.(map[string]any)
		if countManaged(commandsIn([]any{m})) == 1 {
			if got, _ := m["matcher"].(string); got != "" {
				t.Fatalf("sx's own entry gained matcher %q, narrowing when it fires", got)
			}
			return
		}
	}
	t.Fatalf("sx's command vanished: %v", entries)
}
