package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/asset"
	"github.com/sleuth-io/sx/v2/internal/clients"
	"github.com/sleuth-io/sx/v2/internal/lockfile"
	"github.com/sleuth-io/sx/v2/internal/scope"
)

// scopedLockFile builds a lock file with one global asset and two
// assets scoped to the given repos.
func scopedLockFile(repos ...string) *lockfile.LockFile {
	lf := &lockfile.LockFile{LockVersion: "1"}
	lf.Assets = append(lf.Assets, lockfile.Asset{
		Name: "global-skill", Version: "1.0.0", Type: asset.TypeSkill,
	})
	for i, repo := range repos {
		lf.Assets = append(lf.Assets, lockfile.Asset{
			Name:    "scoped-" + string(rune('a'+i)),
			Version: "1.0.0",
			Type:    asset.TypeSkill,
			Scopes:  []lockfile.Scope{{Repo: repo}},
		})
	}
	return lf
}

func TestFilterAssetsByScope_CountsScopeSkips(t *testing.T) {
	clientList := []clients.Client{&stubClient{id: "claude-code"}}
	lf := scopedLockFile(
		"https://github.com/acme/matching",
		"https://github.com/acme/other",
		"https://github.com/acme/another",
	)

	matcher := scope.NewMatcher(&scope.Scope{
		Type:    scope.TypeRepo,
		RepoURL: "git@github.com:acme/matching.git",
	})
	applicable, skipped := filterAssetsByScope(lf, clientList, matcher)
	if len(applicable) != 2 {
		t.Fatalf("applicable = %d, want 2 (global + matching)", len(applicable))
	}
	if skipped != 2 {
		t.Fatalf("scopeSkipped = %d, want 2", skipped)
	}

	// Global scope: every repo-scoped asset is a scope skip.
	matcher = scope.NewMatcher(&scope.Scope{Type: scope.TypeGlobal})
	applicable, skipped = filterAssetsByScope(lf, clientList, matcher)
	if len(applicable) != 1 || skipped != 3 {
		t.Fatalf("global context: applicable=%d skipped=%d, want 1 and 3", len(applicable), skipped)
	}
}

func TestPrintDryRunPreview_ReportsScopeSkips(t *testing.T) {
	env := &installEnvironment{
		Clients: []clients.Client{&stubClient{id: "claude-code"}},
		CurrentScope: &scope.Scope{
			Type:    scope.TypeRepo,
			RepoURL: "git@github.com:acme/app.git",
		},
	}
	assets := []*lockfile.Asset{
		{Name: "global-skill", Version: "1.0.0", Type: asset.TypeSkill},
	}

	var buf bytes.Buffer
	printDryRunPreview(&buf, assets, env, map[string]string{}, false, 46)
	if !strings.Contains(buf.String(), "# 46 asset(s) skipped: scope does not match this context") {
		t.Fatalf("dry-run output missing skip line:\n%s", buf.String())
	}

	// Zero-resolved branch also reports skips.
	buf.Reset()
	printDryRunPreview(&buf, nil, env, map[string]string{}, false, 3)
	out := buf.String()
	if !strings.Contains(out, "# No assets resolved for this context.") ||
		!strings.Contains(out, "# 3 asset(s) skipped: scope does not match this context") {
		t.Fatalf("zero-resolved dry-run output missing skip line:\n%s", out)
	}

	// No skips → no noise.
	buf.Reset()
	printDryRunPreview(&buf, assets, env, map[string]string{}, false, 0)
	if strings.Contains(buf.String(), "skipped") {
		t.Fatalf("unexpected skip line with zero skips:\n%s", buf.String())
	}
}
