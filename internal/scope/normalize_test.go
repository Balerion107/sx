package scope

import (
	"testing"

	"github.com/sleuth-io/sx/v2/internal/lockfile"
)

// withSSHHostStub replaces the ssh_config lookup for the duration of a
// test so the suite never reads the developer's real ~/.ssh/config.
// Pass nil for "no aliases configured".
func withSSHHostStub(t *testing.T, hosts map[string]string) {
	t.Helper()
	restore := SetSSHHostLookup(func(alias string) (string, bool) {
		h, ok := hosts[alias]
		return h, ok
	})
	t.Cleanup(restore)
}

func TestNormalizeRepoURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"https", "https://github.com/acme/x", "github.com/acme/x"},
		{"https with .git", "https://github.com/acme/x.git", "github.com/acme/x"},
		{"https trailing slash", "https://github.com/acme/x/", "github.com/acme/x"},
		{"https with port", "https://gitea.corp.com:3000/acme/x.git", "gitea.corp.com/acme/x"},
		{"scp github", "git@github.com:acme/x.git", "github.com/acme/x"},
		{"scp uppercase", "Git@GitHub.com:Acme/X.git", "github.com/acme/x"},
		{"scp self-hosted", "git@ghe.corp.com:acme/x.git", "ghe.corp.com/acme/x"},
		{"scp ssh alias", "git@workgit:acme/x.git", "workgit/acme/x"},
		{"scp userless alias", "workgit:acme/x.git", "workgit/acme/x"},
		{"scp userless host", "github.com:acme/x.git", "github.com/acme/x"},
		{"scp absolute path", "git@server.corp.com:/srv/git/x.git", "server.corp.com/srv/git/x"},
		{"ssh scheme", "ssh://git@github.com/acme/x.git", "github.com/acme/x"},
		{"ssh scheme with port", "ssh://git@github.com:22/acme/x.git", "github.com/acme/x"},
		{"ssh nondefault port", "ssh://git@gitea.corp.com:2222/acme/x.git", "gitea.corp.com/acme/x"},
		{"already normalized", "github.com/acme/x", "github.com/acme/x"},
		{"legacy ported form kept literal", "gitea.corp.com:3000/acme/x", "gitea.corp.com/3000/acme/x"},
		{"host and port only", "gitea.corp.com:3000", "gitea.corp.com:3000"},
		{"numeric owner with user", "git@github.com:123/repo.git", "github.com/123/repo"},
		{"numeric owner userless", "github.com:123/repo", "github.com/123/repo"},
		{"numeric path segment kept", "gitea.corp.com:2024/team/app", "gitea.corp.com/2024/team/app"},
		{"whitespace", "  https://github.com/acme/x.git  ", "github.com/acme/x"},
		{"absolute local path", "/srv/git/x", "srv/git/x"},
		{"file url", "file:///srv/git/x.git", "srv/git/x"},
		{"scp ipv6", "[2001:db8::1]:acme/x.git", "[2001:db8::1]/acme/x"},
		{"scp ipv6 with user", "git@[2001:db8::1]:acme/x.git", "[2001:db8::1]/acme/x"},
		{"windows drive path", "c:/repos/x", "c:/repos/x"},
		{"not a url", "not a url", "not a url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeRepoURL(tt.in)
			if got != tt.want {
				t.Errorf("NormalizeRepoURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
			// Normalization must be a fixed point: its output is what
			// the vault write paths persist, so a form that re-splits
			// on the next read (as unbracketed IPv6 once did) breaks
			// the write→read round trip.
			if again := NormalizeRepoURL(got); again != got {
				t.Errorf("not idempotent: NormalizeRepoURL(%q) = %q", got, again)
			}
		})
	}
}

func TestMatchRepoURLs_CrossTransport(t *testing.T) {
	withSSHHostStub(t, nil)

	stored := "https://github.com/acme/infra-ops"
	remotes := []string{
		"git@github.com:acme/infra-ops.git",
		"github.com:acme/infra-ops.git",
		"ssh://git@github.com/acme/infra-ops.git",
		"ssh://git@github.com:22/acme/infra-ops.git",
		"https://github.com/acme/infra-ops.git",
		"HTTPS://GitHub.com/Acme/Infra-Ops",
	}
	for _, remote := range remotes {
		if !MatchRepoURLs(remote, stored) {
			t.Errorf("MatchRepoURLs(%q, %q) = false, want true", remote, stored)
		}
	}
	if MatchRepoURLs("git@github.com:acme/other.git", stored) {
		t.Error("different repos matched")
	}
}

