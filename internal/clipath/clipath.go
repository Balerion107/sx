// Package clipath answers one question in one place: where is the sx CLI?
//
// Client hooks and MCP server entries are configuration that some other
// program executes later, in an environment sx does not control. Writing a
// bare "sx" into them assumes the CLI is on that program's PATH — true when sx
// was installed with Homebrew or install.sh, false when the user only
// installed the desktop app, and unreliable for GUI clients launched from
// Finder or a Dock (which inherit launchd's minimal PATH, not a shell's).
//
// Writing os.Executable() instead is wrong from the desktop app: that is the
// Wails GUI binary, which has no subcommands at all.
//
// So resolution walks from most to least specific: an explicit override, the
// running binary when it is already the CLI, a CLI shipped alongside the app,
// PATH, and finally the directories installers use.
package clipath

import (
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// ErrNotFound means no sx CLI could be located. Callers should degrade rather
// than fail: a hook carrying a bare "sx" still works for anyone whose PATH has
// it, which is strictly better than refusing to install the hook.
var ErrNotFound = errors.New("clipath: no sx CLI binary found")

// EnvOverride names the escape hatch. Set it when the CLI lives somewhere the
// search below cannot reasonably guess.
const EnvOverride = "SX_CLI_PATH"

// goos is a seam: the Windows quoting and bundle-layout rules need to be
// exercised from a non-Windows host, and nothing here depends on the real OS
// beyond its name.
var goos = runtime.GOOS

// binaryName is the CLI's file name, which is deliberately not the app's
// (wails builds "sx-app"), so a sibling lookup cannot pick the GUI by mistake.
func binaryName() string {
	if goos == "windows" {
		return "sx.exe"
	}
	return "sx"
}

// legacyBinaryNames are argv[0] values that earlier versions wrote into hook
// configs. Recognized so existing hooks are upgraded in place instead of
// duplicated.
var legacyBinaryNames = []string{"sx", "sx.exe", "skills", "skills.exe"}

// Resolve returns an absolute path to an sx CLI binary that can run
// subcommands.
//
// Precedence, and what it does and does not guarantee: the binary already
// running wins first, then a CLI shipped with the app, then PATH. So an install
// initiated *by the app* always names the app's own bundled CLI — which is the
// property that matters, because the on-disk vault format is versioned and a
// hook invoking an older separately-installed CLI could read and write vaults
// the app manages with code predating the current layout. App-initiated skew
// stays forward-only.
//
// It is not a global guarantee. Running your own `sx install` from a terminal
// bakes *that* CLI's path in — more precisely, the most stable known spelling
// of the binary you invoked (a versioned Homebrew Cellar target is recorded
// as the stable bin/ symlink that points at it, never the Cellar path an
// upgrade deletes). Alternating between a terminal install and an app install
// still rewrites the stored path each way. Both paths work; only the version
// they pin differs. SX_CLI_PATH overrides all of it.
//
// This only decides what goes into hook and MCP configuration. What happens
// when the user types "sx" in a terminal is their shell's PATH, untouched.
//
// The result is memoized for the process lifetime: Managed and friends call
// this once per config entry they inspect, and the probe work (PATH lookups,
// stats, symlink resolution) is not free.
func Resolve() (string, error) {
	// The override stays outside the memoized path: it is one env read,
	// and callers may set or change it between calls.
	if override := strings.TrimSpace(os.Getenv(EnvOverride)); override != "" && isExecutableFile(override) {
		if abs, err := filepath.Abs(override); err == nil {
			return abs, nil
		}
		return override, nil
	}
	resolveCache.Lock()
	defer resolveCache.Unlock()
	if !resolveCache.done {
		resolveCache.path, resolveCache.err = resolve()
		resolveCache.done = true
	}
	return resolveCache.path, resolveCache.err
}

var resolveCache struct {
	sync.Mutex
	done bool
	path string
	err  error
}

// resetResolveCache clears the memoized Resolve result. Tests re-stub the
// executable/installDirs seams and change env vars, so the stub helpers call
// this on both stub and cleanup.
func resetResolveCache() {
	resolveCache.Lock()
	defer resolveCache.Unlock()
	resolveCache.done = false
}

func resolve() (string, error) {
	for _, candidate := range candidates() {
		if isExecutableFile(candidate) {
			if abs, err := filepath.Abs(candidate); err == nil {
				return abs, nil
			}
			return candidate, nil
		}
	}
	// PATH before the guessed install directories: PATH reflects what the user
	// actually set up, while installDirs is a last-ditch guess for the
	// GUI-launched case where PATH is launchd's minimal set.
	if p, err := exec.LookPath(binaryName()); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs, nil
		}
		return p, nil
	}
	for _, dir := range installDirs() {
		candidate := filepath.Join(dir, binaryName())
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	return "", ErrNotFound
}

