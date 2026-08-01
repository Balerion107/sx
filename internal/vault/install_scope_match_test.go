package vault

import (
	"context"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/manifest"
	"github.com/sleuth-io/sx/v2/internal/mgmt"
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

// TestCreateTeam_CanonicalizesAndDedupesRepos pins the create-path
// invariant: repo rows are stored canonical (same form `team repo add`
// writes) and two spellings of one repo collapse to a single row.
func TestCreateTeam_CanonicalizesAndDedupesRepos(t *testing.T) {
	mgmt.ResetActorCache()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "alice@example.com")
	runGit(t, dir, "config", "user.name", "Alice Admin")
	if err := manifest.Save(dir, &manifest.Manifest{SchemaVersion: manifest.CurrentSchemaVersion}); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
	v, err := NewPathVault("file://" + dir)
	if err != nil {
		t.Fatalf("NewPathVault: %v", err)
	}

	ctx := context.Background()
	if err := v.CreateTeam(ctx, mgmt.Team{
		Name:   "platform",
		Admins: []string{"alice@example.com"},
		Repositories: []string{
			"git@github.com:acme/x.git",
			"https://github.com/acme/x.git",
			"https://github.com/acme/y",
		},
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	team, err := v.GetTeam(ctx, "platform")
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	want := []string{"github.com/acme/x", "github.com/acme/y"}
	if len(team.Repositories) != len(want) || team.Repositories[0] != want[0] || team.Repositories[1] != want[1] {
		t.Fatalf("repositories = %v, want %v (canonical, deduped)", team.Repositories, want)
	}
}
