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

// stubSSHHostLookup keeps these tests hermetic — without it the
// matcher would parse the developer's real ~/.ssh/config through the
// process-wide cache in internal/git.
func stubSSHHostLookup(t *testing.T) {
	t.Helper()
	restore := scope.SetSSHHostLookup(func(string) (string, bool) { return "", false })
	t.Cleanup(restore)
}

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
	stubSSHHostLookup(t)
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
	skips := &scopeSkips{}
	applicable := filterAssetsByScope(lf, clientList, matcher, skips)
	if len(applicable) != 2 {
		t.Fatalf("applicable = %d, want 2 (global + matching)", len(applicable))
	}
	if skips.Skipped() != 2 {
		t.Fatalf("Skipped() = %d, want 2", skips.Skipped())
	}
	if skips.NearMiss() != 0 {
		t.Fatalf("NearMiss() = %d, want 0 for ordinary other-repo skips", skips.NearMiss())
	}

	// Global scope: every repo-scoped asset is an (expected) skip.
	matcher = scope.NewMatcher(&scope.Scope{Type: scope.TypeGlobal})
	skips = &scopeSkips{}
	applicable = filterAssetsByScope(lf, clientList, matcher, skips)
	if len(applicable) != 1 || skips.Skipped() != 3 {
		t.Fatalf("global context: applicable=%d skipped=%d, want 1 and 3", len(applicable), skips.Skipped())
	}
}

func TestFilterAssetsByScope_NearMissAndProfileDedupe(t *testing.T) {
	stubSSHHostLookup(t)
	clientList := []clients.Client{&stubClient{id: "claude-code"}}
	// Same owner/repo as the current remote but a host form that can't
	// be reconciled — the near-miss signature.
	lf := scopedLockFile("https://github.com/acme/app")

	matcher := scope.NewMatcher(&scope.Scope{
		Type:    scope.TypeRepo,
		RepoURL: "git@sx-test-unresolvable.invalid:acme/app.git",
	})
	skips := &scopeSkips{}
	filterAssetsByScope(lf, clientList, matcher, skips)
	// Same lock filtered again, as a second active profile would.
	filterAssetsByScope(lf, clientList, matcher, skips)

	if skips.Skipped() != 1 {
		t.Fatalf("Skipped() = %d, want 1 (deduped across profiles)", skips.Skipped())
	}
	if skips.NearMiss() != 1 {
		t.Fatalf("NearMiss() = %d, want 1", skips.NearMiss())
	}
	details := skips.NearMissDetails()
	if len(details) != 1 || details[0] != "scoped-a: scoped to https://github.com/acme/app (github.com/acme/app)" {
		t.Fatalf("NearMissDetails() = %v", details)
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
	skipsOf := func(skippedNames []string, nearMiss map[string]string) *scopeSkips {
		s := &scopeSkips{skippedNames: map[string]struct{}{}, nearMissRepos: nearMiss}
		for _, n := range skippedNames {
			s.skippedNames[n] = struct{}{}
		}
		return s
	}

	var buf bytes.Buffer
	printDryRunPreview(&buf, assets, env, map[string]string{}, false,
		skipsOf([]string{"a", "b", "c"}, map[string]string{"a": "https://ghe.corp/acme/app"}))
	out := buf.String()
	if !strings.Contains(out, "# Current scope: git@github.com:acme/app.git (github.com/acme/app)") ||
		!strings.Contains(out, "# 3 asset(s) skipped: scope does not match this context") ||
		!strings.Contains(out, "# 1 of them name the same owner/repo path as this repo but a different host:") ||
		!strings.Contains(out, "#   a: scoped to https://ghe.corp/acme/app (ghe.corp/acme/app)") {
		t.Fatalf("dry-run output missing skip lines:\n%s", out)
	}

	// Zero-resolved branch also reports skips.
	buf.Reset()
	printDryRunPreview(&buf, nil, env, map[string]string{}, false, skipsOf([]string{"a"}, nil))
	out = buf.String()
	if !strings.Contains(out, "# No assets resolved for this context.") ||
		!strings.Contains(out, "# Current scope: git@github.com:acme/app.git (github.com/acme/app)") ||
		!strings.Contains(out, "# 1 asset(s) skipped: scope does not match this context") {
		t.Fatalf("zero-resolved dry-run output missing skip line:\n%s", out)
	}

	// No skips → no noise.
	buf.Reset()
	printDryRunPreview(&buf, assets, env, map[string]string{}, false, &scopeSkips{})
	if strings.Contains(buf.String(), "skipped") {
		t.Fatalf("unexpected skip line with zero skips:\n%s", buf.String())
	}
}
