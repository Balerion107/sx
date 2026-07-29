package commands

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/assets"
	"github.com/sleuth-io/sx/v2/internal/lockfile"
	vaultpkg "github.com/sleuth-io/sx/v2/internal/vault"
)

// fakeScopeVault implements just enough of vaultpkg.Vault for the
// scopeLookup type assertion in existingAssetScopes. The other Vault methods
// are not exercised by these unit tests and are intentionally left nil — the
// tests will panic loudly if production code starts depending on them, which
// is the right failure mode for an unintended API expansion.
type fakeScopeVault struct {
	vaultpkg.Vault
	scopes map[string][]lockfile.Scope
}

func (f *fakeScopeVault) ExistingAssetScopes(name string) []lockfile.Scope {
	if s, ok := f.scopes[name]; ok {
		return s
	}
	return nil
}

// resetTrackerForTest writes an empty tracker so each subtest sees a known
// baseline. NewTestEnv sandboxes the cache dir, so this only affects the
// current test.
func resetTrackerForTest(t *testing.T) {
	t.Helper()
	if err := assets.SaveTracker(&assets.Tracker{Version: assets.TrackerFormatVersion}); err != nil {
		t.Fatalf("reset tracker: %v", err)
	}
}

func TestResolveCurrentScopes(t *testing.T) {
	t.Run("vault_has_scopes_returns_them", func(t *testing.T) {
		NewTestEnv(t)
		resetTrackerForTest(t)

		repoScope := []lockfile.Scope{{Repo: "github.com/acme/web"}}
		vault := &fakeScopeVault{scopes: map[string][]lockfile.Scope{"my-skill": repoScope}}

		got := resolveCurrentScopes(vault, "my-skill")
		if len(got) != 1 || got[0].Repo != "github.com/acme/web" {
			t.Errorf("expected vault scopes to be returned verbatim, got %#v", got)
		}
	})

	t.Run("vault_empty_tracker_has_entry_returns_global", func(t *testing.T) {
		NewTestEnv(t)
		resetTrackerForTest(t)
		tracker := &assets.Tracker{Version: assets.TrackerFormatVersion}
		tracker.UpsertAsset(assets.InstalledAsset{Name: "my-skill", Version: "1", Type: "skill"})
		if err := assets.SaveTracker(tracker); err != nil {
			t.Fatalf("save tracker: %v", err)
		}

		vault := &fakeScopeVault{scopes: nil}

		got := resolveCurrentScopes(vault, "my-skill")
		if got == nil {
			t.Fatal("expected non-nil scope slice when tracker knows the asset (global install)")
		}
		if len(got) != 0 {
			t.Errorf("expected empty slice (= global install), got %#v", got)
		}
	})

	t.Run("vault_and_tracker_both_empty_returns_nil", func(t *testing.T) {
		NewTestEnv(t)
		resetTrackerForTest(t)

		vault := &fakeScopeVault{scopes: nil}

		if got := resolveCurrentScopes(vault, "unknown-skill"); got != nil {
			t.Errorf("expected nil for unknown asset, got %#v", got)
		}
	})

	t.Run("vault_authoritative_even_when_tracker_disagrees", func(t *testing.T) {
		// If the vault has authoritative scope info we must NOT override it
		// with a (potentially stale) tracker entry.
		NewTestEnv(t)
		resetTrackerForTest(t)
		tracker := &assets.Tracker{Version: assets.TrackerFormatVersion}
		tracker.UpsertAsset(assets.InstalledAsset{Name: "my-skill", Version: "1", Type: "skill"})
		if err := assets.SaveTracker(tracker); err != nil {
			t.Fatalf("save tracker: %v", err)
		}

		repoScope := []lockfile.Scope{{Repo: "github.com/acme/web"}}
		vault := &fakeScopeVault{scopes: map[string][]lockfile.Scope{"my-skill": repoScope}}

		got := resolveCurrentScopes(vault, "my-skill")
		if len(got) != 1 || got[0].Repo != "github.com/acme/web" {
			t.Errorf("tracker must not override authoritative vault scopes, got %#v", got)
		}
	})
}

func TestResolveInstalledAssetPath(t *testing.T) {
	t.Run("returns_path_when_tracker_and_disk_agree", func(t *testing.T) {
		env := NewTestEnv(t)
		resetTrackerForTest(t)

		// Plant an installed skill at Claude Code's conventional location.
		installedDir := env.MkdirAll(filepath.Join(env.GlobalClaudeDir(), "skills", "my-skill"))

		tracker := &assets.Tracker{Version: assets.TrackerFormatVersion}
		tracker.UpsertAsset(assets.InstalledAsset{
			Name:    "my-skill",
			Version: "1",
			Type:    "skill",
			Clients: []string{"claude-code"},
		})
		if err := assets.SaveTracker(tracker); err != nil {
			t.Fatalf("save tracker: %v", err)
		}

		got := resolveInstalledAssetPath(context.Background(), "my-skill")
		if got != installedDir {
			t.Errorf("expected %q, got %q", installedDir, got)
		}
	})

	t.Run("returns_empty_when_tracker_has_no_entry", func(t *testing.T) {
		NewTestEnv(t)
		resetTrackerForTest(t)

		if got := resolveInstalledAssetPath(context.Background(), "ghost"); got != "" {
			t.Errorf("expected empty path for unknown asset, got %q", got)
		}
	})

	t.Run("returns_empty_when_tracker_entry_points_at_missing_dir", func(t *testing.T) {
		NewTestEnv(t)
		resetTrackerForTest(t)
		// Tracker claims installation but no on-disk dir exists.
		tracker := &assets.Tracker{Version: assets.TrackerFormatVersion}
		tracker.UpsertAsset(assets.InstalledAsset{
			Name:    "stale-skill",
			Version: "1",
			Type:    "skill",
			Clients: []string{"claude-code"},
		})
		if err := assets.SaveTracker(tracker); err != nil {
			t.Fatalf("save tracker: %v", err)
		}

		if got := resolveInstalledAssetPath(context.Background(), "stale-skill"); got != "" {
			t.Errorf("expected empty path when on-disk dir is missing, got %q", got)
		}
	})

	t.Run("returns_empty_when_tracker_entry_has_no_type", func(t *testing.T) {
		// Pre-v3 tracker entries didn't store the asset type; without a type
		// we can't ask the client where the asset lives.
		env := NewTestEnv(t)
		resetTrackerForTest(t)
		env.MkdirAll(filepath.Join(env.GlobalClaudeDir(), "skills", "typeless-skill"))

		tracker := &assets.Tracker{Version: assets.TrackerFormatVersion}
		tracker.UpsertAsset(assets.InstalledAsset{
			Name:    "typeless-skill",
			Version: "1",
			Clients: []string{"claude-code"},
			// Type intentionally omitted.
		})
		if err := assets.SaveTracker(tracker); err != nil {
			t.Fatalf("save tracker: %v", err)
		}

		if got := resolveInstalledAssetPath(context.Background(), "typeless-skill"); got != "" {
			t.Errorf("expected empty path for typeless tracker entry, got %q", got)
		}
	})
}
