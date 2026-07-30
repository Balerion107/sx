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
// bakes *that* CLI's path in, which is the least surprising outcome — the CLI
// you invoked is the one that gets recorded — but it does mean alternating
// between a terminal install and an app install rewrites the stored path each
// way. Both paths work; only the version they pin differs. SX_CLI_PATH
// overrides all of it.
//
// This only decides what goes into hook and MCP configuration. What happens
// when the user types "sx" in a terminal is their shell's PATH, untouched.
func Resolve() (string, error) {
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
	if p, err := exec.LookPath("sx"); err == nil {
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
		// layout, but keep the original path when it cannot be resolved.
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		dir := filepath.Dir(exe)

		// Already running as the CLI.
		if filepath.Base(exe) == binaryName() {
			out = append(out, exe)
		}

		// macOS: .../sx.app/Contents/MacOS/sx-app -> .../Contents/Resources/sx
		if filepath.Base(dir) == "MacOS" {
			out = append(out, filepath.Join(filepath.Dir(dir), "Resources", binaryName()))
		}

		// Windows and Linux ship the CLI beside the app binary.
		out = append(out, filepath.Join(dir, binaryName()))
	}
	return out
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
	dirs := []string{"/usr/local/bin", "/opt/homebrew/bin"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append([]string{filepath.Join(home, ".local", "bin")}, dirs...)
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
	if base := normalizedBase(cmd); base == "sx-app" || base == "sx-app.exe" {
		return true
	}
	return false
}

// repairVerdict classifies a single argv[0]. owned is false when the binary is
// not one sx writes, in which case verdict carries no meaning.
func repairVerdict(argv0 string) (verdict, owned bool) {
	base := normalizedBase(argv0)

	// The GUI binary is ours and is never usable as the CLI.
	if base == "sx-app" || base == "sx-app.exe" {
		return true, true
	}
	if slices.Contains(legacyBinaryNames, base) {
		// A bare name defers to PATH at run time, so it cannot go stale the way
		// an absolute path can.
		if argv0 == base {
			return false, true
		}
		return !isExecutableFile(argv0), true
	}
	return false, false
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
	if !strings.ContainsAny(s, " \t\"'\\$`") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(s) + `"`
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
func isResolvedCLI(argv0 string) bool {
	if argv0 == "" {
		return false
	}
	resolved, err := Resolve()
	if err != nil {
		return false
	}
	a, b := filepath.Clean(argv0), filepath.Clean(resolved)
	if goos == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
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
		// Find the closing quote, skipping the escaped ones shellQuote emits.
		// strings.Index would stop at the first `\"` and cut argv[0] in half.
		var head strings.Builder
		escaped := false
		for i := 1; i < len(cmd); i++ {
			c := cmd[i]
			if escaped {
				head.WriteByte(c)
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				return append([]string{head.String()}, strings.Fields(cmd[i+1:])...)
			default:
				head.WriteByte(c)
			}
		}
		// Unterminated quote: fall through to whitespace splitting.
	}
	return strings.Fields(cmd)
}
