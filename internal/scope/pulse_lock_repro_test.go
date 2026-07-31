package scope

import (
	"testing"

	"github.com/sleuth-io/sx/v2/internal/lockfile"
)

// Mirrors the TOML skills.new (Pulse) emits from
// _generate_lock_file_content + _generate_vaults_toml: scope rows carry
// Repository.url, which is always the provider's https form.
const pulseShapedLock = `lock-version = "1.0"
version = "PLACEHOLDER"
created-by = "sx-backend/1.0.0"

[[assets]]
name = "infra-skill"
version = "7"
type = "skill"

[assets.source-http]
url = "https://app.skills.new/api/skills/assets/infra-skill/7/infra-skill-7.zip"
hashes = {sha256 = "abc123"}
size = 1234

[[assets.scopes]]
repo = "https://github.com/kintsugi-tax/infra-ops"
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

	for _, remote := range []string{
		"git@workgit:kintsugi-tax/infra-ops.git", // alias, User in ssh config omitted or not
		"workgit:kintsugi-tax/infra-ops.git",     // userless alias form
		"ssh://git@workgit/kintsugi-tax/infra-ops.git",
		"git@github.com:kintsugi-tax/infra-ops.git", // plain SSH, no alias involved
	} {
		m := NewMatcher(&Scope{Type: TypeRepo, RepoURL: remote})
		if !m.MatchesAsset(&lf.Assets[0]) {
			t.Errorf("remote %q did not match pulse lock scope %q", remote, lf.Assets[0].Scopes[0].Repo)
		}
	}
}
