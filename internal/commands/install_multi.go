package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"sync"

	"github.com/sleuth-io/sx/v2/internal/assets"
	"github.com/sleuth-io/sx/v2/internal/clients"
	"github.com/sleuth-io/sx/v2/internal/config"
	"github.com/sleuth-io/sx/v2/internal/lockfile"
	"github.com/sleuth-io/sx/v2/internal/logger"
	"github.com/sleuth-io/sx/v2/internal/mgmt"
	"github.com/sleuth-io/sx/v2/internal/scope"
	"github.com/sleuth-io/sx/v2/internal/ui"
	"github.com/sleuth-io/sx/v2/internal/ui/components"
	vaultpkg "github.com/sleuth-io/sx/v2/internal/vault"
)

// profileLockFile pairs a fetched lock file with the profile and vault
// it came from. Used by the multi-active install flow to route asset
// downloads back to the originating vault and to attribute conflicts.
type profileLockFile struct {
	ProfileName string
	Config      *config.Config
	Vault       vaultpkg.Vault
	LockFile    *lockfile.LockFile
	FetchErr    error
	// RepairDiscarded is the short SHA of a local vault-cache commit that
	// --repair reset away to match the remote (empty if nothing was discarded).
	RepairDiscarded string
}

// vaultRepairer is implemented by vaults whose local cache can drift from the
// authoritative remote — i.e. the git vault, whose clone can be left with an
// unpushed commit by an interrupted mutation. RepairVaultClone re-syncs the
// cache to the remote, returning the discarded local tip (or "").
type vaultRepairer interface {
	RepairVaultClone(ctx context.Context) (discardedTip string, err error)
}

// loadActiveProfilesAndLockFiles is the top-level helper for runInstall's
// per-profile bootstrap. It loads every active profile, fetches each
// vault's lock file, applies the partial-failure policy, and returns
// the data the rest of the install flow needs. The done bool is true
// when runInstall should return nil (e.g. all profiles report no lock
// file yet — fresh setup).
func loadActiveProfilesAndLockFiles(
	ctx context.Context,
	status *components.Status,
	styledOut *ui.Output,
	repair bool,
) (profileLocks []profileLockFile, mpc *config.MultiProfileConfig, primaryCfg *config.Config, cfg *config.Config, done bool, err error) {
	activeConfigs, mpc, err := config.LoadActive()
	if err != nil {
		return nil, nil, nil, nil, false, fmt.Errorf("failed to load configuration: %w\nRun 'sx init' to configure", err)
	}
	if len(activeConfigs) == 0 {
		return nil, nil, nil, nil, false, errors.New("no active profiles configured — run 'sx profile activate <name>'")
	}
	// Validate every active config up front; an invalid entry is a
	// configuration error the user should fix regardless of fetch order.
	for _, c := range activeConfigs {
		if validateErr := c.Validate(); validateErr != nil {
			return nil, nil, nil, nil, false, fmt.Errorf("invalid configuration for profile %s: %w", c.ProfileName, validateErr)
		}
	}
	// Seed the process-global identity from the first active profile so
	// any code path that runs before the parallel fan-out completes (or
	// that reads the override without a context) sees a sane value.
	// loadActiveLockFiles passes per-profile identity via context so the
	// global override is not touched mid-fetch.
	mgmt.SetIdentityOverride(activeConfigs[0].Identity)

	profileLocks = loadActiveLockFiles(ctx, activeConfigs, status, repair)
	if repair {
		for _, pl := range profileLocks {
			if pl.RepairDiscarded != "" {
				styledOut.Warning(fmt.Sprintf("Repaired %s vault cache: discarded unpushed local commit %s to match the remote.", pl.ProfileName, pl.RepairDiscarded))
			}
		}
	}
	if !reportFetchErrors(profileLocks, styledOut) {
		// All profiles failed. A pristine "no lock file yet" outcome is
		// the new-user case (every profile reports ErrLockFileNotFound).
		// Anything else is a hard failure: embed the underlying fetch
		// errors in the returned error rather than pointing at the
		// warnings — in hook mode the output is silent, so the error is
		// the only diagnostic the caller sees.
		var fetchErrs []error
		for _, pl := range profileLocks {
			if pl.FetchErr == nil || errors.Is(pl.FetchErr, vaultpkg.ErrLockFileNotFound) {
				continue
			}
			fetchErrs = append(fetchErrs, fmt.Errorf("profile %s: %w", pl.ProfileName, pl.FetchErr))
		}
		if len(fetchErrs) > 0 {
			return nil, nil, nil, nil, false, fmt.Errorf("no active profile produced a lock file: %w", errors.Join(fetchErrs...))
		}
		styledOut.Info("No assets installed yet.")
		styledOut.Muted("Add skills with 'sx add' or browse skills.sh with 'sx add --browse'.")
		return nil, nil, nil, nil, true, nil
	}

	// Primary = first profile that actually contributed a lock file.
	// This is the profile that owns audit attribution for any
	// post-fetch single-vault work (env detection, hooks, ensure-asset-
	// support) and is the fallback identity when no per-vault swap
	// applies. Using the first *active* profile would misattribute when
	// the default's fetch failed but a secondary succeeded.
	primaryCfg = activeConfigs[0]
	cfg = primaryCfg
	for _, pl := range profileLocks {
		if pl.LockFile != nil {
			primaryCfg = pl.Config
			cfg = pl.Config
			break
		}
	}
	mgmt.SetIdentityOverride(primaryCfg.Identity)
	mgmt.SetAuditProfileTag(primaryCfg.ProfileName)
	return profileLocks, mpc, primaryCfg, cfg, false, nil
}

