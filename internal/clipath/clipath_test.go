package clipath

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeCLI creates an executable file so the resolver's stat/permission
// check treats it as a real binary.
func writeFakeCLI(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// tempRoot is t.TempDir with symlinks resolved: on macOS it lives under
// /var -> /private/var, and comparing unresolved against resolved paths is a
// harness artifact, not a behavior difference.
func tempRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return dir
}

// stubExecutable makes the package believe it is running as the given binary.
func stubExecutable(t *testing.T, path string) {
	t.Helper()
	prev := executable
	executable = func() (string, error) { return path, nil }
	t.Cleanup(func() { executable = prev })
}

// stubInstallDirs isolates the search from whatever the host machine has in
// ~/.local/bin or /opt/homebrew/bin.
func stubInstallDirs(t *testing.T, dirs []string) {
	t.Helper()
	prev := installDirs
	installDirs = func() []string { return dirs }
	t.Cleanup(func() { installDirs = prev })
}

func TestResolvePrefersEnvOverride(t *testing.T) {
	dir := tempRoot(t)
	want := writeFakeCLI(t, dir, binaryName())
	// A second, lower-priority candidate that must lose.
	other := tempRoot(t)
	writeFakeCLI(t, other, binaryName())
	t.Setenv(EnvOverride, want)
	stubExecutable(t, filepath.Join(other, "sx-app"))
	stubInstallDirs(t, nil)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Fatalf("Resolve = %q, want the override %q", got, want)
	}
}

// The desktop app is the case this package exists for: the running binary is
// the GUI, and the CLI sits in the bundle's Resources directory.
func TestResolveFindsCLIInMacOSAppBundle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("app bundle layout is macOS-only")
	}
	t.Setenv(EnvOverride, "")
	bundle := filepath.Join(tempRoot(t), "sx.app", "Contents")
	stubExecutable(t, filepath.Join(bundle, "MacOS", "sx-app"))
	if err := os.MkdirAll(filepath.Join(bundle, "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := writeFakeCLI(t, filepath.Join(bundle, "Resources"), binaryName())
	stubInstallDirs(t, nil)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Fatalf("Resolve = %q, want bundled CLI %q", got, want)
	}
}

// Windows and Linux ship the CLI beside the app binary rather than in a bundle.
func TestResolveFindsSiblingCLI(t *testing.T) {
	t.Setenv(EnvOverride, "")
	dir := tempRoot(t)
	stubExecutable(t, filepath.Join(dir, "sx-app"))
	want := writeFakeCLI(t, dir, binaryName())
	stubInstallDirs(t, nil)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Fatalf("Resolve = %q, want sibling %q", got, want)
	}
}

// Running as the CLI itself must resolve to itself, not go hunting on PATH.
func TestResolveUsesRunningCLI(t *testing.T) {
	t.Setenv(EnvOverride, "")
	dir := tempRoot(t)
	want := writeFakeCLI(t, dir, binaryName())
	stubExecutable(t, want)
	stubInstallDirs(t, nil)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Fatalf("Resolve = %q, want running binary %q", got, want)
	}
}

func TestCommandQuotesPathsWithSpaces(t *testing.T) {
	dir := filepath.Join(tempRoot(t), "Application Support")
	path := writeFakeCLI(t, dir, binaryName())
	t.Setenv(EnvOverride, path)
	stubExecutable(t, filepath.Join(dir, "sx-app"))

	cmd, err := Command("install", "--hook-mode", "--client=cline")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if !strings.HasPrefix(cmd, `"`) {
		t.Fatalf("path with a space must be quoted, got %s", cmd)
	}
	if !strings.HasSuffix(cmd, " install --hook-mode --client=cline") {
		t.Fatalf("args lost: %s", cmd)
	}
	// The quoted form must still be recognized as ours.
	if !Managed(cmd, "install") {
		t.Fatalf("Managed did not recognize the quoted command %s", cmd)
	}
}