// candidates lists paths to probe, in priority order, ahead of PATH.
func candidates() []string {
	var out []string

	if override := strings.TrimSpace(os.Getenv(EnvOverride)); override != "" {
		out = append(out, override)
	}

	if exe, err := executable(); err == nil {
		// Resolve symlinks so a shim in ~/.local/bin does not hide the real
		// layout, but keep the original path when it cannot be resolved. The
		// resolved path anchors layout detection below; it is written into
		// configs only when no stabler spelling of the same binary exists.
		resolved := exe
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			resolved = r
		}

		// Already running as the CLI. Prefer a stable spelling over the
		// resolved target: package managers that version their install
		// directory expose a stable symlink (Homebrew's /opt/homebrew/bin/sx
		// points into Cellar/sx/<version>/), and writing the versioned target
		// into a hook leaves the hook pointing at a directory the next
		// upgrade deletes. On macOS the invocation path preserves that
		// symlink, but Linux (/proc/self/exe) and Windows hand back the
		// already-resolved target no matter how the process was invoked — so
		// when the known path is not itself a stable spelling, probe the
		// stable locations for an alias of this same binary. An
		// already-stable invocation path is kept as-is: a user shim earlier
		// on PATH must not displace the spelling that was actually invoked.
		// Comparisons use normalizedBase: the CLI may be invoked as "SX" on a
		// case-insensitive filesystem.
		if normalizedBase(exe) == binaryName() {
			if !isStableSpelling(exe) {
				if alias := stableAlias(exe); alias != "" {
					out = append(out, alias)
				}
			}
			out = append(out, exe)
		} else if normalizedBase(resolved) == binaryName() {
			// Invoked through a differently-named symlink ("skills" -> sx):
			// only the resolved path is recognizably the CLI.
			if !isStableSpelling(resolved) {
				if alias := stableAlias(resolved); alias != "" {
					out = append(out, alias)
				}
			}
			out = append(out, resolved)
		}

		dir := filepath.Dir(resolved)

		// macOS: .../sx.app/Contents/MacOS/sx-app -> .../Contents/Resources/sx
		if filepath.Base(dir) == "MacOS" {
			out = append(out, filepath.Join(filepath.Dir(dir), "Resources", binaryName()))
		}

		// Windows and Linux ship the CLI beside the app binary.
		out = append(out, filepath.Join(dir, binaryName()))
	}
	return out
}