// loadActiveLockFiles fetches lock files for every active profile in
// parallel, honoring per-profile identity overrides so team/user scope
// resolution happens against the right email. Each goroutine carries
// its profile's identity on its context (mgmt.ContextWithIdentity), so
// the process-global override is not touched and concurrent fetches
// cannot bleed identity into each other. Audit attribution doesn't
// matter during fetch (no mutations), so the global audit tag is left
// for the caller to set after the fan-out. Individual fetch failures
// are captured per-profile rather than failing the whole call so
// partial installs can proceed.
func loadActiveLockFiles(ctx context.Context, configs []*config.Config, status *components.Status, repair bool) []profileLockFile {
	results := make([]profileLockFile, len(configs))
	if len(configs) == 0 {
		return results
	}

	if len(configs) == 1 {
		status.Start("Fetching lock file")
	} else {
		status.Start(fmt.Sprintf("Fetching lock files for %d profiles", len(configs)))
	}

	var wg sync.WaitGroup
	for i, cfg := range configs {
		wg.Add(1)
		go func(i int, cfg *config.Config) {
			defer wg.Done()
			entry := profileLockFile{ProfileName: cfg.ProfileName, Config: cfg}
			fetchCtx := mgmt.ContextWithIdentity(ctx, cfg.Identity)
			vault, err := vaultpkg.NewFromConfig(cfg)
			if err != nil {
				entry.FetchErr = fmt.Errorf("failed to create vault for profile %s: %w", cfg.ProfileName, err)
				results[i] = entry
				return
			}
			entry.Vault = vault
			// In repair mode, re-sync the local vault cache to the remote
			// BEFORE resolving the lock file, so the lock reflects the
			// remote's authoritative scopes rather than a stale/diverged
			// local clone (SD-10170). Repair failures are non-fatal — fall
			// through to a normal fetch and let it surface any real error.
			if repair {
				if r, ok := vault.(vaultRepairer); ok {
					if discarded, rerr := r.RepairVaultClone(fetchCtx); rerr == nil {
						entry.RepairDiscarded = discarded
					}
				}
			}
			lf, err := fetchLockFile(fetchCtx, vault, cfg)
			if err != nil {
				entry.FetchErr = err
			} else {
				entry.LockFile = lf
			}
			results[i] = entry
		}(i, cfg)
	}
	wg.Wait()
	status.Clear()
	return results
}

// assetConflict records that two or more active profiles publish an
// asset with the same name. Winner is the profile whose copy is being
// installed; Shadowed is the rest.
type assetConflict struct {
	AssetName string
	Winner    string
	Shadowed  []string
}