func TestMatchRepoURLs_SelfHostedSCP(t *testing.T) {
	withSSHHostStub(t, nil)

	if !MatchRepoURLs("git@ghe.corp.com:acme/x.git", "https://ghe.corp.com/acme/x") {
		t.Error("self-hosted scp remote should match https scope")
	}
}

func TestMatchRepoURLs_LocalAndFileForms(t *testing.T) {
	withSSHHostStub(t, nil)

	// A local clone's remote can be a bare path or a file:// URL; both
	// must match each other and rows stored by older sx versions
	// ("srv/git/x").
	for _, pair := range [][2]string{
		{"/srv/git/x", "file:///srv/git/x"},
		{"/srv/git/x", "srv/git/x"},
		{"file:///srv/git/x.git", "srv/git/x"},
	} {
		if !MatchRepoURLs(pair[0], pair[1]) {
			t.Errorf("MatchRepoURLs(%q, %q) = false, want true", pair[0], pair[1])
		}
	}
}

func TestMatchRepoURLs_IPv6(t *testing.T) {
	withSSHHostStub(t, nil)

	if !MatchRepoURLs("[2001:db8::1]:acme/x.git", "ssh://git@[2001:db8::1]/acme/x") {
		t.Error("bracketed IPv6 scp remote should match its ssh:// form")
	}
}

func TestNormalizeRepoURLCandidates_Alias(t *testing.T) {
	withSSHHostStub(t, map[string]string{"workgit": "github.com"})

	for _, remote := range []string{"git@workgit:acme/x.git", "workgit:acme/x.git"} {
		got := NormalizeRepoURLCandidates(remote)
		want := []string{"workgit/acme/x", "github.com/acme/x"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("candidates(%q) = %v, want %v", remote, got, want)
		}
	}

	// Non-SSH URLs never consult ssh_config.
	got := NormalizeRepoURLCandidates("https://workgit/acme/x")
	if len(got) != 1 || got[0] != "workgit/acme/x" {
		t.Fatalf("https candidates = %v, want single literal form", got)
	}
}

func TestNormalizeRepoURLCandidates_AliasIsRealHost(t *testing.T) {
	// A "Host github.com" proxy rewrite must not change the identity of
	// the literal form — the resolved host is only an extra candidate.
	withSSHHostStub(t, map[string]string{"github.com": "gh-proxy.corp.com"})

	got := NormalizeRepoURLCandidates("git@github.com:acme/x.git")
	if got[0] != "github.com/acme/x" {
		t.Fatalf("literal candidate = %q, want github.com/acme/x", got[0])
	}
	if !MatchRepoURLs("git@github.com:acme/x.git", "https://github.com/acme/x") {
		t.Error("proxy rewrite broke the literal-host match")
	}
}

func TestMatchRepoURLs_AliasResolvesToStoredScope(t *testing.T) {
	withSSHHostStub(t, map[string]string{"workgit": "github.com"})

	stored := "https://github.com/acme/infra-ops"
	for _, remote := range []string{
		"git@workgit:acme/infra-ops.git",
		"workgit:acme/infra-ops.git",
		"ssh://git@workgit/acme/infra-ops.git",
	} {
		if !MatchRepoURLs(remote, stored) {
			t.Errorf("alias remote %q should match %q", remote, stored)
		}
	}
}

func TestMatchStoredRepoURL_LegacyPortedRows(t *testing.T) {
	withSSHHostStub(t, nil)

	// The pre-alias normalizer persisted scope rows with the port kept
	// ("gitea.corp.com:3000/acme/x"); those rows must keep matching the
	// live remotes they were written for. The portless reading applies
	// to the STORED side only — never to the remote.
	legacy := "gitea.corp.com:3000/acme/x"
	for _, remote := range []string{
		"https://gitea.corp.com:3000/acme/x.git",
		"ssh://git@gitea.corp.com:2222/acme/x.git",
		"https://gitea.corp.com/acme/x",
	} {
		if !MatchStoredRepoURL(legacy, remote) {
			t.Errorf("legacy stored row %q should match remote %q", legacy, remote)
		}
	}

	// Candidates never include the portless reading — a live remote
	// with a numeric path segment keeps only its literal form, so an
	// asset scoped to the unrelated top-level repo can't match it.
	for _, in := range []string{
		"gitea.corp.com:2024/team/app",
		"github.com:123/repo",
		"git@gitea.corp.com:3000/acme/x",
	} {
		if got := NormalizeRepoURLCandidates(in); len(got) != 1 {
			t.Errorf("candidates(%q) = %v, want only the literal form", in, got)
		}
	}
	if MatchStoredRepoURL("gitea.corp.com/team/app", "gitea.corp.com:2024/team/app") {
		t.Error("a remote's numeric path segment must not be reinterpreted as a port")
	}

	// One-segment legacy rows are outside the reconciliation rule (the
	// documented ambiguity trade against numeric owners).
	if MatchStoredRepoURL("ghe.corp:2222/tools", "https://ghe.corp/tools") {
		t.Error("one-segment legacy row should keep only its literal reading")
	}

	// The deliberate width of the stored-side reading: a stored row
	// whose numeric segment is genuinely a path (not a legacy port)
	// also matches the same path without it. Documented trade in
	// docs/repos.md — losing this would lose every real legacy row.
	if !MatchStoredRepoURL("gitea.corp.com:2024/team/app", "https://gitea.corp.com/team/app") {
		t.Error("stored-side legacy reading should apply to port-like numeric segments")
	}
}

