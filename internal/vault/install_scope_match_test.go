package vault

import (
	"testing"

	"github.com/sleuth-io/sx/v2/internal/manifest"
)

// TestInstallScopeMatches_LegacyReadingWidth pins the deliberate width
// of the stored-side legacy reading on the scope-row write paths: a
// stored legacy row is reachable by the URL forms users actually see
// for the repo, which also means a "host/team/app" needle matches a
// stored "host:2024/team/app" row (its dual reading already grants
// that repo). See the comment on installScopeMatches.
func TestInstallScopeMatches_LegacyReadingWidth(t *testing.T) {
	repoRow := func(repo string) manifest.Scope {
		return manifest.Scope{Kind: manifest.ScopeKindRepo, Repo: repo}
	}

	// Reconciliation: a legacy ported row matches its live https form.
	if !installScopeMatches(repoRow("gitea.corp.com:3000/acme/x"), repoRow("https://gitea.corp.com:3000/acme/x")) {
		t.Error("legacy row should match its live https form")
	}
	// The width: the portless needle matches the legacy-shaped row too.
	if !installScopeMatches(repoRow("gitea.corp.com:2024/team/app"), repoRow("gitea.corp.com/team/app")) {
		t.Error("portless needle should match a legacy-shaped stored row (documented width)")
	}
	// But never the other way: the reading applies to the stored side
	// only, so a modern stored row is not matched by a colon needle's
	// portless interpretation.
	if installScopeMatches(repoRow("gitea.corp.com/team/app"), repoRow("gitea.corp.com:2024/team/app")) {
		t.Error("needle-side legacy reading must not apply")
	}
	// Different repos stay different.
	if installScopeMatches(repoRow("github.com/acme/x"), repoRow("github.com/acme/y")) {
		t.Error("distinct repos must not match")
	}
}
