package lockfile

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/Masterminds/semver/v3"
)

var (
	// gitCommitSHARegex matches full 40-character Git commit SHAs
	gitCommitSHARegex = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// ExtractInvalidSourceGitAssets moves every asset whose source-git ref
// is not a pinned 40-hex commit SHA out of Assets and into
// SkippedAssets, returning the moved assets.
//
// This is the enforcement point for the ref contract that a real
// command path actually reaches: source-git is a hand-authored manifest
// feature (no sx command constructs one), so a bad ref can only be
// discovered when the resolved lock file is consumed. Extracting the
// offending asset lets Validate pass for everything else — one bad
// entry costs one asset, visibly, instead of failing every consumer's
// entire install — while SkippedAssets keeps cleanup from mistaking
// "broken" for "removed".
func (lf *LockFile) ExtractInvalidSourceGitAssets() []Asset {
	var invalid []Asset
	skippedNames := make(map[string]bool)
	kept := lf.Assets[:0]
	for _, a := range lf.Assets {
		if a.HasInvalidSourceGitRef() {
			invalid = append(invalid, a)
			skippedNames[a.Name] = true
			continue
		}
		kept = append(kept, a)
	}
	lf.Assets = kept

	// A dependency on a skipped asset can never be satisfied, and
	// Validate would reject the whole lock file for the dangling
	// reference — extract the dependents too, to a fixpoint, so the
	// cost stays "the assets that can't work" rather than the vault.
	for changed := len(invalid) > 0; changed; {
		changed = false
		kept = lf.Assets[:0]
		for _, a := range lf.Assets {
			if dependsOnAny(a, skippedNames) {
				invalid = append(invalid, a)
				skippedNames[a.Name] = true
				changed = true
				continue
			}
			kept = append(kept, a)
		}
		lf.Assets = kept
	}

	lf.SkippedAssets = append(lf.SkippedAssets, invalid...)
	return invalid
}

// HasInvalidSourceGitRef reports whether the asset carries a source-git
// ref that is not a pinned 40-hex commit SHA.
func (a *Asset) HasInvalidSourceGitRef() bool {
	return a.SourceGit != nil && !gitCommitSHARegex.MatchString(a.SourceGit.Ref)
}

func dependsOnAny(a Asset, names map[string]bool) bool {
	for _, d := range a.Dependencies {
		if names[d.Name] {
			return true
		}
	}
	return false
}

// Validate validates the entire lock file
func (lf *LockFile) Validate() error {
	// Validate top-level fields
	if lf.LockVersion == "" {
		return errors.New("lock-version is required")
	}

	if lf.Version == "" {
		return errors.New("version is required")
	}

	if lf.CreatedBy == "" {
		return errors.New("created-by is required")
	}

	// Validate each asset
	names := make(map[string]bool)
	for i, ast := range lf.Assets {
		if err := ast.Validate(); err != nil {
			return fmt.Errorf("asset %d (%s): %w", i, ast.Name, err)
		}

		// Check for duplicate assets (name@version must be unique)
		key := ast.Key()
		if names[key] {
			return fmt.Errorf("duplicate asset: %s", key)
		}
		names[key] = true
	}

	// Validate dependencies reference existing assets
	assetMap := make(map[string]*Asset)
	for i := range lf.Assets {
		assetMap[lf.Assets[i].Name] = &lf.Assets[i]
	}

	for i, ast := range lf.Assets {
		for _, dep := range ast.Dependencies {
			if err := validateDependency(&dep, assetMap, &ast); err != nil {
				return fmt.Errorf("asset %d (%s): dependency %s: %w", i, ast.Name, dep.Name, err)
			}
		}
	}

	return nil
}

// Validate validates a single asset
func (a *Asset) Validate() error {
	// Validate required fields
	if a.Name == "" {
		return errors.New("name is required")
	}

	if a.Version == "" {
		return errors.New("version is required")
	}

	// Validate semantic version
	if _, err := semver.NewVersion(a.Version); err != nil {
		return fmt.Errorf("invalid semantic version %q: %w", a.Version, err)
	}

	if !a.Type.IsValid() {
		return fmt.Errorf("invalid asset type: %s", a.Type)
	}

	// Validate exactly one source is specified
	sourceCount := 0
	if a.SourceHTTP != nil {
		sourceCount++
	}
	if a.SourcePath != nil {
		sourceCount++
	}
	if a.SourceGit != nil {
		sourceCount++
	}

	if sourceCount == 0 {
		return errors.New("exactly one source must be specified (http, path, or git)")
	}
	if sourceCount > 1 {
		return errors.New("only one source type can be specified")
	}

	// Validate source-specific requirements
	if a.SourceHTTP != nil {
		if err := a.SourceHTTP.Validate(); err != nil {
			return fmt.Errorf("source-http: %w", err)
		}
	}
	if a.SourcePath != nil {
		if err := a.SourcePath.Validate(); err != nil {
			return fmt.Errorf("source-path: %w", err)
		}
	}
	if a.SourceGit != nil {
		if err := a.SourceGit.Validate(); err != nil {
			return fmt.Errorf("source-git: %w", err)
		}
	}

	// Validate scopes
	for i, scope := range a.Scopes {
		if err := scope.Validate(); err != nil {
			return fmt.Errorf("scopes[%d]: %w", i, err)
		}
	}

	return nil
}

// Validate validates a Scope entry
func (s *Scope) Validate() error {
	if s.Repo == "" {
		return errors.New("repo is required")
	}

	return nil
}

// Validate validates an HTTP source
func (s *SourceHTTP) Validate() error {
	if s.URL == "" {
		return errors.New("url is required")
	}

	// Validate hash algorithms if provided
	for algo := range s.Hashes {
		if algo != "sha256" && algo != "sha512" {
			return fmt.Errorf("unsupported hash algorithm: %s (must be sha256 or sha512)", algo)
		}
	}

	return nil
}

// Validate validates a path source
func (s *SourcePath) Validate() error {
	if s.Path == "" {
		return errors.New("path is required")
	}
	return nil
}

// Validate validates a Git source
func (s *SourceGit) Validate() error {
	if s.URL == "" {
		return errors.New("url is required")
	}

	if s.Ref == "" {
		return errors.New("ref is required")
	}

	// In lock files, ref must be a full commit SHA
	if !gitCommitSHARegex.MatchString(s.Ref) {
		return fmt.Errorf("ref must be a full 40-character commit SHA (got %q)", s.Ref)
	}

	return nil
}

// validateDependency validates a dependency reference
func validateDependency(dep *Dependency, assetMap map[string]*Asset, parent *Asset) error {
	if dep.Name == "" {
		return errors.New("dependency name is required")
	}

	// Check if dependency exists in lock file
	ast, exists := assetMap[dep.Name]
	if !exists {
		return errors.New("dependency not found in lock file")
	}

	// If version is specified, it must match
	if dep.Version != "" && dep.Version != ast.Version {
		return fmt.Errorf("dependency version %q does not match asset version %q", dep.Version, ast.Version)
	}

	// Check for self-dependency
	if dep.Name == parent.Name {
		return errors.New("asset cannot depend on itself")
	}

	return nil
}

// ValidateDependencies checks for circular dependencies using DFS
func (lf *LockFile) ValidateDependencies() error {
	// Build dependency graph
	graph := make(map[string][]string)
	for _, ast := range lf.Assets {
		deps := make([]string, 0, len(ast.Dependencies))
		for _, dep := range ast.Dependencies {
			deps = append(deps, dep.Name)
		}
		graph[ast.Name] = deps
	}

	// Check each asset for circular dependencies
	for _, ast := range lf.Assets {
		visited := make(map[string]bool)
		recStack := make(map[string]bool)

		if hasCycle(ast.Name, graph, visited, recStack) {
			return fmt.Errorf("circular dependency detected involving %s", ast.Name)
		}
	}

	return nil
}

// hasCycle detects cycles in the dependency graph using DFS
func hasCycle(node string, graph map[string][]string, visited, recStack map[string]bool) bool {
	visited[node] = true
	recStack[node] = true

	for _, neighbor := range graph[node] {
		if !visited[neighbor] {
			if hasCycle(neighbor, graph, visited, recStack) {
				return true
			}
		} else if recStack[neighbor] {
			return true
		}
	}

	recStack[node] = false
	return false
}