// stableAlias looks for a stable path that names the same binary as
// target, and returns it, or "" when none exists. It never returns
// target's own spelling, so callers can append it as a strictly-preferred
// candidate.
//
// Probe order matters. A Homebrew Cellar target names its own stable
// prefix, so that alias is derived directly from the path and cannot
// depend on PATH — GUI-launched clients run hooks under launchd's
// minimal PATH, where a LookPath-only probe would fail and the hook
// would be rewritten back to the fragile versioned path. PATH and the
// installer directories are consulted after.
func stableAlias(target string) string {
	canon, err := filepath.EvalSymlinks(target)
	if err != nil {
		return ""
	}

	var probes []string
	if alias := brewCellarAlias(canon); alias != "" {
		probes = append(probes, alias)
	}
	if p, err := exec.LookPath(binaryName()); err == nil {
		probes = append(probes, p)
	}
	for _, dir := range installDirs() {
		probes = append(probes, filepath.Join(dir, binaryName()))
	}

	for _, p := range probes {
		if !isExecutableFile(p) {
			continue
		}
		// Identity, not spelling: stat follows symlinks, and os.SameFile
		// sidesteps case differences on case-insensitive filesystems.
		if !sameFile(p, canon) {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if samePath(p, target) {
			// The probe IS the target's spelling — nothing gained.
			continue
		}
		return p
	}
	return ""
}

// brewCellarAlias derives the stable bin path a Homebrew-style Cellar
// target implies: /opt/homebrew/Cellar/sx/2.3.1/bin/sx names
// /opt/homebrew/bin/sx. Works for any prefix — /usr/local,
// /home/linuxbrew/.linuxbrew, a custom --prefix — with one rule and no
// PATH dependency. Returns "" for non-Cellar paths.
func brewCellarAlias(canon string) string {
	idx := strings.Index(filepath.ToSlash(canon), "/Cellar/")
	if idx < 0 {
		return ""
	}
	return filepath.Join(canon[:idx], "bin", binaryName())
}

// isStableSpelling reports whether path already lives somewhere durable:
// a PATH directory or an installer directory, and not inside a versioned
// Cellar tree. Such a path is kept as-is rather than traded for whatever
// alias happens to probe first.
func isStableSpelling(path string) bool {
	if strings.Contains(filepath.ToSlash(path), "/Cellar/") {
		return false
	}
	dir := filepath.Dir(path)
	for _, d := range installDirs() {
		if d != "" && samePath(dir, d) {
			return true
		}
	}
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d != "" && samePath(dir, d) {
			return true
		}
	}
	return false
}

// samePath reports whether two paths are the same spelling, honoring
// Windows case-insensitivity. It does not resolve symlinks; use
// sameFile to compare identity.
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if goos == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// sameFile reports whether two paths name the same file on disk. Stat
// follows symlinks, so this compares final targets and is immune to
// case and separator differences string comparison would trip on.
func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// installDirs are where sx's own installers put the CLI. A GUI-launched client
// often cannot see these on PATH, which is the whole reason hooks need an
// absolute path. A var so tests can isolate from the host machine.
var installDirs = func() []string {
	if goos == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return []string{filepath.Join(local, "sx", "bin"), filepath.Join(local, "Programs", "sx")}
		}
		return nil
	}
	dirs := []string{"/usr/local/bin", "/opt/homebrew/bin", "/home/linuxbrew/.linuxbrew/bin"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append([]string{filepath.Join(home, ".local", "bin")}, dirs...)
		dirs = append(dirs, filepath.Join(home, ".linuxbrew", "bin"))
	}
	return dirs
}

// executable is a seam for tests; production always uses os.Executable.
var executable = os.Executable

func isExecutableFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if goos == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

// Command builds the string form a hook config needs: an absolute CLI path
// followed by args, shell-quoted when the path contains spaces (an app bundle
// the user renamed, a home directory with a space in it).
//
// When no CLI can be found it returns the bare-"sx" form and ErrNotFound, so a
// caller can log the degradation and still write a hook that works for anyone
// with sx on PATH.
func Command(args ...string) (string, error) {
	parts := append([]string{binaryName()}, args...)
	fallback := strings.Join(parts, " ")

	path, err := Resolve()
	if err != nil {
		return fallback, err
	}
	parts[0] = shellQuote(path)
	return strings.Join(parts, " "), nil
}

// AppManaged reports whether the running binary is the CLI copy that ships
// inside the desktop app.
//
// Such a copy must not self-update. The CLI's updater overwrites its own
// executable in place, and that executable lives inside a signed, notarized
// .app — rewriting a file in there invalidates the bundle signature, which on
// macOS can stop the app from launching at all. /Applications is typically
// writable by admin users, so this would tend to succeed at breaking things.
//
// It would also be pointless: the app's updater swaps the whole bundle, so a
// CLI that updated itself gets replaced on the app's next update anyway. And it
// would reintroduce exactly the app/CLI version skew that bundling exists to
// prevent. The app updates this copy; the CLI stays out of it.
func AppManaged() bool {
	exe, err := executable()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	// macOS: anywhere inside Foo.app/Contents/.
	if goos == "darwin" {
		for dir := filepath.Dir(exe); ; {
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			if filepath.Base(dir) == "Contents" && strings.HasSuffix(filepath.Base(parent), ".app") {
				return true
			}
			dir = parent
		}
		return false
	}

	// Windows and Linux: the CLI sits beside the app binary.
	appName := "sx-app"
	if goos == "windows" {
		appName = "sx-app.exe"
	}
	return isExecutableFile(filepath.Join(filepath.Dir(exe), appName))
}

