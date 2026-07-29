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
