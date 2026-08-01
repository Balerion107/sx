package scope

import (
	"testing"

	"github.com/sleuth-io/sx/v2/internal/lockfile"
)

// Mirrors the TOML skills.new generates for a repo-scoped asset — see
// pulse sleuth/apps/skills/service/assets/locking.py
// (_generate_lock_file_content and _generate_vaults_toml). Scope rows
// carry Repository.url, which is always the provider's https form.
const pulseShapedLock = `lock-version = "1.0"
version = "PLACEHOLDER"
created-by = "sx-backend/1.0.0"

[[assets]]
name = "infra-skill"
version = "7"
type = "skill"

[assets.source-http]
url = "https://skills.example.com/api/skills/assets/infra-skill/7/infra-skill-7.zip"
hashes = {sha256 = "abc123"}
size = 1234

[[assets.scopes]]
repo = "https://github.com/acme/infra-ops"
`

func TestPulseLock_AliasRemoteMatches(t *testing.T) {
	withSSHHostStub(t, map[string]string{"workgit": "github.com"})

	lf, err := lockfile.Parse([]byte(pulseShapedLock))
	if err != nil {
		t.Fatalf("parse pulse-shaped lock: %v", err)
	}
	if len(lf.Assets) != 1 || len(lf.Assets[0].Scopes) != 1 {
		t.Fatalf("unexpected lock shape: %+v", lf.Assets)
	}

	// Pin the parts of the generator's shape this client relies on —
	// lockfile.Parse ignores unknown keys, so without these assertions
	// a drifted source-http block would go unnoticed.
	src := lf.Assets[0].SourceHTTP
	if src == nil {
		t.Fatal("source-http block did not parse")
	}
	if src.URL != "https://skills.example.com/api/skills/assets/infra-skill/7/infra-skill-7.zip" ||
		src.Hashes["sha256"] != "abc123" || src.Size != 1234 {
		t.Fatalf("source-http parsed unexpectedly: %+v", src)
	}

	for _, remote := range []string{
		"git@workgit:acme/infra-ops.git", // alias with explicit user
		"workgit:acme/infra-ops.git",     // userless alias form (User in ssh config)
		"ssh://git@workgit/acme/infra-ops.git",
		"git@github.com:acme/infra-ops.git", // plain SSH, no alias involved
	} {
		m := NewMatcher(&Scope{Type: TypeRepo, RepoURL: remote})
		if !m.MatchesAsset(&lf.Assets[0]) {
			t.Errorf("remote %q did not match pulse lock scope %q", remote, lf.Assets[0].Scopes[0].Repo)
		}
	}
}