// mergeApplicableAssets runs scope filtering + dependency resolution per
// profile, then folds the results into a single ordered list. The
// caller controls precedence by ordering profileLocks (default-first
// for the persisted case, user-specified for --profile overrides). On
// name collision the first encountered wins; later occurrences are
// reported via conflicts so reportConflicts can decide how loudly to
// surface them.
//
// Per-profile identity was already applied during lock fetch in
// loadActiveLockFiles; the resolver only reads lockfile bytes, so it
// doesn't touch the identity override here.
func mergeApplicableAssets(
	profileLocks []profileLockFile,
	targetClients []clients.Client,
	matcherScope *scope.Matcher,
) (sortedAssets []*lockfile.Asset, assetOrigin map[string]string, conflicts []assetConflict, skips *scopeSkips, err error) {
	assetOrigin = make(map[string]string)
	conflictByName := make(map[string]*assetConflict)
	skips = &scopeSkips{}

	for _, pl := range profileLocks {
		if pl.LockFile == nil {
			continue
		}
		applicable := filterAssetsByScope(pl.LockFile, targetClients, matcherScope, skips)
		sorted, resolveErr := resolveAssetDependencies(pl.LockFile, applicable)
		if resolveErr != nil {
			return nil, nil, nil, skips, fmt.Errorf("dependency resolution for profile %s: %w", pl.ProfileName, resolveErr)
		}
		for _, asset := range sorted {
			if existing, taken := assetOrigin[asset.Name]; taken {
				rec, ok := conflictByName[asset.Name]
				if !ok {
					rec = &assetConflict{AssetName: asset.Name, Winner: existing}
					conflictByName[asset.Name] = rec
				}
				rec.Shadowed = append(rec.Shadowed, pl.ProfileName)
				continue
			}
			sortedAssets = append(sortedAssets, asset)
			assetOrigin[asset.Name] = pl.ProfileName
		}
	}

	if len(conflictByName) > 0 {
		names := make([]string, 0, len(conflictByName))
		for n := range conflictByName {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			conflicts = append(conflicts, *conflictByName[n])
		}
	}

	// An asset can be scope-skipped and still end up resolved: dependency
	// resolution pulls missing dependencies from the full lock file
	// regardless of scope, and another profile can resolve a name this
	// profile skipped. Anything that resolved installs, so it must not
	// also be reported as skipped.
	for name := range assetOrigin {
		skips.unrecord(name)
	}
	return sortedAssets, assetOrigin, conflicts, skips, nil
}

// profileMetadata pairs identity + audit-tag context for the profiles
// that own at least one downloaded asset. downloadAssetsMultiVault
// consults this to swap the process-global identity/audit overrides
// for each vault group's fetch.
type profileMetadata struct {
	Identity string
	Profile  string
	Vault    vaultpkg.Vault
	// VaultKey is the vault's repo URL (or server URL for Sleuth) used
	// to partition the asset disk cache so two vaults publishing the
	// same name@version don't collide.
	VaultKey string
}

// buildProfileMetadata derives the per-profile context from the slice
// of profile lock files, indexed by ProfileName. Only profiles that
// successfully fetched a lock file (and thus participated in the merge)
// are included.
func buildProfileMetadata(profileLocks []profileLockFile) map[string]profileMetadata {
	out := make(map[string]profileMetadata, len(profileLocks))
	for _, pl := range profileLocks {
		if pl.LockFile == nil || pl.Config == nil {
			continue
		}
		out[pl.ProfileName] = profileMetadata{
			Identity: pl.Config.Identity,
			Profile:  pl.ProfileName,
			Vault:    pl.Vault,
			VaultKey: pl.Config.VaultIdentifier(),
		}
	}
	return out
}

// hadHardFetchFailure reports whether any active profile failed to
// fetch its lock file with something other than ErrLockFileNotFound.
// Used to gate removed-asset cleanup so a transient outage on one
// profile doesn't cause sx to uninstall assets that belong to another
// active profile (since cleanup compares against the merged-but-
// partial sortedAssets set).
func hadHardFetchFailure(profileLocks []profileLockFile) bool {
	for _, pl := range profileLocks {
		if pl.FetchErr == nil {
			continue
		}
		if errors.Is(pl.FetchErr, vaultpkg.ErrLockFileNotFound) {
			continue
		}
		return true
	}
	return false
}

