package vault

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/asset"
	"github.com/sleuth-io/sx/v2/internal/cache"
	"github.com/sleuth-io/sx/v2/internal/git"
	"github.com/sleuth-io/sx/v2/internal/lockfile"
	"github.com/sleuth-io/sx/v2/internal/utils"
)

// setupSourceGitRepo creates a git repo with n exploded asset
// subdirectories and returns its file:// URL and HEAD SHA.
func setupSourceGitRepo(t *testing.T, n int) (repoURL, sha string) {
	t.Helper()

	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "alice@example.com")
	runGit(t, dir, "config", "user.name", "Alice")

	for i := range n {
		assetDir := filepath.Join(dir, fmt.Sprintf("asset%d", i))
		if err := os.MkdirAll(assetDir, 0755); err != nil {
			t.Fatalf("failed to create asset dir: %v", err)
		}
		metadata := fmt.Sprintf("name = %q\nversion = \"1.0.0\"\ntype = \"skill\"\n", fmt.Sprintf("asset%d", i))
		if err := os.WriteFile(filepath.Join(assetDir, "metadata.toml"), []byte(metadata), 0644); err != nil {
			t.Fatalf("failed to write metadata: %v", err)
		}
		if err := os.WriteFile(filepath.Join(assetDir, "SKILL.md"), []byte("# skill\n"), 0644); err != nil {
			t.Fatalf("failed to write skill file: %v", err)
		}
	}

	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "add assets")
	sha = strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))

	return "file://" + dir, sha
}

func sourceGitAsset(name, repoURL, sha, subdirectory string) *lockfile.Asset {
	return &lockfile.Asset{
		Name:    name,
		Version: "1.0.0",
		Type:    asset.TypeSkill,
		SourceGit: &lockfile.SourceGit{
			URL:          repoURL,
			Ref:          sha,
			Subdirectory: subdirectory,
		},
	}
}

// TestGitSourceHandler_ConcurrentFetchSameRepo reproduces issue #220:
// concurrent fetches of assets sharing one source-git URL all target the
// same URL-keyed cache directory and must serialize instead of racing on
// the clone and checkout.
func TestGitSourceHandler_ConcurrentFetchSameRepo(t *testing.T) {
	t.Setenv("SX_CACHE_DIR", t.TempDir())

	const numAssets = 10
	repoURL, sha := setupSourceGitRepo(t, numAssets)

	handler := NewGitSourceHandler(git.NewClient())

	var wg sync.WaitGroup
	errs := make([]error, numAssets)
	data := make([][]byte, numAssets)
	for i := range numAssets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("asset%d", i)
			a := sourceGitAsset(name, repoURL, sha, name)
			data[i], errs[i] = handler.Fetch(context.Background(), a)
		}(i)
	}
	wg.Wait()

	for i := range numAssets {
		if errs[i] != nil {
			t.Errorf("asset%d: fetch failed: %v", i, errs[i])
			continue
		}
		if !utils.IsZipFile(data[i]) {
			t.Errorf("asset%d: fetched data is not a valid zip", i)
			continue
		}
		// Content identity, not just zip validity: a read overlapping a
		// concurrent checkout could produce a valid zip of the wrong asset.
		metadata, err := utils.ReadZipFile(data[i], "metadata.toml")
		if err != nil {
			t.Errorf("asset%d: zip missing metadata.toml: %v", i, err)
			continue
		}
		if want := fmt.Sprintf("name = %q", fmt.Sprintf("asset%d", i)); !strings.Contains(string(metadata), want) {
			t.Errorf("asset%d: zip contains the wrong asset: %s", i, metadata)
		}
	}
}

