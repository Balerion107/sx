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

// binaryName is the CLI's file name, which is deliberately not the app's
// (wails builds "sx-app"), so a sibling lookup cannot pick the GUI by mistake.
func binaryName() string {
	if runtime.GOOS == "windows" {
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
// A CLI shipped with the app deliberately outranks one the user installed
// separately. The app and its bundled CLI come from the same build, and the
// on-disk vault format is versioned — a hook that invoked an older
// Homebrew-installed CLI could read and write vaults the app manages with code
// that predates the current layout. Preferring the bundled binary keeps that
// skew forward-only and makes the app's behavior independent of machine state.
// SX_CLI_PATH overrides this for anyone who wants their own build.
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
	if runtime.GOOS == "windows" {
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
	if runtime.GOOS == "windows" {
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
	parts := append([]string{"sx"}, args...)
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
	if runtime.GOOS == "darwin" {
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
	if runtime.GOOS == "windows" {
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

func shellQuote(s string) string {
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
	// Normalize separators so a Windows-style path in a config is recognized on
	// any host: filepath.Base does not treat "\" as a separator off Windows,
	// and these config files get synced between machines.
	base := strings.ToLower(path.Base(filepath.ToSlash(strings.ReplaceAll(fields[0], `\`, "/"))))
	if !slices.Contains(legacyBinaryNames, base) {
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

// splitCommand splits on whitespace while keeping a quoted argv[0] intact,
// which is the only quoting Command ever emits.
func splitCommand(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	if cmd[0] == '"' {
		if end := strings.Index(cmd[1:], `"`); end >= 0 {
			head := strings.ReplaceAll(cmd[1:end+1], `\"`, `"`)
			rest := strings.Fields(cmd[end+2:])
			return append([]string{head}, rest...)
		}
	}
	return strings.Fields(cmd)
}