// reportFetchErrors surfaces per-profile lock file fetch failures as
// warnings. Returns true if at least one profile fetched successfully.
func reportFetchErrors(profileLocks []profileLockFile, styledOut *ui.Output) bool {
	log := logger.Get()
	successCount := 0
	for _, pl := range profileLocks {
		if pl.LockFile != nil {
			successCount++
			continue
		}
		if pl.FetchErr == nil || errors.Is(pl.FetchErr, vaultpkg.ErrLockFileNotFound) {
			continue
		}
		styledOut.Warning(fmt.Sprintf("profile %s: %v", pl.ProfileName, pl.FetchErr))
		log.Error("profile lockfile fetch failed", "profile", pl.ProfileName, "error", pl.FetchErr)
	}
	return successCount > 0
}

// reportConflicts emits a per-shadowed-asset notice. The agreed policy
// is "default wins silently, otherwise first-active wins with a
// warning" — so we suppress the loud warning only when the winner is
// actually the persisted default profile.
func reportConflicts(conflicts []assetConflict, defaultProfile string, styledOut *ui.Output) {
	log := logger.Get()
	for _, c := range conflicts {
		shadowed := c.Shadowed
		msg := fmt.Sprintf("asset %s: kept from %s, shadowed in %v", c.AssetName, c.Winner, shadowed)
		log.Warn("asset conflict between profiles", "asset", c.AssetName, "winner", c.Winner, "shadowed", shadowed)
		if defaultProfile != "" && c.Winner == defaultProfile {
			styledOut.Muted(msg)
			continue
		}
		styledOut.Warning(msg)
	}
}

