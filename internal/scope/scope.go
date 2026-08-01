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

// NearMissScope returns the first repo scope of an asset that failed
// scope matching yet names the same owner/repo path as the current
// repo — possibly a URL-form mismatch (unresolvable SSH alias,
// unexpected host), possibly a genuinely different host carrying the
// same project path. Callers surface it so the user can make that
// call, as opposed to an asset simply scoped to a different repo.
func (m *Matcher) NearMissScope(asset *lockfile.Asset) (repo string, ok bool) {
	if m.currentScope.Type == TypeGlobal || m.currentScope.RepoURL == "" {
		return "", false
	}
	for _, s := range asset.Scopes {
		if LooksLikeSameRepo(m.currentScope.RepoURL, s.Repo) {
			return s.Repo, true
		}
	}
	return "", false
}

// matchesRepoURL checks if the asset's repo matches the current repo.
// The asset side is the stored value, so it gets the stored-side
// legacy ported reading on top of the symmetric comparison.
func (m *Matcher) matchesRepoURL(assetRepo string) bool {
	if m.currentScope.RepoURL == "" || assetRepo == "" {
		return false
	}
	return MatchStoredRepoURL(assetRepo, m.currentScope.RepoURL)
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
// scp-style SSH remotes (git@host:owner/repo or host:owner/repo) are
// handled for any host, and userinfo, ports, a trailing ".git", and
// trailing slashes are dropped. Note that dropping the port collapses
// distinct git servers hosted on different ports of one hostname —
// the deliberate trade that lets ssh://host:2222/x match
// https://host/x. Strings that don't look like a URL are returned
// cleaned but otherwise as-is.
func NormalizeRepoURL(repoURL string) string {
	cleaned := CleanRepoURL(repoURL)

	if host, path, ok := splitSCPLike(cleaned); ok {
		return bracketIPv6(host) + "/" + strings.TrimLeft(path, "/")
	}

	u, err := url.Parse(cleaned)
	if err != nil {
		return cleaned
	}
	if u.Host == "" {
		// Local paths and file:// URLs identify a repository by path
		// alone — reduce them as the pre-branch normalizer did, so
		// /srv/git/x, file:///srv/git/x, and rows stored by older sx
		// versions (srv/git/x) all agree. Everything else host-less
		// (opaque forms like "gitea.corp.com:3000", Windows drive
		// paths whose scheme parses as the drive letter, already
		// normalized host/path strings) stays as-is.
		if u.Scheme == "" || u.Scheme == "file" {
			if p := strings.TrimPrefix(u.Path, "/"); p != "" {
				return p
			}
		}
		return cleaned
	}
	return strings.TrimSuffix(bracketIPv6(u.Hostname())+u.Path, "/")
}

// bracketIPv6 re-brackets a host that contains a colon (an IPv6
// literal, unbracketed by both splitSCPLike and url.URL.Hostname).
// Without the brackets the normalized output would itself be
// scp-shaped ("2001:db8::1/acme/x") and re-split into a different
// host on the next read — normalization must be a fixed point, since
// its output is exactly what the vault write paths persist. The
// bracketed form is: splitSCPLike rejects it (no colon after "]") and
// url.Parse errors on it, so it re-normalizes to itself.
func bracketIPv6(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// LooksLikeSameRepo reports whether the caller's remote and a stored
// scope repo name the same owner/repo path even though they don't
// fully match — the signature of a host form that failed to normalize
// (an unresolvable alias, a different host for the same project).
// Full matches, including via the stored side's legacy ported
// reading, return false.
func LooksLikeSameRepo(remote, stored string) bool {
	if MatchStoredRepoURL(stored, remote) {
		return false
	}
	p1, p2 := repoPathPortion(remote), repoPathPortion(stored)
	return p1 != "" && p1 == p2
}

// repoPathPortion returns the owner/repo part of a normalized repo
// URL (everything after the host), or "" when there is none.
func repoPathPortion(repoURL string) string {
	normalized := NormalizeRepoURL(repoURL)
	if i := strings.IndexByte(normalized, '/'); i > 0 {
		return normalized[i+1:]
	}
	return ""
}

// CleanRepoURL applies the shared pre-normalization cleanup: trim,
// lowercase, and drop a trailing slash and ".git". Every function
// that inspects repo URL structure must clean through here so host
// extraction and normalization can never diverge.
func CleanRepoURL(repoURL string) string {
	cleaned := strings.TrimSpace(strings.ToLower(repoURL))
	cleaned = strings.TrimSuffix(cleaned, "/")
	return strings.TrimSuffix(cleaned, ".git")
}

// NormalizeRepoURLCandidates returns every normalized form repoURL can
// take: the plain normalization and, when the URL is an SSH remote
// whose host is an alias defined in the user's ~/.ssh/config, the
// normalization with the alias replaced by its configured HostName.
// A remote like git@workgit:acme/x (Host workgit / HostName
// github.com) therefore also yields github.com/acme/x. The legacy
// ported reading is deliberately NOT included — only stored vault
// rows can carry that pre-alias shape, so it is expanded exclusively
// on the stored side (MatchStoredRepoURL); reinterpreting a live
// remote's numeric path segment would create false matches.
func NormalizeRepoURLCandidates(repoURL string) []string {
	out := []string{NormalizeRepoURL(repoURL)}
	if resolved := AliasResolvedForm(repoURL); resolved != "" && !slices.Contains(out, resolved) {
		out = append(out, resolved)
	}
	return out
}

// MatchStoredRepoURL reports whether a stored scope row names the same
// repository as the caller's remote. On top of the symmetric
// MatchRepoURLs comparison, the STORED side alone is also read under
// its legacy ported form ("gitea.corp.com:3000/acme/x", written by
// the pre-alias normalizer which kept ports) — asymmetric so a live
// remote with a genuine numeric path segment is never reinterpreted.
func MatchStoredRepoURL(stored, remote string) bool {
	if MatchRepoURLs(stored, remote) {
		return true
	}
	legacy := legacyPortedForm(stored)
	return legacy != "" && slices.Contains(NormalizeRepoURLCandidates(remote), legacy)
}

// CanonicalizeStoredRepoRow returns the modern canonical form of an
// EXISTING stored scope row: a legacy ported row collapses to its
// portless reading, everything else to the plain normalization. For
// migrations (sx vault copy) that re-persist stored rows through the
// user-input write paths — those paths apply NormalizeRepoURL, which
// would turn "gitea.corp.com:3000/acme/x" into the dead slash form
// "gitea.corp.com/3000/acme/x" that no reading reconciles. Never use
// this on live user input: a userless numeric segment in fresh input
// is kept literal by design (see splitSCPLike).
func CanonicalizeStoredRepoRow(stored string) string {
	if legacy := legacyPortedForm(stored); legacy != "" {
		return legacy
	}
	return NormalizeRepoURL(stored)
}

// StoredRepoRowMatches reports whether a stored repo row and a
// user-supplied needle name the same repository, for vault WRITE
// paths: normalized equality plus the stored row's legacy ported
// reading, and deliberately no ~/.ssh/config involvement so the same
// mutation resolves identically on every machine.
func StoredRepoRowMatches(stored, needle string) bool {
	n := NormalizeRepoURL(needle)
	if NormalizeRepoURL(stored) == n {
		return true
	}
	legacy := legacyPortedForm(stored)
	return legacy != "" && legacy == n
}

// AliasResolvedForm returns the normalized form of an SSH remote with
// its host replaced by the HostName from ~/.ssh/config, or "" when
// the URL is not an SSH remote or no alias mapping applies. Callers
// use it both as a match candidate and to tell the user their host is
// remapped locally.
func AliasResolvedForm(repoURL string) string {
	host, path := sshHostAndPath(repoURL)
	if host == "" {
		return ""
	}
	resolved, ok := lookupSSHHostname(host)
	resolved = strings.ToLower(strings.TrimSpace(resolved))
	if !ok || resolved == "" || resolved == host {
		return ""
	}
	return bracketIPv6(resolved) + "/" + strings.TrimLeft(path, "/")
}

// legacyPortedForm returns the port-dropped reading of a userless
// scp-like value, or "" when that reading doesn't apply. Rows
// persisted by the pre-alias normalizer kept the port
// ("gitea.corp.com:3000/acme/x", "ghe.corp:2222/acme/x" from ssh://
// remotes), came from u.Host (never userinfo), and were always
// host:port/owner/repo. Emitting the portless reading as an extra
// match candidate — instead of rewriting inside NormalizeRepoURL —
// keeps those legacy rows matching today's remotes while the write
// path stores exactly what the user passed, so a genuine numeric
// path segment (gitolite year directories, numeric subgroups) is
// never silently dropped from stored data.
func legacyPortedForm(repoURL string) string {
	cleaned := CleanRepoURL(repoURL)
	if strings.Contains(cleaned, "@") {
		return ""
	}
	host, path, ok := splitSCPLike(cleaned)
	if !ok {
		return ""
	}
	slash := strings.IndexByte(path, '/')
	if slash <= 0 || !looksLikePort(path[:slash]) || strings.Count(path[slash+1:], "/") < 1 {
		return ""
	}
	return host + "/" + path[slash+1:]
}

// lookupSSHHostname resolves an SSH host alias to its configured
// HostName. Package-level so tests can stub the ~/.ssh/config lookup.
var lookupSSHHostname = git.SSHConfigHostname

// SetSSHHostLookup replaces the ~/.ssh/config hostname lookup and
// returns a func restoring the previous one. Test seam only: it lets
// suites in other packages stay hermetic instead of parsing the
// developer's real ssh config through the process-wide cache.
func SetSSHHostLookup(fn func(alias string) (string, bool)) (restore func()) {
	orig := lookupSSHHostname
	lookupSSHHostname = fn
	return func() { lookupSSHHostname = orig }
}

// sshHostAndPath extracts the host and repo path from an SSH remote
// (scp-style or ssh://). Returns "" for non-SSH URLs.
func sshHostAndPath(repoURL string) (host, path string) {
	cleaned := CleanRepoURL(repoURL)

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

// splitSCPLike splits an scp-style remote into host and path,
// following git's rule: with no "://", a colon before the first slash
// separates host from path, and userinfo is optional — ssh-config
// alias users typically write "workgit:acme/x" because User lives in
// the config. Single-character hosts are rejected so Windows drive
// paths (c:/repos/x) are never mistaken for remotes.
//
// The split follows git's rule strictly — the path is never rewritten
// here, so a numeric first path segment survives normalization and
// the write path stores exactly what the user passed. (Legacy stored
// rows that kept a port are reconciled at match time by
// legacyPortedForm instead.) The one exception: userless "host:3000"
// with a port-like remainder and no path is a host and port, not a
// repository, and is rejected.
func splitSCPLike(s string) (host, path string, ok bool) {
	if strings.Contains(s, "://") || strings.ContainsAny(s, " \t") {
		return "", "", false
	}
	rest := s
	hadUser := false
	// Optional userinfo: an '@' that precedes the first colon belongs
	// to the user part, matching git's parse.
	if at := strings.IndexByte(rest, '@'); at > 0 {
		if colon := strings.IndexByte(rest, ':'); colon > at {
			rest = rest[at+1:]
			hadUser = true
		}
	}
	// Bracketed IPv6 hosts carry colons inside the brackets
	// ("[2001:db8::1]:acme/x"); the host/path separator is the colon
	// after "]". The brackets are stripped so the host agrees with
	// url.URL.Hostname(), which returns the address unbracketed.
	var colon int
	if strings.HasPrefix(rest, "[") {
		end := strings.IndexByte(rest, ']')
		if end < 0 || end+1 >= len(rest) || rest[end+1] != ':' {
			return "", "", false
		}
		host = rest[1:end]
		colon = end + 1
	} else {
		colon = strings.IndexByte(rest, ':')
		if colon <= 0 {
			return "", "", false
		}
		host = rest[:colon]
	}
	if colon == len(rest)-1 {
		return "", "", false
	}
	if slash := strings.IndexByte(rest, '/'); slash >= 0 && slash < colon {
		return "", "", false
	}
	if len(host) <= 1 {
		return "", "", false
	}
	path = rest[colon+1:]
	if !hadUser && looksLikePort(path) {
		return "", "", false
	}
	return host, path, true
}

// looksLikePort reports whether s is a valid TCP port number as a
// port would actually be written — no leading zeros, 1..65535.
func looksLikePort(s string) bool {
	if s == "" || len(s) > 5 || s[0] == '0' {
		return false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
		n = n*10 + int(r-'0')
	}
	return n > 0 && n <= 65535
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
