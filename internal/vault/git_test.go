package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	repoCache, err := cache.GetGitSourceCachePath(repoURL)
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

// TestGitSourceHandler_BadRefDoesNotDestroyCache guards the checkout
// recovery's blast radius: a ref the repository simply doesn't have must
// fail fast without discarding the healthy clone that every other asset
// from the same URL shares.
func TestGitSourceHandler_BadRefDoesNotDestroyCache(t *testing.T) {
	t.Setenv("SX_CACHE_DIR", t.TempDir())

	repoURL, sha := setupSourceGitRepo(t, 1)
	handler := NewGitSourceHandler(git.NewClient())

	// Warm the cache with a good fetch.
	if _, err := handler.Fetch(context.Background(), sourceGitAsset("asset0", repoURL, sha, "asset0")); err != nil {
		t.Fatalf("warm fetch failed: %v", err)
	}
	repoCache, err := cache.GetGitSourceCachePath(repoURL)
	if err != nil {
		t.Fatalf("failed to get cache path: %v", err)
	}
	// An untracked marker: it survives checkouts, but not the destroy-
	// and-re-clone path this test exists to rule out.
	marker := filepath.Join(repoCache, "untracked-marker")
	if err := os.WriteFile(marker, []byte("x"), 0644); err != nil {
		t.Fatalf("failed to write marker: %v", err)
	}

	badRef := strings.Repeat("d", 40)
	if _, err := handler.Fetch(context.Background(), sourceGitAsset("asset0", repoURL, badRef, "asset0")); err == nil {
		t.Fatal("fetch of a nonexistent SHA should fail")
	}
	if !utils.FileExists(marker) {
		t.Fatal("a bad ref must not destroy the shared cache")
	}
}

// TestGitSourceHandler_CorruptCacheInsideAncestorRepo pins the
// discovery behavior of the repo probes: when the cache directory lives
// inside some unrelated git repository (a dotfiles-managed $HOME, a
// hand-set SX_CACHE_DIR inside a checkout), git's upward repository
// discovery must not make a corrupt cache look healthy — recovery has
// to fire against the cache itself, not the ancestor.
func TestGitSourceHandler_CorruptCacheInsideAncestorRepo(t *testing.T) {
	// The cache root is itself a git repository, with a commit so HEAD
	// and status give us something to compare against afterwards.
	ancestor := t.TempDir()
	runGit(t, ancestor, "init")
	runGit(t, ancestor, "config", "user.email", "alice@example.com")
	runGit(t, ancestor, "config", "user.name", "Alice")
	if err := os.WriteFile(filepath.Join(ancestor, "README.md"), []byte("dotfiles\n"), 0644); err != nil {
		t.Fatalf("failed to write ancestor file: %v", err)
	}
	runGit(t, ancestor, "add", ".")
	runGit(t, ancestor, "commit", "-m", "ancestor commit")
	ancestorHead := gitOut(t, ancestor, "rev-parse", "HEAD")
	t.Setenv("SX_CACHE_DIR", ancestor)

	repoURL, sha := setupSourceGitRepo(t, 1)

	repoCache, err := cache.GetGitSourceCachePath(repoURL)
	if err != nil {
		t.Fatalf("failed to get cache path: %v", err)
	}
	// An interrupted clone left a .git directory git doesn't recognize.
	if err := os.MkdirAll(filepath.Join(repoCache, ".git"), 0755); err != nil {
		t.Fatalf("failed to create corrupt .git dir: %v", err)
	}

	handler := NewGitSourceHandler(git.NewClient())
	data, err := handler.Fetch(context.Background(), sourceGitAsset("asset0", repoURL, sha, "asset0"))
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if !utils.IsZipFile(data) {
		t.Fatal("fetched data is not a valid zip")
	}

	// The ancestor repository must be completely untouched: no fetch of
	// its remotes, no checkout switching its HEAD, no dirtied status.
	if got := gitOut(t, ancestor, "rev-parse", "HEAD"); got != ancestorHead {
		t.Fatalf("ancestor HEAD changed: %q -> %q", ancestorHead, got)
	}
	if status := gitOut(t, ancestor, "status", "--porcelain", "README.md"); strings.TrimSpace(status) != "" {
		t.Fatalf("ancestor working tree dirtied: %q", status)
	}
}

