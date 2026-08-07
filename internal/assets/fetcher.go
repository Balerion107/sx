package assets

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/schollz/progressbar/v3"

	"github.com/sleuth-io/sx/v2/internal/cache"
	"github.com/sleuth-io/sx/v2/internal/config"
	"github.com/sleuth-io/sx/v2/internal/git"
	"github.com/sleuth-io/sx/v2/internal/lockfile"
	"github.com/sleuth-io/sx/v2/internal/metadata"
	"github.com/sleuth-io/sx/v2/internal/utils"
	vaultpkg "github.com/sleuth-io/sx/v2/internal/vault"
)

// AssetFetcher handles fetching assets from a vault
type AssetFetcher struct {
	vault    vaultpkg.Vault
	vaultKey string
}

// NewAssetFetcher creates a new asset fetcher. vaultKey, when non-empty,
// partitions the disk cache by vault so two vaults that publish the
// same name@version don't collide. Empty key keeps the legacy global
// cache layout.
func NewAssetFetcher(vault vaultpkg.Vault, vaultKey string) *AssetFetcher {
	return &AssetFetcher{
		vault:    vault,
		vaultKey: vaultKey,
	}
}

// diskCacheable reports whether an asset's content is immutable for its
// declared version, and may therefore be served from the version-keyed
// disk cache. A source-git asset pinned to a moving branch can change
// while its locked version stays fixed — caching it would pin the
// first-ever fetch forever, defeating the fetch that tracks the remote.
// SHAs are immutable, and a ref that resolves to a tag in the local
// source cache is treated as immutable too: tags are the conventional
// release pin, and disabling the cache for them would cost a serialized
// network fetch per asset on every install. Tradeoff: a force-moved tag
// keeps serving cached content until the asset cache is cleared —
// accepted, since retagging is pathological while branches (the refs
// that legitimately move) stay uncached.
func diskCacheable(ctx context.Context, asset *lockfile.Asset) bool {
	if asset.SourceGit == nil {
		return true
	}
	ref := asset.SourceGit.Ref
	if isHexSHA(ref) {
		return true
	}
	if srcPath, err := cache.GetGitSourceCachePath(asset.SourceGit.URL); err == nil {
		if git.NewClient().HasRef(ctx, srcPath, "refs/tags/"+ref) {
			return true
		}
	}
	return false
}

func isHexSHA(ref string) bool {
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

// FetchAsset downloads a single asset
func (f *AssetFetcher) FetchAsset(ctx context.Context, asset *lockfile.Asset) (zipData []byte, meta *metadata.Metadata, err error) {
	// Try disk cache first (mutable-ref assets are never disk-cached)
	cacheable := diskCacheable(ctx, asset)
	zipData = nil
	err = errors.New("cache skipped")
	if cacheable {
		zipData, err = cache.LoadAssetFromDisk(asset.Name, asset.Version, f.vaultKey)
	}
	if err == nil {
		// Cache hit, extract metadata and return
		metadataBytes, err := utils.ReadZipFile(zipData, "metadata.toml")
		if err == nil {
			meta, err = metadata.Parse(metadataBytes)
			if err == nil && meta.Validate() == nil {
				// Valid cached asset
				return zipData, meta, nil
			}
		}
		// Cache corrupted, fall through to download
	}

	// Cache miss or invalid, download asset
	zipData, err = f.vault.GetAsset(ctx, asset)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to download asset: %w", err)
	}

	// Verify it's a valid zip
	if !utils.IsZipFile(zipData) {
		return nil, nil, errors.New("downloaded file is not a valid zip archive")
	}

	// Extract and parse metadata from zip
	metadataBytes, err := utils.ReadZipFile(zipData, "metadata.toml")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read metadata.toml from zip: %w", err)
	}

	meta, err = metadata.Parse(metadataBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	// Validate metadata
	if err := meta.Validate(); err != nil {
		return nil, nil, fmt.Errorf("metadata validation failed: %w", err)
	}

	// Cache to disk for future use. Recomputed: a first-ever tag fetch
	// finds no source clone before the download, but does after it.
	if cacheable || diskCacheable(ctx, asset) {
		_ = cache.SaveAssetToDisk(asset.Name, asset.Version, f.vaultKey, zipData)
	}
	// Ignore cache save errors - not critical

	return zipData, meta, nil
}