// TestGitSourceHandler_ConcurrentFetchDifferentRefs covers the race the
// zip-validity check alone cannot: goroutines pinned to different SHAs
// share one working tree, so a read overlapping another ref's checkout
// would return a valid zip with the wrong content.
func TestGitSourceHandler_ConcurrentFetchDifferentRefs(t *testing.T) {
	t.Setenv("SX_CACHE_DIR", t.TempDir())

	repoURL, sha1 := setupSourceGitRepo(t, 1)
	dir := strings.TrimPrefix(repoURL, "file://")
	if err := os.WriteFile(filepath.Join(dir, "asset0", "SKILL.md"), []byte("# rev2\n"), 0644); err != nil {
		t.Fatalf("failed to update skill file: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "rev2")
	sha2 := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))

	want := map[string]string{sha1: "# skill\n", sha2: "# rev2\n"}
	handler := NewGitSourceHandler(git.NewClient())

	const workers = 8
	shas := make([]string, workers)
	data := make([][]byte, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		shas[i] = sha1
		if i%2 == 1 {
			shas[i] = sha2
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := sourceGitAsset("asset0", repoURL, shas[i], "asset0")
			data[i], errs[i] = handler.Fetch(context.Background(), a)
		}(i)
	}
	wg.Wait()

	for i := range workers {
		if errs[i] != nil {
			t.Errorf("worker %d: fetch failed: %v", i, errs[i])
			continue
		}
		content, err := utils.ReadZipFile(data[i], "SKILL.md")
		if err != nil {
			t.Errorf("worker %d: zip missing SKILL.md: %v", i, err)
			continue
		}
		if string(content) != want[shas[i]] {
			t.Errorf("worker %d: got %q for ref %s, want %q", i, content, shas[i], want[shas[i]])
		}
	}
}

// TestGitSourceHandler_FetchRecoversFromStaleCacheDir verifies that a
// cache directory left without a .git (e.g. by an interrupted clone) is
// cleared and re-cloned instead of failing "destination already exists"
// forever.
func TestGitSourceHandler_FetchRecoversFromStaleCacheDir(t *testing.T) {
	t.Setenv("SX_CACHE_DIR", t.TempDir())

	repoURL, sha := setupSourceGitRepo(t, 1)

	// Simulate an interrupted clone: a non-empty cache dir with no .git
	repoCache, err := cache.GetGitRepoCachePath(repoURL)
	if err != nil {
		t.Fatalf("failed to get cache path: %v", err)
	}
	if err := os.MkdirAll(repoCache, 0755); err != nil {
		t.Fatalf("failed to create stale cache dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoCache, "partial"), []byte("junk"), 0644); err != nil {
		t.Fatalf("failed to write stale file: %v", err)
	}

	handler := NewGitSourceHandler(git.NewClient())
	data, err := handler.Fetch(context.Background(), sourceGitAsset("asset0", repoURL, sha, "asset0"))
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if !utils.IsZipFile(data) {
		t.Fatal("fetched data is not a valid zip")
	}
}

// TestGitSourceHandler_FetchRecoversFromCorruptGitDir covers the other
// interrupted-clone shape: .git exists but the repository is unusable,
// so fetch fails and the cache must be discarded and re-cloned rather
// than failing on every retry.
func TestGitSourceHandler_FetchRecoversFromCorruptGitDir(t *testing.T) {
	t.Setenv("SX_CACHE_DIR", t.TempDir())

	repoURL, sha := setupSourceGitRepo(t, 1)

	repoCache, err := cache.GetGitRepoCachePath(repoURL)
	if err != nil {
		t.Fatalf("failed to get cache path: %v", err)
	}
	if err := os.MkdirAll(repoCache, 0755); err != nil {
		t.Fatalf("failed to create cache dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoCache, ".git"), []byte("junk"), 0644); err != nil {
		t.Fatalf("failed to write corrupt .git: %v", err)
	}

	handler := NewGitSourceHandler(git.NewClient())
	data, err := handler.Fetch(context.Background(), sourceGitAsset("asset0", repoURL, sha, "asset0"))
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if !utils.IsZipFile(data) {
		t.Fatal("fetched data is not a valid zip")
	}
}