// downloadAssetsMultiVault downloads each asset from the vault its
// origin profile points at, swapping the process-global identity and
// audit-profile-tag overrides per group so a profile's downloads run
// under that profile's identity. Failures of one vault group are
// reported as warnings; the call only errors out when no group produced
// any successful downloads. Caller is responsible for restoring the
// primary identity/audit-tag after the function returns.
func downloadAssetsMultiVault(
	ctx context.Context,
	assetsToInstall []*lockfile.Asset,
	assetOrigin map[string]string,
	profileMeta map[string]profileMetadata,
	profileOrder []string,
	status *components.Status,
	styledOut *ui.Output,
) (*downloadAssetsResult, error) {
	if len(assetsToInstall) == 0 {
		return &downloadAssetsResult{}, nil
	}

	// Group assets by origin profile so we issue one batched fetch per
	// profile (preserving the existing per-vault concurrency limit) and
	// have a stable iteration order matching the input config.
	type group struct {
		profile string
		assets  []*lockfile.Asset
	}
	groupsByProfile := make(map[string]*group)
	for _, asset := range assetsToInstall {
		origin, ok := assetOrigin[asset.Name]
		if !ok {
			return nil, fmt.Errorf("no origin profile recorded for asset %s", asset.Name)
		}
		if _, hasMeta := profileMeta[origin]; !hasMeta {
			return nil, fmt.Errorf("no profile metadata for origin %s of asset %s", origin, asset.Name)
		}
		g, exists := groupsByProfile[origin]
		if !exists {
			g = &group{profile: origin}
			groupsByProfile[origin] = g
		}
		g.assets = append(g.assets, asset)
	}

	// Build an ordered slice using profileOrder so output is stable.
	orderedGroups := make([]*group, 0, len(groupsByProfile))
	for _, name := range profileOrder {
		if g, ok := groupsByProfile[name]; ok {
			orderedGroups = append(orderedGroups, g)
		}
	}
	// Defensive: include any group not in profileOrder (shouldn't
	// happen, but guards against silently dropping assets). Sort the
	// missing names so output stays deterministic on the safety path,
	// matching the primary path's profileOrder iteration.
	var missing []string
	for name := range groupsByProfile {
		if !slices.ContainsFunc(orderedGroups, func(o *group) bool { return o.profile == name }) {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		orderedGroups = append(orderedGroups, groupsByProfile[name])
	}

	status.Start(fmt.Sprintf("Downloading %d assets", len(assetsToInstall)))

	var merged []assets.DownloadResult
	var groupErrs []error
	for _, g := range orderedGroups {
		meta := profileMeta[g.profile]
		mgmt.SetIdentityOverride(meta.Identity)
		mgmt.SetAuditProfileTag(meta.Profile)

		fetcher := assets.NewAssetFetcher(meta.Vault, meta.VaultKey)
		results, err := fetcher.FetchAssets(ctx, g.assets, 10)
		if err != nil {
			groupErrs = append(groupErrs, fmt.Errorf("profile %s: %w", g.profile, err))
			continue
		}
		merged = append(merged, results...)
	}

	// Stop the spinner before printing any human-facing diagnostics so
	// per-group warnings and per-asset error lines don't interleave
	// with spinner redraws on a multi-vault failure.
	status.Clear()

	for _, err := range groupErrs {
		styledOut.Warning(err.Error())
	}

	result := processDownloadResults(merged, styledOut)

	if len(result.Downloads) == 0 {
		if len(groupErrs) > 0 {
			return nil, errors.New("no assets downloaded successfully (every vault download failed; see warnings above)")
		}
		styledOut.Error("No assets downloaded successfully")
		return nil, errors.New("no assets downloaded successfully")
	}

	return result, nil
}

// skippedAssetNames collects the names of assets excluded from
// installation at fetch time (see LockFile.SkippedAssets); removed-asset
// cleanup treats them as still present so they are not uninstalled.
func skippedAssetNames(profileLocks []profileLockFile) map[string]bool {
	names := make(map[string]bool)
	for _, pl := range profileLocks {
		if pl.LockFile == nil {
			continue
		}
		for _, a := range pl.LockFile.SkippedAssets {
			names[a.Name] = true
		}
	}
	return names
}

// skippedAssetReason is the single source of the human-readable reason
// an asset was excluded at lock-fetch time; every surface (install
// warning, dry-run preview, log) shares it so they cannot drift.
func skippedAssetReason(a lockfile.Asset) string {
	if a.HasInvalidSourceGitRef() {
		return fmt.Sprintf("source-git ref %q is not a pinned 40-character commit SHA — fix it in the vault's sx.toml", a.SourceGit.Ref)
	}
	return "it depends on a skipped asset"
}

// forEachSkippedLockAsset visits each fetch-time-skipped asset once
// (deduped by name across profiles).
func forEachSkippedLockAsset(profileLocks []profileLockFile, visit func(a lockfile.Asset)) {
	seen := make(map[string]bool)
	for _, pl := range profileLocks {
		if pl.LockFile == nil {
			continue
		}
		for _, a := range pl.LockFile.SkippedAssets {
			if seen[a.Name] {
				continue
			}
			seen[a.Name] = true
			visit(a)
		}
	}
}

// reportSkippedLockAssets surfaces assets excluded at lock-fetch time —
// once per asset per run, after the fetch fan-out, because fetchLockFile
// runs in per-profile goroutines and must stay out of shared output.
// Also logged unconditionally: styledOut is silenced in hook mode (the
// dominant install path), and the condition persists until the vault
// owner fixes sx.toml, so the log file must carry the diagnosis.
func reportSkippedLockAssets(profileLocks []profileLockFile, styledOut *ui.Output) {
	forEachSkippedLockAsset(profileLocks, func(a lockfile.Asset) {
		logger.Get().Warn("skipping asset from lock file", "asset", a.Name, "reason", skippedAssetReason(a))
		styledOut.Warning(fmt.Sprintf("Skipping asset %q: %s", a.Name, skippedAssetReason(a)))
	})
}

// printDryRunSkippedLockAssets is the dry-run counterpart: --dry-run is
// exactly where a user asks "why isn't my asset installing", so the
// skipped set must not be silently absent from the preview.
func printDryRunSkippedLockAssets(w io.Writer, profileLocks []profileLockFile) {
	forEachSkippedLockAsset(profileLocks, func(a lockfile.Asset) {
		fmt.Fprintf(w, "# skipped %s: %s\n", a.Name, skippedAssetReason(a))
	})
}