func TestCanonicalizeStoredRepoRow(t *testing.T) {
	withSSHHostStub(t, nil)

	tests := []struct{ in, want string }{
		{"gitea.corp.com:3000/acme/x", "gitea.corp.com/acme/x"},
		{"ghe.corp:2222/acme/x", "ghe.corp/acme/x"},
		{"github.com/acme/x", "github.com/acme/x"},
		{"https://github.com/acme/x.git", "github.com/acme/x"},
		// Not legacy-shaped: numeric owner (2 segments) and one-segment
		// rows keep their literal normalization.
		{"github.com:123/repo", "github.com/123/repo"},
		{"ghe.corp:2222/tools", "ghe.corp/2222/tools"},
	}
	for _, tt := range tests {
		if got := CanonicalizeStoredRepoRow(tt.in); got != tt.want {
			t.Errorf("CanonicalizeStoredRepoRow(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStoredRepoRowMatches(t *testing.T) {
	withSSHHostStub(t, map[string]string{"workgit": "github.com"})

	if !StoredRepoRowMatches("gitea.corp.com:3000/acme/x", "https://gitea.corp.com:3000/acme/x") {
		t.Error("legacy ported row should match its live https form")
	}
	if !StoredRepoRowMatches("github.com/acme/x", "git@github.com:acme/x.git") {
		t.Error("normalized-equal forms should match")
	}
	// Write paths must not consult ssh config: the workgit alias would
	// resolve to github.com for candidate matching, but not here.
	if StoredRepoRowMatches("github.com/acme/x", "git@workgit:acme/x.git") {
		t.Error("write-path comparison must ignore ~/.ssh/config aliases")
	}
}

func TestLooksLikeSameRepo(t *testing.T) {
	withSSHHostStub(t, nil)

	if !LooksLikeSameRepo("git@unresolvable-alias:acme/x.git", "https://github.com/acme/x") {
		t.Error("same owner/repo on different hosts should be a near miss")
	}
	if LooksLikeSameRepo("git@github.com:acme/x.git", "https://github.com/acme/x") {
		t.Error("a full match must not be a near miss")
	}
	if LooksLikeSameRepo("git@github.com:acme/x.git", "https://github.com/acme/y") {
		t.Error("different repos are not a near miss")
	}
}

func TestMatchesAsset_SSHRemote(t *testing.T) {
	withSSHHostStub(t, map[string]string{"workgit": "github.com"})

	asset := &lockfile.Asset{
		Name:   "infra-skill",
		Scopes: []lockfile.Scope{{Repo: "https://github.com/acme/infra-ops"}},
	}
	for _, remote := range []string{
		"git@github.com:acme/infra-ops.git",
		"git@workgit:acme/infra-ops.git",
		"workgit:acme/infra-ops.git",
	} {
		m := NewMatcher(&Scope{Type: TypeRepo, RepoURL: remote})
		if !m.MatchesAsset(asset) {
			t.Errorf("remote %q did not match asset scope", remote)
		}
	}
}

func TestNearMissScope(t *testing.T) {
	withSSHHostStub(t, nil)

	asset := &lockfile.Asset{
		Name:   "infra-skill",
		Scopes: []lockfile.Scope{{Repo: "https://github.com/acme/x"}},
	}

	m := NewMatcher(&Scope{Type: TypeRepo, RepoURL: "git@badalias:acme/x.git"})
	repo, ok := m.NearMissScope(asset)
	if !ok || repo != "https://github.com/acme/x" {
		t.Errorf("NearMissScope = (%q, %v), want the offending scope repo", repo, ok)
	}

	m = NewMatcher(&Scope{Type: TypeRepo, RepoURL: "git@github.com:acme/other.git"})
	if _, ok := m.NearMissScope(asset); ok {
		t.Error("asset for another repo is not a near miss")
	}

	m = NewMatcher(&Scope{Type: TypeGlobal})
	if _, ok := m.NearMissScope(asset); ok {
		t.Error("global context has no near misses")
	}
}
