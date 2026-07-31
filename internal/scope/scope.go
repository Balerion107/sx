package scope

import (
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sleuth-io/sx/v2/internal/git"
	"github.com/sleuth-io/sx/v2/internal/lockfile"
)

// Re-export scope type constants from lockfile for convenience
const (
	TypeGlobal = lockfile.ScopeGlobal
	TypeRepo   = lockfile.ScopeRepo
	TypePath   = lockfile.ScopePath
)

// Matcher matches assets based on scope
type Matcher struct {
	currentScope *Scope
}

// Scope represents the current working context
type Scope struct {
	Type     lockfile.ScopeType // TypeGlobal, TypeRepo, or TypePath
	RepoURL  string             // Repository URL (if in a repo)
	RepoPath string             // Path relative to repo root (if applicable)
}

// NewMatcher creates a new scope matcher
func NewMatcher(currentScope *Scope) *Matcher {
	return &Matcher{
		currentScope: currentScope,
	}
}

// MatchesAsset checks if an asset should be installed in the current scope
// An asset matches if:
// - It's global (no scopes) OR
// - It has a scope entry that matches the current context
func (m *Matcher) MatchesAsset(asset *lockfile.Asset) bool {
	// Global assets (no repositories) always match
	if asset.IsGlobal() {
		return true
	}

	// Check each repository entry to see if any match
	for _, repo := range asset.Scopes {
		if m.matchesRepository(&repo) {
			return true
		}
	}

	return false
}

// matchesRepository checks if a repository entry matches the current scope
func (m *Matcher) matchesRepository(repo *lockfile.Scope) bool {
	// If we're in global scope, repository-specific assets don't match
	if m.currentScope.Type == TypeGlobal {
		return false
	}

	// Check if repo URL matches
	if !m.matchesRepoURL(repo.Repo) {
		return false
	}

	// If repository has no paths, it matches the entire repo
	if len(repo.Paths) == 0 {
		return true
	}

	// When at repo root, include ALL path-scoped assets for this repo
	// They will be installed to their respective paths
	if m.currentScope.Type == TypeRepo {
		return true
	}

	// If we're in a specific path, check if current path matches any of them
	return slices.ContainsFunc(repo.Paths, m.matchesPath)
}

// matchesRepoURL checks if the asset's repo matches the current repo
func (m *Matcher) matchesRepoURL(assetRepo string) bool {
	if m.currentScope.RepoURL == "" || assetRepo == "" {
		return false
	}
	return MatchRepoURLs(m.currentScope.RepoURL, assetRepo)
}

// matchesPath checks if the asset's path matches the current path
func (m *Matcher) matchesPath(assetPath string) bool {
	if m.currentScope.RepoPath == "" || assetPath == "" {
		return false
	}

	// Normalize paths
	currentPath := normalizeRepoPath(m.currentScope.RepoPath)
	assetPath = normalizeRepoPath(assetPath)

	// Check if current path is within or equal to asset path
	// For example, if asset is scoped to "services/api"
	// and we're in "services/api/handlers", it should match
	return strings.HasPrefix(currentPath, assetPath) || currentPath == assetPath
}

// MatchRepoURLs checks if two repository URLs refer to the same repository.
// Each side is expanded into its normalized candidate set (the URL as
// written, plus its SSH alias resolution if one applies) and the URLs
// match when any candidates coincide. Comparing candidate sets means
// alias resolution can only widen matching — it never breaks a pair
// that already matched on the literal host.
func MatchRepoURLs(url1, url2 string) bool {
	for _, a := range NormalizeRepoURLCandidates(url1) {
		if slices.Contains(NormalizeRepoURLCandidates(url2), a) {
			return true
		}
	}
	return false
}

// NormalizeRepoURL normalizes a repository URL for comparison. All
// transports of the same repository reduce to "host/owner/repo":
// scp-style SSH remotes (git@host:owner/repo) are handled for any
// host, and userinfo, ports, a trailing ".git", and trailing slashes
// are dropped. Strings that don't look like a URL are returned
// cleaned but otherwise as-is.
func NormalizeRepoURL(repoURL string) string {
	cleaned := strings.TrimSpace(strings.ToLower(repoURL))
	cleaned = strings.TrimSuffix(cleaned, "/")
	cleaned = strings.TrimSuffix(cleaned, ".git")

	if host, path, ok := splitSCPLike(cleaned); ok {
		return host + "/" + strings.TrimLeft(path, "/")
	}

	u, err := url.Parse(cleaned)
	if err != nil || u.Host == "" {
		// Not URL-shaped (or already normalized to host/path form).
		return cleaned
	}
	return strings.TrimSuffix(u.Hostname()+u.Path, "/")
}