// Resolution failure must degrade to the legacy bare-"sx" form, not error out
// of installing a hook entirely.
func TestCommandFallsBackToBareSx(t *testing.T) {
	t.Setenv(EnvOverride, filepath.Join(tempRoot(t), "definitely-absent"))
	t.Setenv("PATH", tempRoot(t))
	stubExecutable(t, filepath.Join(tempRoot(t), "sx-app"))
	stubInstallDirs(t, nil)

	cmd, err := Command("install", "--hook-mode")
	if err == nil {
		t.Fatal("expected ErrNotFound when no CLI exists")
	}
	if cmd != "sx install --hook-mode" {
		t.Fatalf("fallback = %q, want the bare form", cmd)
	}
}

func TestManaged(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		subs []string
		want bool
	}{
		{"legacy bare sx", "sx install --hook-mode --client=cline", []string{"install"}, true},
		{"legacy skills binary", "skills install --hook-mode", []string{"install"}, true},
		{"absolute path", "/opt/homebrew/bin/sx install --hook-mode", []string{"install"}, true},
		{"app bundle path", "/Applications/sx.app/Contents/Resources/sx report-usage --client=x", []string{"report-usage"}, true},
		{"quoted path with space", `"/My Apps/sx.app/Contents/Resources/sx" install --hook-mode`, []string{"install"}, true},
		{"windows exe", `C:\Users\a\AppData\Local\sx\bin\sx.exe install`, []string{"install"}, true},
		{"fuller prefix", "sx install --hook-mode --client=cline", []string{"install --hook-mode"}, true},
		{"wrong subcommand", "sx audit", []string{"install"}, false},
		{"someone else's hook", "npm run lint", []string{"install"}, false},
		{"binary named sx-app is not the CLI", "/Applications/sx.app/Contents/MacOS/sx-app install", []string{"install"}, false},
		{"substring trap", "/usr/bin/notsx install", []string{"install"}, false},
		{"empty", "", []string{"install"}, false},
		{"no subcommands given", "sx install", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Managed(tc.cmd, tc.subs...); got != tc.want {
				t.Fatalf("Managed(%q, %v) = %v, want %v", tc.cmd, tc.subs, got, tc.want)
			}
		})
	}
}

// The bundled CLI must never self-update: its executable lives inside a signed
// .app, and rewriting a file in there invalidates the bundle signature.
func TestAppManagedInsideMacOSBundle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("bundle detection is darwin-specific")
	}
	root := tempRoot(t)
	// The CLI ships in Contents/Resources.
	stubExecutable(t, filepath.Join(root, "sx.app", "Contents", "Resources", "sx"))
	if !AppManaged() {
		t.Fatal("CLI inside sx.app/Contents/Resources must be app-managed")
	}
	// Anywhere under Contents counts — helper layouts vary.
	stubExecutable(t, filepath.Join(root, "sx.app", "Contents", "MacOS", "sx"))
	if !AppManaged() {
		t.Fatal("CLI inside sx.app/Contents/MacOS must be app-managed")
	}
}

func TestAppManagedFalseForStandaloneCLI(t *testing.T) {
	root := tempRoot(t)
	for _, p := range []string{
		filepath.Join(root, "bin", "sx"),
		filepath.Join(root, ".local", "bin", "sx"),
		filepath.Join(root, "opt", "homebrew", "bin", "sx"),
		// A directory merely named like an app, without the Contents layout.
		filepath.Join(root, "sx.app", "sx"),
	} {
		stubExecutable(t, p)
		if AppManaged() {
			t.Fatalf("%s is a standalone CLI and must self-update normally", p)
		}
	}
}

// On Windows and Linux the CLI ships beside the app binary rather than in a
// bundle, so the app binary's presence is the signal.
func TestAppManagedSiblingAppBinary(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin uses the bundle layout")
	}
	dir := tempRoot(t)
	stubExecutable(t, filepath.Join(dir, binaryName()))
	if AppManaged() {
		t.Fatal("no app binary alongside: must not be app-managed")
	}
	appName := "sx-app"
	if runtime.GOOS == "windows" {
		appName = "sx-app.exe"
	}
	writeFakeCLI(t, dir, appName)
	if !AppManaged() {
		t.Fatal("app binary alongside the CLI must mark it app-managed")
	}
}

// stubGOOS exercises platform-specific rules from any host.
func stubGOOS(t *testing.T, name string) {
	t.Helper()
	prev := goos
	goos = name
	t.Cleanup(func() { goos = prev })
}

