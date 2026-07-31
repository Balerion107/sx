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
		{Kind: vaultpkg.InstallKindRepo, Repo: "https://github.com/acme/y"},
		{Kind: vaultpkg.InstallKindTeam, Team: "platform"},
	}, styledOut)

	combined := out.String() + errOut.String()
	if !strings.Contains(combined, "git@workgit:acme/x.git uses an SSH alias resolving to github.com/acme/x") {
		t.Fatalf("missing alias warning, got:\n%s", combined)
	}
	if strings.Contains(combined, "github.com/acme/y") {
		t.Fatalf("non-alias target should not warn, got:\n%s", combined)
	}
}
