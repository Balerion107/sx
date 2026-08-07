package assets

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/asset"
	"github.com/sleuth-io/sx/v2/internal/lockfile"
	"github.com/sleuth-io/sx/v2/internal/utils"
	"github.com/sleuth-io/sx/v2/internal/vault"
)

func fetcherTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestFetchAssetBranchRefBypassesDiskCache pins the end-to-end behavior
// of mutable source-git refs: the name@version disk cache must not pin
// a branch-ref asset to its first-ever fetch, or the handler-level
// remote-tracking fix never reaches the user.
func TestFetchAssetBranchRefBypassesDiskCache(t *testing.T) {
	t.Setenv("SX_CACHE_DIR", t.TempDir())

	dir := t.TempDir()
	fetcherTestGit(t, dir, "init")
	fetcherTestGit(t, dir, "config", "user.email", "alice@example.com")
	fetcherTestGit(t, dir, "config", "user.name", "Alice")
	if err := os.MkdirAll(filepath.Join(dir, "asset0"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeAsset := func(content string) {
		t.Helper()
		metadata := "[asset]\nname = \"asset0\"\nversion = \"1.0.0\"\ntype = \"skill\"\ndescription = \"test asset\"\n\n[skill]\nprompt-file = \"SKILL.md\"\n"
		if err := os.WriteFile(filepath.Join(dir, "asset0", "metadata.toml"), []byte(metadata), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "asset0", "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeAsset("# rev1\n")
	fetcherTestGit(t, dir, "add", ".")
	fetcherTestGit(t, dir, "commit", "-m", "rev1")
	branch := fetcherTestGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
	repoURL := "file://" + dir

	v, err := vault.NewGitVault(repoURL)
	if err != nil {
		t.Fatalf("failed to create vault: %v", err)
	}
	fetcher := NewAssetFetcher(v, repoURL)
	a := &lockfile.Asset{
		Name:    "asset0",
		Version: "1.0.0",
		Type:    asset.TypeSkill,
		SourceGit: &lockfile.SourceGit{
			URL:          repoURL,
			Ref:          branch,
			Subdirectory: "asset0",
		},
	}

	if _, _, err := fetcher.FetchAsset(context.Background(), a); err != nil {
		t.Fatalf("initial fetch failed: %v", err)
	}

	// Advance the branch upstream; the same name@version must serve the
	// new tip rather than a disk-cached first fetch.
	writeAsset("# rev2\n")
	fetcherTestGit(t, dir, "add", ".")
	fetcherTestGit(t, dir, "commit", "-m", "rev2")

	zipData, _, err := fetcher.FetchAsset(context.Background(), a)
	if err != nil {
		t.Fatalf("fetch after upstream commit failed: %v", err)
	}
	content, err := utils.ReadZipFile(zipData, "SKILL.md")
	if err != nil {
		t.Fatalf("zip missing SKILL.md: %v", err)
	}
	if string(content) != "# rev2\n" {
		t.Fatalf("branch-ref asset served stale content %q, want the remote tip", content)
	}
}