// CommandOrBare is Command with the not-found error folded away, for the many
// call sites that cannot do anything useful about a missing CLI. The bare "sx"
// form it falls back to is exactly what these configs contained before, so the
// worst case is the old behavior rather than a failed install.
func CommandOrBare(args ...string) string {
	cmd, _ := Command(args...)
	return cmd
}

// NeedsRepair reports whether an existing config command was written by sx but
// can no longer work, and should therefore be overwritten.
//
// Two cases qualify. An entry naming the desktop app's GUI binary was written
// by a version that used os.Executable() from the app — it can never serve as
// the CLI, since that binary has no subcommands. An entry naming an sx CLI at a
// path that no longer exists was valid when written and went stale, typically
// because the app moved or a separately installed CLI was removed.
//
// Anything else returns false. A command sx did not write is the user's, and a
// hand-written "skills" MCP entry pointing at their own server must survive an
// sx install untouched.
func NeedsRepair(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	// argv[0] first. Consulting the whole string first was wrong in a way that
	// destroys user config: normalizedBase is the last path segment, so any
	// multi-token command ending in an sx-like segment ("docker run
	// ghcr.io/acme/skills", "uv run --directory /opt/tools/sx") looked owned, and
	// since the whole string is not a file the verdict came back "repair" — so
	// Cursor and Kiro would overwrite a hand-written server with their own entry.
	fields := splitCommand(cmd)
	if len(fields) > 0 {
		if verdict, owned := repairVerdict(fields[0]); owned {
			return verdict
		}
	}

	// Only then the whole value, and only when it is unambiguously ours: an MCP
	// "command" is a bare executable path, so an unquoted Windows path with a
	// space ("C:\\Program Files\\sx\\sx-app.exe") splits into a meaningless
	// argv[0]. Requiring either the GUI binary name or a file that actually
	// exists keeps this branch from reaching the destructive verdict by accident.
	// Requiring an absolute path keeps a multi-token command out of this branch:
	// "docker run acme/sx-app" ends in the GUI binary's name but is somebody
	// else's server, and treating it as ours would overwrite their config. An
	// unquoted "C:\Program Files\sx\sx-app.exe" — the case this branch exists
	// for — is absolute and still matches.
	if isAbsolutePath(cmd) {
		if base := normalizedBase(cmd); base == "sx-app" || base == "sx-app.exe" {
			return true
		}
	}
	return false
}

// isAbsolutePath recognizes POSIX and Windows absolute paths regardless of host,
// since filepath.IsAbs only understands the running platform's rules and these
// values come out of config files that get synced between machines.
func isAbsolutePath(p string) bool {
	if p == "" {
		return false
	}
	if p[0] == '/' || strings.HasPrefix(p, `\\`) {
		return true
	}
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		c := p[0] | 0x20
		return c >= 'a' && c <= 'z'
	}
	return false
}

// repairVerdict classifies a single argv[0]. owned is false when the binary is
// not one sx writes, in which case verdict carries no meaning.
func repairVerdict(argv0 string) (verdict, owned bool) {
	base := normalizedBase(argv0)

	// sx only ever writes one of two forms: an absolute path, or the bare binary
	// name. Anything relative was written by someone else even when its last
	// segment looks like ours ("./sx-app", "acme/sx"), and claiming it would
	// overwrite their config.
	bare := argv0 == base
	if !bare && !isAbsolutePath(argv0) {
		return false, false
	}

	// The GUI binary is ours and is never usable as the CLI.
	if base == "sx-app" || base == "sx-app.exe" {
		return true, true
	}
	if slices.Contains(legacyBinaryNames, base) {
		// A bare name defers to PATH at run time, so it cannot go stale the way
		// an absolute path can.
		if bare {
			return false, true
		}
		return !isExecutableFile(argv0), true
	}
	return false, false
}

