package scope

import (
	"testing"

	"github.com/sleuth-io/sx/v2/internal/lockfile"
)

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
		{"scp absolute path", "git@server.corp.com:/srv/git/x.git", "server.corp.com/srv/git/x"},
		{"ssh scheme", "ssh://git@github.com/acme/x.git", "github.com/acme/x"},
		{"ssh scheme with port", "ssh://git@github.com:22/acme/x.git", "github.com/acme/x"},
		{"ssh nondefault port", "ssh://git@gitea.corp.com:2222/acme/x.git", "gitea.corp.com/acme/x"},
		{"already normalized", "github.com/acme/x", "github.com/acme/x"},
		{"whitespace", "  https://github.com/acme/x.git  ", "github.com/acme/x"},
		{"not a url", "not a url", "not a url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeRepoURL(tt.in); got != tt.want {
				t.Errorf("NormalizeRepoURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMatchRepoURLs_CrossTransport(t *testing.T) {
	stored := "https://github.com/kintsugi-tax/infra-ops"
	remotes := []string{
		"git@github.com:kintsugi-tax/infra-ops.git",
		"ssh://git@github.com/kintsugi-tax/infra-ops.git",
		"ssh://git@github.com:22/kintsugi-tax/infra-ops.git",
		"https://github.com/kintsugi-tax/infra-ops.git",
		"HTTPS://GitHub.com/Kintsugi-Tax/Infra-Ops",
	}
	for _, remote := range remotes {
		if !MatchRepoURLs(remote, stored) {
			t.Errorf("MatchRepoURLs(%q, %q) = false, want true", remote, stored)
		}
	}
	if MatchRepoURLs("git@github.com:kintsugi-tax/other.git", stored) {
		t.Error("different repos matched")
	}
}

func TestMatchRepoURLs_SelfHostedSCP(t *testing.T) {
	if !MatchRepoURLs("git@ghe.corp.com:acme/x.git", "https://ghe.corp.com/acme/x") {
		t.Error("self-hosted scp remote should match https scope")
	}
}

// withSSHHostStub replaces the ssh_config lookup for the duration of a test.
func withSSHHostStub(t *testing.T, hosts map[string]string) {
	t.Helper()
	orig := lookupSSHHostname
	lookupSSHHostname = func(alias string) (string, bool) {
		h, ok := hosts[alias]
		return h, ok
	}
	t.Cleanup(func() { lookupSSHHostname = orig })
}

func TestNormalizeRepoURLCandidates_Alias(t *testing.T) {
	withSSHHostStub(t, map[string]string{"workgit": "github.com"})

	got := NormalizeRepoURLCandidates("git@workgit:acme/x.git")
	want := []string{"workgit/acme/x", "github.com/acme/x"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("candidates = %v, want %v", got, want)
	}

	// Non-SSH URLs never consult ssh_config.
	got = NormalizeRepoURLCandidates("https://workgit/acme/x")
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

	if !MatchRepoURLs("git@workgit:kintsugi-tax/infra-ops.git", "https://github.com/kintsugi-tax/infra-ops") {
		t.Error("ssh alias remote should match https scope for the resolved host")
	}
	if !MatchRepoURLs("ssh://git@workgit/kintsugi-tax/infra-ops.git", "https://github.com/kintsugi-tax/infra-ops") {
		t.Error("ssh:// alias remote should match https scope for the resolved host")
	}
}

func TestMatchesAsset_SSHRemote(t *testing.T) {
	withSSHHostStub(t, map[string]string{"workgit": "github.com"})

	asset := &lockfile.Asset{
		Name:   "infra-skill",
		Scopes: []lockfile.Scope{{Repo: "https://github.com/kintsugi-tax/infra-ops"}},
	}
	for _, remote := range []string{
		"git@github.com:kintsugi-tax/infra-ops.git",
		"git@workgit:kintsugi-tax/infra-ops.git",
	} {
		m := NewMatcher(&Scope{Type: TypeRepo, RepoURL: remote})
		if !m.MatchesAsset(asset) {
			t.Errorf("remote %q did not match asset scope", remote)
		}
	}
}