// TestGitVaultCloneOrUpdateRecoversCorruptClone: a vault clone whose
// .git is unusable must be discarded and re-cloned, not synced as if
// healthy — a corrupt clone that reads as an "empty vault" yields no
// lock file, which install treats as benign and responds to by
// uninstalling every asset from that vault.
func TestGitVaultCloneOrUpdateRecoversCorruptClone(t *testing.T) {
	t.Setenv("SX_CACHE_DIR", t.TempDir())

	repoURL, _ := setupSourceGitRepo(t, 1)

	v, err := NewGitVault(repoURL)
	if err != nil {
		t.Fatalf("failed to create vault: %v", err)
	}
	// An interrupted clone left a .git directory git doesn't recognize.
	if err := os.MkdirAll(filepath.Join(v.repoPath, ".git"), 0755); err != nil {
		t.Fatalf("failed to create corrupt .git: %v", err)
	}

	// Unlocked sync must refuse to repair (repair deletes shared state)…
	if err := v.cloneOrUpdate(context.Background()); !errors.Is(err, errCorruptVaultClone) {
		t.Fatalf("cloneOrUpdate = %v, want errCorruptVaultClone", err)
	}
	// …while the locked variant discards and re-clones.
	if err := v.cloneOrUpdateLocked(context.Background()); err != nil {
		t.Fatalf("cloneOrUpdateLocked failed: %v", err)
	}
	if !v.gitClient.IsRepo(context.Background(), v.repoPath) {
		t.Fatal("corrupt vault clone was not re-cloned")
	}

	// The other interrupted shape: content without .git. A bare clone
	// can never succeed there (git refuses non-empty destinations), so
	// it must take the same sentinel/repair path.
	if err := os.RemoveAll(filepath.Join(v.repoPath, ".git")); err != nil {
		t.Fatalf("failed to remove .git: %v", err)
	}
	v.lastSynced = time.Time{} // bypass the sync TTL for the next call
	if err := v.cloneOrUpdate(context.Background()); !errors.Is(err, errCorruptVaultClone) {
		t.Fatalf("cloneOrUpdate on stale dir = %v, want errCorruptVaultClone", err)
	}
	if err := v.cloneOrUpdateLocked(context.Background()); err != nil {
		t.Fatalf("cloneOrUpdateLocked on stale dir failed: %v", err)
	}
	if !v.gitClient.IsRepo(context.Background(), v.repoPath) {
		t.Fatal("stale vault directory was not re-cloned")
	}
}

// TestGitSourceHandler_FetchHealsPartialWorktree: a cache whose .git and
// HEAD are intact but whose working tree lost files (an interrupted
// delete) must heal on the next fetch. A plain checkout no-ops when
// HEAD already equals the pinned SHA, which made this shape permanent.
func TestGitSourceHandler_FetchHealsPartialWorktree(t *testing.T) {
	t.Setenv("SX_CACHE_DIR", t.TempDir())

	repoURL, sha := setupSourceGitRepo(t, 1)
	handler := NewGitSourceHandler(git.NewClient())

	if _, err := handler.Fetch(context.Background(), sourceGitAsset("asset0", repoURL, sha, "asset0")); err != nil {
		t.Fatalf("warm fetch failed: %v", err)
	}
	repoCache, err := cache.GetGitSourceCachePath(repoURL)
	if err != nil {
		t.Fatalf("failed to get cache path: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(repoCache, "asset0")); err != nil {
		t.Fatalf("failed to remove asset dir: %v", err)
	}

	data, err := handler.Fetch(context.Background(), sourceGitAsset("asset0", repoURL, sha, "asset0"))
	if err != nil {
		t.Fatalf("fetch after partial delete failed: %v", err)
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

	repoCache, err := cache.GetGitSourceCachePath(repoURL)
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