// NormalizeRepoURLCandidates returns every normalized form repoURL can
// take: the plain normalization and, when the URL is an SSH remote
// whose host is an alias defined in the user's ~/.ssh/config, the
// normalization with the alias replaced by its configured HostName.
// A remote like git@workgit:acme/x (Host workgit / HostName
// github.com) therefore also yields github.com/acme/x.
func NormalizeRepoURLCandidates(repoURL string) []string {
	base := NormalizeRepoURL(repoURL)

	host, path := sshHostAndPath(repoURL)
	if host == "" {
		return []string{base}
	}
	resolved, ok := lookupSSHHostname(host)
	resolved = strings.ToLower(strings.TrimSpace(resolved))
	if !ok || resolved == "" || resolved == host {
		return []string{base}
	}
	return []string{base, resolved + "/" + strings.TrimLeft(path, "/")}
}

// lookupSSHHostname resolves an SSH host alias to its configured
// HostName. Package-level so tests can stub the ~/.ssh/config lookup.
var lookupSSHHostname = git.SSHConfigHostname

// sshHostAndPath extracts the host and repo path from an SSH remote
// (scp-style or ssh://), already cleaned the same way NormalizeRepoURL
// cleans its input. Returns "" for non-SSH URLs.
func sshHostAndPath(repoURL string) (host, path string) {
	cleaned := strings.TrimSpace(strings.ToLower(repoURL))
	cleaned = strings.TrimSuffix(cleaned, "/")
	cleaned = strings.TrimSuffix(cleaned, ".git")

	if h, p, ok := splitSCPLike(cleaned); ok {
		return h, p
	}
	u, err := url.Parse(cleaned)
	if err != nil || u.Host == "" {
		return "", ""
	}
	switch u.Scheme {
	case "ssh", "git+ssh":
		return u.Hostname(), strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/")
	}
	return "", ""
}

// splitSCPLike splits an scp-style remote (user@host:path) into host
// and path. The host part cannot carry a port in this syntax, and a
// "://" anywhere means the string is a real URL instead.
func splitSCPLike(s string) (host, path string, ok bool) {
	if strings.Contains(s, "://") {
		return "", "", false
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 {
		return "", "", false
	}
	rest := s[at+1:]
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 || colon == len(rest)-1 {
		return "", "", false
	}
	return rest[:colon], rest[colon+1:], true
}

// normalizeRepoPath normalizes a repository-relative path
func normalizeRepoPath(path string) string {
	// Clean the path
	cleaned := filepath.Clean(path)

	// Remove leading slash or ./
	cleaned = strings.TrimPrefix(cleaned, "/")
	cleaned = strings.TrimPrefix(cleaned, "./")

	// Convert to forward slashes
	cleaned = filepath.ToSlash(cleaned)

	return cleaned
}

// GetInstallLocations returns all installation base directories for an asset in the current context
// An asset can have multiple installation locations if it has multiple repository entries
func GetInstallLocations(asset *lockfile.Asset, currentScope *Scope, repoRoot, globalBase string) []string {
	var locations []string

	// If global asset (no repositories), install to global base
	if asset.IsGlobal() {
		return []string{globalBase}
	}

	matcher := NewMatcher(currentScope)

	// Check each repository entry
	for _, repo := range asset.Scopes {
		if !matcher.matchesRepository(&repo) {
			continue
		}

		// If repository has paths, install to each path
		if len(repo.Paths) > 0 {
			for _, path := range repo.Paths {
				if matcher.matchesPath(path) {
					locations = append(locations, filepath.Join(repoRoot, path, ".claude"))
				}
			}
		} else {
			// No paths = entire repo
			locations = append(locations, filepath.Join(repoRoot, ".claude"))
		}
	}

	return locations
}
