package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/sleuth-io/sx/v2/internal/cache"
	"github.com/sleuth-io/sx/v2/internal/git"
	"github.com/sleuth-io/sx/v2/internal/lockfile"
	"github.com/sleuth-io/sx/v2/internal/utils"
)

// GitSourceHandler handles assets with source-git
type GitSourceHandler struct {
	gitClient *git.Client
}

// NewGitSourceHandler creates a new Git source handler
func NewGitSourceHandler(gitClient *git.Client) *GitSourceHandler {
	return &GitSourceHandler{
		gitClient: gitClient,
	}
}

// Fetch clones/fetches a git repository and retrieves the asset
func (g *GitSourceHandler) Fetch(ctx context.Context, asset *lockfile.Asset) ([]byte, error) {
	if asset.SourceGit == nil {
		return nil, errors.New("asset does not have source-git")
	}

	source := asset.SourceGit

	// Get cache path for this repository
	repoCache, err := cache.GetGitRepoCachePath(source.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to get cache path: %w", err)
	}

	// The cache dir is keyed by URL alone, so every asset from the same
	// repo shares one clone/checkout. Hold the lock for the whole fetch:
	// checkout mutates the shared working tree, so even the read below
	// must not overlap a concurrent checkout of a different ref.
	fileLock, err := acquireRepoCacheLock(ctx, repoCache)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire repo lock: %w", err)
	}
	defer func() { _ = fileLock.Unlock() }()

	// Refs are pinned 40-char SHAs, so once one fetch has brought the
	// commit into the shared cache, the queued fetches behind it (and any
	// later install) can skip the network round-trip entirely.
	if !isFullSHA(source.Ref) || !g.gitClient.HasCommit(ctx, repoCache, source.Ref) {
		if err := g.cloneOrUpdate(ctx, source.URL, repoCache); err != nil {
			return nil, fmt.Errorf("failed to clone/update repository: %w", err)
		}
	}

	// Checkout the specific commit
	if err := g.checkout(ctx, repoCache, source.Ref); err != nil {
		return nil, fmt.Errorf("failed to checkout ref %s: %w", source.Ref, err)
	}

	// Determine the directory to look for the asset
	searchDir := repoCache
	if source.Subdirectory != "" {
		searchDir = filepath.Join(repoCache, source.Subdirectory)
		if !utils.IsDirectory(searchDir) {
			return nil, fmt.Errorf("subdirectory not found: %s", source.Subdirectory)
		}
	}

	// First, try to find .zip files in the directory
	zipFiles, err := g.findZipFiles(searchDir)
	if err != nil {
		return nil, fmt.Errorf("failed to find zip files: %w", err)
	}

	if len(zipFiles) > 0 {
		// Found zip files - use the first one or match by name
		var zipFile string
		if len(zipFiles) == 1 {
			zipFile = zipFiles[0]
		} else {
			for _, f := range zipFiles {
				base := filepath.Base(f)
				if strings.HasPrefix(base, asset.Name) {
					zipFile = f
					break
				}
			}
			if zipFile == "" {
				zipFile = zipFiles[0] // Default to first
			}
		}

		// Read the zip file
		data, err := os.ReadFile(zipFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read zip file: %w", err)
		}

		// Verify it's a valid zip file
		if !utils.IsZipFile(data) {
			return nil, fmt.Errorf("file is not a valid zip archive: %s", zipFile)
		}

		return data, nil
	}

	// No zip files found - check if this is an exploded directory
	// Look for metadata.toml to confirm it's an asset directory
	metadataPath := filepath.Join(searchDir, "metadata.toml")
	if utils.FileExists(metadataPath) {
		// This is an exploded asset directory - create a zip from it
		data, err := utils.CreateZip(searchDir)
		if err != nil {
			return nil, fmt.Errorf("failed to create zip from directory: %w", err)
		}
		return data, nil
	}

	return nil, fmt.Errorf("no zip files or exploded asset directory found in %s", searchDir)
}

// repoCacheLockTimeout bounds how long a caller waits for another
// holder of a repo cache lock. Generous because the holder may be
// cloning a large repository over the network; its purpose is only to
// turn a wedged holder into an error instead of an infinite silent spin.
const repoCacheLockTimeout = 10 * time.Minute

