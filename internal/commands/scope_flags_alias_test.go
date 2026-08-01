package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/scope"
	"github.com/sleuth-io/sx/v2/internal/ui"
	vaultpkg "github.com/sleuth-io/sx/v2/internal/vault"
)

func TestWarnAliasScopeTargets(t *testing.T) {
	restore := scope.SetSSHHostLookup(func(alias string) (string, bool) {
		if alias == "workgit" {
			return "github.com", true
		}
		return "", false
	})
	t.Cleanup(restore)

	var out, errOut bytes.Buffer
	styledOut := ui.NewOutput(&out, &errOut)

	warnAliasScopeTargets([]vaultpkg.InstallTarget{
		{Kind: vaultpkg.InstallKindRepo, Repo: "git@workgit:acme/x.git"},
		{Kind: vaultpkg.InstallKindPath, Repo: "git@workgit:acme/x.git", Paths: []string{"services/api"}},
		{Kind: vaultpkg.InstallKindRepo, Repo: "https://github.com/acme/y"},
		{Kind: vaultpkg.InstallKindTeam, Team: "platform"},
	}, styledOut)

	combined := out.String() + errOut.String()
	note := `~/.ssh/config maps "workgit" to "github.com" on this machine; storing workgit/acme/x`
	if !strings.Contains(combined, note) {
		t.Fatalf("missing alias note, got:\n%s", combined)
	}
	if strings.Count(combined, note) != 1 {
		t.Fatalf("note should print once per repo, not per target, got:\n%s", combined)
	}
	if strings.Contains(combined, "github.com/acme/y") {
		t.Fatalf("non-alias target should not warn, got:\n%s", combined)
	}
}

func TestWarnAliasRepoURL_TransportRewrite(t *testing.T) {
	// The GitHub port-443 workaround (Host github.com / HostName
	// ssh.github.com) remaps the real host for transport only. The note
	// must state the mapping without telling the user their (correct)
	// input needs changing — pinning that the stored value shown is the
	// literal github.com form, never ssh.github.com.
	restore := scope.SetSSHHostLookup(func(alias string) (string, bool) {
		if alias == "github.com" {
			return "ssh.github.com", true
		}
		return "", false
	})
	t.Cleanup(restore)

	var out, errOut bytes.Buffer
	warnAliasRepoURL("git@github.com:acme/x.git", ui.NewOutput(&out, &errOut))

	combined := out.String() + errOut.String()
	if !strings.Contains(combined, `~/.ssh/config maps "github.com" to "ssh.github.com" on this machine; storing github.com/acme/x`) {
		t.Fatalf("missing transport-rewrite note, got:\n%s", combined)
	}
	if strings.Contains(combined, "storing ssh.github.com") {
		t.Fatalf("note must show the literal stored value, got:\n%s", combined)
	}
}