// ShouldRewrite reports whether an existing config command should be replaced
// with the current one.
//
// That is NeedsRepair plus two upgrade cases it deliberately excludes. A bare
// "sx" was written when no CLI could be found; it still works wherever PATH
// has one, so it is not broken — but once a CLI is resolvable, an absolute
// path is strictly better, and without this a degraded first install stays
// degraded forever. And an absolute sx path that resolves to the same binary
// as the current resolution but spells it differently — a versioned Homebrew
// Cellar path recorded before the stable-alias preference existed — still
// works today but dies on the next upgrade, so it is upgraded to the current
// spelling. Distinct binaries (a terminal-installed CLI vs the app's bundled
// one) are left alone to avoid rewrite thrash.
func ShouldRewrite(cmd string) bool {
	if NeedsRepair(cmd) {
		return true
	}
	fields := splitCommand(cmd)
	if len(fields) == 0 {
		return false
	}
	argv0 := fields[0]
	if !slices.Contains(legacyBinaryNames, normalizedBase(argv0)) {
		return false
	}
	if argv0 == normalizedBase(argv0) {
		// Bare name: worth upgrading only if there is something better to point at.
		_, err := Resolve()
		return err == nil
	}
	if !isAbsolutePath(argv0) {
		return false
	}
	resolved, err := Resolve()
	if err != nil || samePath(argv0, resolved) {
		return false
	}
	// One-directional on purpose: only a spelling that is itself fragile
	// and has a stabler alias gets upgraded. Without this the branch
	// would adopt whatever Resolve currently says in either direction —
	// downgrading a stable entry to a versioned Cellar path when a
	// GUI-launched client's minimal PATH skews resolution, or flapping
	// between two equally stable aliases with PATH order.
	if isStableSpelling(argv0) || stableAlias(argv0) == "" {
		return false
	}
	return sameFile(argv0, resolved)
}

// MCPEntryNeedsRewrite reports whether an existing MCP server entry should be
// replaced with a freshly resolved one.
//
// entry is the decoded JSON object for a single server: a "command" plus "args".
// A broken command is replaced whatever its arguments, since it cannot work as
// it stands. The bare-"sx" upgrade is held to a stricter test — that entry does
// work, only its path is being improved — so it must leave a hand-written
// invocation of the same binary alone, which means its arguments have to be the
// ones sx itself writes.
func MCPEntryNeedsRewrite(entry any) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	cmd, ok := m["command"].(string)
	if !ok {
		return false
	}
	if NeedsRepair(cmd) {
		return true
	}
	return ShouldRewrite(cmd) && mcpArgsAreOurs(m["args"])
}

// mcpArgsAreOurs reports whether an entry's args are exactly what sx writes.
func mcpArgsAreOurs(raw any) bool {
	args, ok := raw.([]any)
	if !ok || len(args) != 1 {
		return false
	}
	s, ok := args[0].(string)
	return ok && s == "serve"
}

// ResolveOrBare returns the resolved CLI path, or the bare name "sx" when none
// can be found.
//
// For argv-style fields — an MCP server's "command", which the client execs
// directly rather than through a shell — so the result is deliberately not
// shell-quoted, and the bare fallback defers to the client's own PATH lookup.
// Failing to write an MCP entry at all would be worse than writing one that
// depends on PATH.
func ResolveOrBare() string {
	if path, err := Resolve(); err == nil {
		return path
	}
	return binaryName()
}

// shellEscaped is the set of characters shellQuote escapes on POSIX and
// splitCommand unescapes. One definition on purpose: the two drifted once,
// leaving `$` and a backtick to survive a round trip as literal backslashes.
const shellEscaped = `\\"$` + "`"