// acquireRepoCacheLock serializes access to a URL-keyed git cache
// directory across goroutines and processes. GitVault.acquireFileLock
// delegates here, so the locked vault operations (GetLockFile, AddAsset,
// path-source GetAsset) and source-git fetches of the same repo queue on
// the same lock; vault read paths that sync without locking are not
// covered.
//
// flock is not re-entrant: two flock.Flock instances conflict even in
// one process, so a caller already holding this repo's lock must not
// call this again — it would block until the timeout.
func acquireRepoCacheLock(ctx context.Context, repoPath string) (*flock.Flock, error) {
	lockFile := repoPath + ".lock"

	if err := os.MkdirAll(filepath.Dir(lockFile), 0755); err != nil {
		return nil, fmt.Errorf("failed to create lock directory: %w", err)
	}

	fileLock := flock.New(lockFile)

	lockCtx, cancel := context.WithTimeout(ctx, repoCacheLockTimeout)
	defer cancel()
	locked, err := fileLock.TryLockContext(lockCtx, 100*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire file lock: %w", err)
	}
	if !locked {
		return nil, errors.New("could not acquire file lock (timeout)")
	}

	return fileLock, nil
}

// isFullSHA reports whether ref is a full 40-character commit SHA.
func isFullSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, c := range ref {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// cloneOrUpdate clones the repository if it doesn't exist, or fetches updates if it does.
// Callers must hold the repo cache lock (see acquireRepoCacheLock).
func (g *GitSourceHandler) cloneOrUpdate(ctx context.Context, repoURL, repoPath string) error {
	// Presence check, not IsDirectory: .git can legitimately be a gitlink
	// file, and treating one as stale would delete a healthy repo.
	if utils.FileExists(filepath.Join(repoPath, ".git")) {
		err := g.fetch(ctx, repoPath)
		if err == nil {
			return nil
		}
		// A fetch can fail because the network is down or because an
		// interrupted clone left the cache corrupt. Only discard the
		// cache when git says it is not a repository; a healthy clone
		// with a transient network error keeps its objects.
		if g.gitClient.IsRepo(ctx, repoPath) {
			return err
		}
		if rmErr := os.RemoveAll(repoPath); rmErr != nil {
			return fmt.Errorf("failed to remove corrupt cache directory: %w", rmErr)
		}
	} else if utils.IsDirectory(repoPath) {
		// A directory without .git is a remnant of an interrupted clone;
		// git refuses to clone into a non-empty directory, so clear it.
		if err := os.RemoveAll(repoPath); err != nil {
			return fmt.Errorf("failed to remove stale cache directory: %w", err)
		}
	}

	return g.clone(ctx, repoURL, repoPath)
}

// clone clones a git repository
func (g *GitSourceHandler) clone(ctx context.Context, repoURL, repoPath string) error {
	return g.gitClient.Clone(ctx, repoURL, repoPath)
}

// fetch fetches updates from the remote repository
func (g *GitSourceHandler) fetch(ctx context.Context, repoPath string) error {
	return g.gitClient.Fetch(ctx, repoPath)
}

// checkout checks out a specific ref (commit SHA)
func (g *GitSourceHandler) checkout(ctx context.Context, repoPath, ref string) error {
	return g.gitClient.Checkout(ctx, repoPath, ref)
}

// findZipFiles finds all .zip files in a directory (non-recursive)
func (g *GitSourceHandler) findZipFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var zipFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".zip") {
			zipFiles = append(zipFiles, filepath.Join(dir, entry.Name()))
		}
	}

	return zipFiles, nil
}

// ResolveRef resolves a branch or tag name to a commit SHA
// This is used during lock file generation to convert friendly names to commit SHAs
func (g *GitSourceHandler) ResolveRef(ctx context.Context, repoURL, ref string) (string, error) {
	// Get cache path for this repository
	repoCache, err := cache.GetGitRepoCachePath(repoURL)
	if err != nil {
		return "", fmt.Errorf("failed to get cache path: %w", err)
	}

	// Serialize with concurrent fetches sharing this cache directory
	fileLock, err := acquireRepoCacheLock(ctx, repoCache)
	if err != nil {
		return "", fmt.Errorf("failed to acquire repo lock: %w", err)
	}
	defer func() { _ = fileLock.Unlock() }()

	// Clone or update repository
	if err := g.cloneOrUpdate(ctx, repoURL, repoCache); err != nil {
		return "", fmt.Errorf("failed to clone/update repository: %w", err)
	}

	// Resolve ref to commit SHA
	sha, err := g.gitClient.RevParse(ctx, repoCache, ref)
	if err != nil {
		return "", err
	}

	if len(sha) != 40 {
		return "", fmt.Errorf("invalid commit SHA: %s", sha)
	}

	return sha, nil
}