// FetchAssetWithProgress downloads a single asset with progress bar
func (f *AssetFetcher) FetchAssetWithProgress(ctx context.Context, asset *lockfile.Asset, bar *progressbar.ProgressBar) (zipData []byte, meta *metadata.Metadata, err error) {
	// Try disk cache first (mutable-ref assets are never disk-cached)
	cacheable := diskCacheable(ctx, asset)
	zipData = nil
	err = errors.New("cache skipped")
	if cacheable {
		zipData, err = cache.LoadAssetFromDisk(asset.Name, asset.Version, f.vaultKey)
	}
	if err == nil {
		// Cache hit, extract metadata and return
		metadataBytes, err := utils.ReadZipFile(zipData, "metadata.toml")
		if err == nil {
			meta, err = metadata.Parse(metadataBytes)
			if err == nil && meta.Validate() == nil {
				// Valid cached asset - complete progress bar immediately
				if bar != nil {
					bar.ChangeMax64(int64(len(zipData)))
					_ = bar.Set64(int64(len(zipData)))
				}
				return zipData, meta, nil
			}
		}
		// Cache corrupted, fall through to download
	}

	// Download asset through vault (handles auth properly)
	zipData, err = f.vault.GetAsset(ctx, asset)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to download asset: %w", err)
	}

	// Update progress bar to 100% after download
	if bar != nil {
		bar.ChangeMax64(int64(len(zipData)))
		_ = bar.Set64(int64(len(zipData)))
	}

	// Verify it's a valid zip
	if !utils.IsZipFile(zipData) {
		return nil, nil, errors.New("downloaded file is not a valid zip archive")
	}

	// Extract and parse metadata from zip
	metadataBytes, err := utils.ReadZipFile(zipData, "metadata.toml")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read metadata.toml from zip: %w", err)
	}

	meta, err = metadata.Parse(metadataBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	// Validate metadata
	if err := meta.Validate(); err != nil {
		return nil, nil, fmt.Errorf("metadata validation failed: %w", err)
	}

	// Cache to disk for future use. Recomputed: a first-ever tag fetch
	// finds no source clone before the download, but does after it.
	if cacheable || diskCacheable(ctx, asset) {
		_ = cache.SaveAssetToDisk(asset.Name, asset.Version, f.vaultKey, zipData)
	}
	// Ignore cache save errors - not critical

	return zipData, meta, nil
}

// FetchAssets downloads multiple assets in parallel
func (f *AssetFetcher) FetchAssets(ctx context.Context, assets []*lockfile.Asset, concurrency int) ([]DownloadResult, error) {
	if concurrency <= 0 {
		concurrency = 10 // Default
	}

	results := make([]DownloadResult, len(assets))
	tasks := make(chan DownloadTask, len(assets))
	resultChan := make(chan DownloadResult, len(assets))

	// Create worker pool
	var wg sync.WaitGroup
	for range concurrency {
		wg.Go(func() {
			for task := range tasks {
				select {
				case <-ctx.Done():
					resultChan <- DownloadResult{
						Asset: task.Asset,
						Error: ctx.Err(),
						Index: task.Index,
					}
					return
				default:
				}

				// Create progress bar for this asset if not in silent mode
				var bar *progressbar.ProgressBar
				if !config.IsSilent() {
					bar = progressbar.NewOptions64(
						-1, // Unknown size initially
						progressbar.OptionSetDescription(fmt.Sprintf("[%d/%d] %s", task.Index+1, len(assets), task.Asset.Name)),
						progressbar.OptionSetWidth(30),
						progressbar.OptionShowBytes(true),
						progressbar.OptionSetPredictTime(true),
						progressbar.OptionClearOnFinish(),
					)
				}

				zipData, meta, err := f.FetchAssetWithProgress(ctx, task.Asset, bar)

				if bar != nil {
					_ = bar.Finish()
				}

				resultChan <- DownloadResult{
					Asset:    task.Asset,
					ZipData:  zipData,
					Metadata: meta,
					Error:    err,
					Index:    task.Index,
				}
			}
		})
	}

	// Send tasks
	go func() {
		for i, asset := range assets {
			tasks <- DownloadTask{
				Asset: asset,
				Index: i,
			}
		}
		close(tasks)
	}()

	// Collect results
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	for result := range resultChan {
		results[result.Index] = result
	}

	return results, nil
}