// A Windows path is full of backslashes; quoting on that alone produced hook
// bodies that PowerShell parses as string expressions rather than commands.
func TestShellQuoteWindows(t *testing.T) {
	stubGOOS(t, "windows")
	cases := []struct{ in, want string }{
		{`C:\Users\bob\AppData\Local\sx\bin\sx.exe`, `C:\Users\bob\AppData\Local\sx\bin\sx.exe`},
		{`C:\Program Files\sx\sx.exe`, `"C:\Program Files\sx\sx.exe"`},
	}
	for _, tc := range cases {
		if got := shellQuote(tc.in); got != tc.want {
			t.Fatalf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestShellQuotePosix(t *testing.T) {
	stubGOOS(t, "darwin")
	if got := shellQuote("/opt/homebrew/bin/sx"); got != "/opt/homebrew/bin/sx" {
		t.Fatalf("plain path should not be quoted, got %q", got)
	}
	got := shellQuote("/My Apps/sx.app/Contents/Resources/sx")
	if got != `"/My Apps/sx.app/Contents/Resources/sx"` {
		t.Fatalf("path with space = %q", got)
	}
}

// Windows paths must survive Command intact and stay recognizable.
func TestCommandWindowsPathWithSpace(t *testing.T) {
	stubGOOS(t, "windows")
	dir := filepath.Join(tempRoot(t), "Program Files")
	path := writeFakeCLI(t, dir, "sx.exe")
	t.Setenv(EnvOverride, path)
	stubExecutable(t, filepath.Join(dir, "sx-app.exe"))
	stubInstallDirs(t, nil)

	cmd, err := Command("install", "--hook-mode", "--client=cline")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if !strings.HasPrefix(cmd, `"`) {
		t.Fatalf("path with a space must be quoted: %s", cmd)
	}
	if strings.Contains(cmd, `\\`) {
		t.Fatalf("Windows separators must not be doubled: %s", cmd)
	}
	if !Managed(cmd, "install") {
		t.Fatalf("quoted Windows command not recognized: %s", cmd)
	}
}

func TestNeedsRepair(t *testing.T) {
	stubGOOS(t, "darwin")
	existing := writeFakeCLI(t, tempRoot(t), "sx")

	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		// Written by a version that used os.Executable() from the app.
		{"gui binary", "/Applications/sx.app/Contents/MacOS/sx-app", true},
		{"gui binary windows", `C:\Program Files\sx\sx-app.exe`, true},
		// Ours, but the file is gone — the app moved or a CLI was uninstalled.
		{"absolute path that no longer exists", "/nope/sx", true},
		// Ours and still working.
		{"absolute path that exists", existing, false},
		// A bare name defers to PATH at run time and cannot go stale.
		{"bare sx", "sx", false},
		{"bare sx with args", "sx serve", false},
		// Not ours: a hand-written entry must survive untouched.
		{"third-party server", "npx -y @acme/mcp-server", false},
		{"python server", "/usr/bin/python3 -m my_server", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsRepair(tc.cmd); got != tc.want {
				t.Fatalf("NeedsRepair(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

// A hand-written command whose last path segment merely looks like ours must
// never be classified as repairable: Cursor and Kiro overwrite on a true
// verdict, which would destroy the user's own MCP server entry.
func TestNeedsRepairLeavesLookalikeUserCommandsAlone(t *testing.T) {
	stubGOOS(t, "darwin")
	for _, cmd := range []string{
		"docker run ghcr.io/acme/skills",
		"uv run --directory /opt/tools/sx",
		"/usr/bin/python3 -m servers/sx",
		"node /srv/tools/sx.exe",
		"npx -y @acme/mcp-server",
	} {
		if NeedsRepair(cmd) {
			t.Fatalf("NeedsRepair(%q) = true; a hand-written entry must be preserved", cmd)
		}
	}
}

// The whole-value branch still has to catch an unquoted Windows GUI path with a
// space, which naive splitting reads as argv[0] "C:\Program".
func TestNeedsRepairUnquotedWindowsGUIPath(t *testing.T) {
	stubGOOS(t, "windows")
	if !NeedsRepair(`C:\Program Files\sx\sx-app.exe`) {
		t.Fatal("GUI binary at a spaced Windows path must be repaired")
	}
}

// The bare fallback must name the platform's binary, since it is resolved by
// the consumer's PATH.
func TestResolveOrBareUsesPlatformBinaryName(t *testing.T) {
	t.Setenv(EnvOverride, filepath.Join(tempRoot(t), "absent"))
	t.Setenv("PATH", tempRoot(t))
	stubExecutable(t, filepath.Join(tempRoot(t), "sx-app"))
	stubInstallDirs(t, nil)

	stubGOOS(t, "windows")
	if got := ResolveOrBare(); got != "sx.exe" {
		t.Fatalf("windows fallback = %q, want sx.exe", got)
	}
	stubGOOS(t, "darwin")
	if got := ResolveOrBare(); got != "sx" {
		t.Fatalf("posix fallback = %q, want sx", got)
	}
}

// SX_CLI_PATH can name the binary anything; a hook written from it must still be
// recognized as ours, or an upgrade appends a duplicate.
func TestManagedRecognizesCustomNamedOverride(t *testing.T) {
	stubGOOS(t, "darwin")
	dir := tempRoot(t)
	custom := writeFakeCLI(t, dir, "my-sx-build")
	t.Setenv(EnvOverride, custom)
	stubExecutable(t, filepath.Join(dir, "sx-app"))
	stubInstallDirs(t, nil)

	cmd := custom + " install --hook-mode --client=cline"
	if !Managed(cmd, "install") {
		t.Fatalf("hook written from SX_CLI_PATH not recognized: %s", cmd)
	}
	// An unrelated binary is still not ours.
	if Managed("/usr/bin/other install --hook-mode", "install") {
		t.Fatal("unrelated binary must not be treated as sx")
	}
}

// shellQuote escapes embedded quotes; splitCommand has to read that back.
func TestSplitCommandRoundTripsEscapedQuotes(t *testing.T) {
	stubGOOS(t, "darwin")
	weird := `/opt/my "sx" dir/sx`
	quoted := shellQuote(weird)
	got := splitCommand(quoted + " install --hook-mode")
	if len(got) == 0 || got[0] != weird {
		t.Fatalf("argv[0] = %q, want %q (from %s)", got, weird, quoted)
	}
	if !Managed(quoted+" install --hook-mode", "install") {
		t.Fatalf("escaped form not recognized: %s", quoted)
	}
}

// An argv-shaped config must not be joined and re-split: a spaced path would
// break in the wrong place and the entry would be orphaned.
func TestManagedArgv(t *testing.T) {
	stubGOOS(t, "darwin")
	cases := []struct {
		name string
		argv []string
		want bool
	}{
		{"bare sx", []string{"sx", "report-usage", "--client=codex"}, true},
		{"absolute path", []string{"/opt/homebrew/bin/sx", "report-usage", "--client=codex"}, true},
		{"path with a space", []string{"/My Apps/sx.app/Contents/Resources/sx", "report-usage"}, true},
		{"legacy skills binary", []string{"skills", "report-usage"}, true},
		{"wrong subcommand", []string{"sx", "audit"}, false},
		{"someone else's notify", []string{"/usr/bin/notify-send", "hello"}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ManagedArgv(tc.argv, "report-usage"); got != tc.want {
				t.Fatalf("ManagedArgv(%v) = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}

// The round trip that matters most on Windows: a spaced CLI path is quoted by
// Command, and Managed has to read it back. Treating "\" as an escape here ate
// every path separator, so sx stopped recognizing its own hooks and the install
// loop appended a new one on every run.
func TestWindowsQuotedPathRoundTrip(t *testing.T) {
	stubGOOS(t, "windows")
	const p = `C:\Program Files\sx\sx.exe`

	quoted := shellQuote(p)
	if quoted != `"`+p+`"` {
		t.Fatalf("shellQuote = %q", quoted)
	}
	got := splitCommand(quoted + " install --hook-mode --client=cline")
	if len(got) == 0 || got[0] != p {
		t.Fatalf("argv[0] = %q, want %q", got, p)
	}
	if !Managed(quoted+" install --hook-mode --client=cline", "install") {
		t.Fatal("sx must recognize its own quoted Windows hook")
	}
	if !ManagedArgv([]string{p, "report-usage"}, "report-usage") {
		t.Fatal("argv form must recognize a spaced Windows path")
	}
}

// The GUI-binary branch inspects the whole command string, so it has to reject a
// multi-token command that merely ends in that name — overwriting a user's own
// MCP server is the destructive outcome.
func TestNeedsRepairWholeValueRequiresAbsolutePath(t *testing.T) {
	stubGOOS(t, "darwin")
	for _, cmd := range []string{
		"docker run acme/sx-app",
		"npx -y some/sx-app",
		"./sx-app",
	} {
		if NeedsRepair(cmd) {
			t.Fatalf("NeedsRepair(%q) = true; not an absolute path, so not ours", cmd)
		}
	}
	// The case the branch exists for still works.
	if !NeedsRepair("/Applications/sx.app/Contents/MacOS/sx-app") {
		t.Fatal("absolute GUI binary path must be repaired")
	}
	stubGOOS(t, "windows")
	if !NeedsRepair(`C:\Program Files\sx\sx-app.exe`) {
		t.Fatal("absolute Windows GUI path must be repaired")
	}
	if NeedsRepair(`docker run acme\sx-app.exe`) {
		t.Fatal("multi-token Windows command must be left alone")
	}
}

func TestIsAbsolutePathAcrossPlatforms(t *testing.T) {
	for _, p := range []string{`/usr/local/bin/sx`, `C:\sx\sx.exe`, `c:/sx/sx.exe`, `\\server\share\sx.exe`} {
		if !isAbsolutePath(p) {
			t.Errorf("isAbsolutePath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"", "sx", "acme/sx-app", "./sx", `..\sx.exe`, "1:/sx"} {
		if isAbsolutePath(p) {
			t.Errorf("isAbsolutePath(%q) = true, want false", p)
		}
	}
}

// A bare "sx" is written when no CLI can be found. It is not broken, so
// NeedsRepair leaves it, but it should be upgraded once a CLI exists — otherwise
// a degraded first install never improves.
func TestShouldRewriteUpgradesDegradedBareEntry(t *testing.T) {
	stubGOOS(t, "darwin")
	// The CLI lives away from the stubbed executable, or the sibling lookup would
	// find it and Resolve would succeed in the case that needs it to fail.
	cliDir := tempRoot(t)
	cli := writeFakeCLI(t, cliDir, "sx")
	appDir := tempRoot(t)

	// No CLI resolvable: nothing better to point at, so leave it alone.
	t.Setenv(EnvOverride, filepath.Join(appDir, "absent"))
	t.Setenv("PATH", tempRoot(t))
	stubExecutable(t, filepath.Join(appDir, "sx-app"))
	stubInstallDirs(t, nil)
	if ShouldRewrite("sx") {
		t.Fatal("with no CLI resolvable a bare sx is the best available; leave it")
	}

	// CLI resolvable: upgrade it.
	t.Setenv(EnvOverride, cli)
	if !ShouldRewrite("sx") {
		t.Fatal("bare sx should be upgraded once a CLI resolves")
	}
	// Still must not touch anyone else's entry.
	if ShouldRewrite("npx -y @acme/mcp") {
		t.Fatal("a third-party command must never be rewritten")
	}
	// An already-correct absolute path needs no rewrite.
	if ShouldRewrite(cli) {
		t.Fatalf("current path %q should not be rewritten", cli)
	}
}

// Every character shellQuote escapes must survive a round trip through
// splitCommand; the two sets drifted once and left literal backslashes behind.
func TestShellQuoteSplitCommandRoundTripAllEscapes(t *testing.T) {
	stubGOOS(t, "darwin")
	for _, weird := range []string{
		`/opt/a b/sx`,
		`/opt/quote"dir/sx`,
		`/opt/back\slash/sx`,
		`/opt/dollar$var/sx`,
		"/opt/tick`cmd`/sx",
		"/opt/all $of \"them\"`and`\\more/sx",
	} {
		quoted := shellQuote(weird)
		got := splitCommand(quoted + " install --hook-mode")
		if len(got) == 0 || got[0] != weird {
			t.Errorf("round trip lost argv[0]\n  input:  %q\n  quoted: %q\n  got:    %q", weird, quoted, got)
		}
	}
}