// shellQuote quotes a path for embedding in a shell command string, and only
// when it has to.
//
// On Windows a backslash is a path separator, not an escape: quoting every path
// because it contains one made hook bodies unnecessarily quoted, and doubling
// the separators is meaningless there. Only a genuine space forces quoting, and
// nothing inside is escaped — cmd.exe and PowerShell both treat a
// double-quoted run as literal.
func shellQuote(s string) string {
	if goos == "windows" {
		if !strings.ContainsAny(s, " \t") {
			return s
		}
		return `"` + s + `"`
	}
	if !strings.ContainsAny(s, " \t'"+shellEscaped) {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := range len(s) {
		if strings.IndexByte(shellEscaped, s[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('"')
	return b.String()
}

// Managed reports whether cmd is an sx-written invocation of one of the given
// subcommands. It accepts both the legacy bare-"sx" form and the absolute-path
// form Command produces, so hook detection, upgrade, and removal keep working
// across the change.
//
// subcommands are matched against the argument list, so "install" matches
// "sx install --hook-mode --client=cline" and "report-usage" matches
// "/abs/sx report-usage --client=github-copilot".
func Managed(cmd string, subcommands ...string) bool {
	fields := splitCommand(cmd)
	if len(fields) == 0 {
		return false
	}
	base := normalizedBase(fields[0])
	if !slices.Contains(legacyBinaryNames, base) && !isResolvedCLI(fields[0]) {
		return false
	}
	rest := strings.Join(fields[1:], " ")
	for _, sub := range subcommands {
		sub = strings.TrimSpace(sub)
		if sub == "" {
			continue
		}
		// Accept a bare subcommand ("install") or a fuller prefix
		// ("install --hook-mode").
		if rest == sub || strings.HasPrefix(rest, sub+" ") {
			return true
		}
	}
	return false
}

// isResolvedCLI reports whether argv0 is the CLI this process would resolve to
// right now, regardless of what it is called. SX_CLI_PATH can point at a binary
// with any name, and without this a hook sx wrote from such an override would
// not be recognized as its own — so an upgrade would append a duplicate.
//
// Two limits worth knowing. It only helps while that same override is in effect:
// a hook written from an override that is no longer set names a binary with no
// recognizable name, and nothing recorded that it was ours. And it is skipped
// for anything without a path separator, both because a bare unknown name cannot
// be a resolved absolute path and to keep Managed from resolving on every entry
// it inspects.
func isResolvedCLI(argv0 string) bool {
	if argv0 == "" || !strings.ContainsAny(argv0, `/\`) {
		return false
	}
	resolved, err := Resolve()
	if err != nil {
		return false
	}
	return samePath(argv0, resolved)
}

// normalizedBase returns argv[0]'s lowercased file name with separators
// normalized, so a Windows-style path in a config is recognized on any host:
// filepath.Base does not treat "\" as a separator off Windows, and these config
// files get synced between machines.
func normalizedBase(argv0 string) string {
	return strings.ToLower(path.Base(filepath.ToSlash(strings.ReplaceAll(argv0, `\`, "/"))))
}

// ManagedArgv is Managed for configs that store a command as an argument array
// rather than a shell string — Codex's config.toml "notify", for instance.
//
// Joining such an array with spaces and handing it to Managed is wrong: a CLI
// path containing a space would be re-split in the wrong place, and the entry
// would go unrecognized on uninstall.
func ManagedArgv(argv []string, subcommands ...string) bool {
	if len(argv) == 0 {
		return false
	}
	base := normalizedBase(argv[0])
	if !slices.Contains(legacyBinaryNames, base) && !isResolvedCLI(argv[0]) {
		return false
	}
	rest := strings.Join(argv[1:], " ")
	for _, sub := range subcommands {
		if sub = strings.TrimSpace(sub); sub == "" {
			continue
		}
		if rest == sub || strings.HasPrefix(rest, sub+" ") {
			return true
		}
	}
	return false
}

// splitCommand splits on whitespace while keeping a quoted argv[0] intact,
// which is the only quoting Command ever emits.
func splitCommand(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	if cmd[0] == '"' {
		// A backslash is an escape only when it precedes one of the characters
		// shellQuote escapes — the same set, kept in sync via shellEscaped. In a
		// Windows path it precedes a path segment, so it stays literal, which is
		// why this is decided per character rather than by the host OS: these
		// configs get synced between machines, and keying on runtime.GOOS mangled
		// a Windows path read on POSIX and left a POSIX escape intact on Windows.
		var head strings.Builder
		for i := 1; i < len(cmd); i++ {
			c := cmd[i]
			if c == '\\' && i+1 < len(cmd) && strings.IndexByte(shellEscaped, cmd[i+1]) >= 0 {
				head.WriteByte(cmd[i+1])
				i++
				continue
			}
			if c == '"' {
				return append([]string{head.String()}, strings.Fields(cmd[i+1:])...)
			}
			head.WriteByte(c)
		}
		// Unterminated quote: fall through to whitespace splitting.
	}
	return strings.Fields(cmd)
}
