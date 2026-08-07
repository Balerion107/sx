package vault

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestInstallScriptSyntax runs the rendered install script through
// bash -n so a template edit that breaks the shell fails here instead of
// shipping to every vault.
func TestInstallScriptSyntax(t *testing.T) {
	requireBash(t)

	scriptPath := writeRenderedInstallScript(t, "https://github.com/acme/vault")
	if out, err := exec.Command("bash", "-n", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("rendered install.sh has a shell syntax error: %v\n%s", err, out)
	}
}

// TestInstallScriptConfigCheckExecution executes the rendered script
// against fake configs and a stub sx binary, asserting the exit codes
// the already-configured logic exists to distinguish. String assertions
// can't hold this behavior — only running it can.
func TestInstallScriptConfigCheckExecution(t *testing.T) {
	requireBash(t)

	scriptPath := writeRenderedInstallScript(t, "https://github.com/acme/vault")

	// A stub sx first on PATH: `command -v sx` finds it, and
	// --version / init are harmless no-ops.
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\necho \"stub-sx $*\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "sx"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	configFor := func(url string) string {
		return fmt.Sprintf(`{"profiles":{"default":{"repositoryUrl":%q}}}`, url)
	}

	cases := []struct {
		name       string
		configJSON string
		wantExit   int
		wantOutput string
	}{
		{
			name:       "same vault exits 0",
			configJSON: configFor("https://github.com/acme/vault"),
			wantExit:   0,
			wantOutput: "already configured for this vault",
		},
		{
			name:       "ssh spelling of same vault exits 0",
			configJSON: configFor("git@github.com:acme/vault.git"),
			wantExit:   0,
			wantOutput: "already configured for this vault",
		},
		{
			name:       "gitless spelling of same vault exits 0",
			configJSON: configFor("https://github.com/acme/vault.git"),
			wantExit:   0,
			wantOutput: "already configured for this vault",
		},
		{
			name:       "prefix neighbour is a different vault",
			configJSON: configFor("https://github.com/acme/vault-team"),
			wantExit:   1,
			wantOutput: "sx profile activate",
		},
		{
			name:       "no config falls through to sx init",
			configJSON: "",
			wantExit:   0,
			wantOutput: "stub-sx init --type git --repo-url https://github.com/acme/vault",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgDir := t.TempDir()
			if tc.configJSON != "" {
				if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(tc.configJSON), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			cmd := exec.Command("bash", scriptPath)
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"SX_CONFIG_DIR="+cfgDir,
			)
			out, err := cmd.CombinedOutput()

			exit := 0
			if err != nil {
				var ee *exec.ExitError
				if !errors.As(err, &ee) {
					t.Fatalf("failed to run script: %v\n%s", err, out)
				}
				exit = ee.ExitCode()
			}
			if exit != tc.wantExit {
				t.Fatalf("exit = %d, want %d\noutput:\n%s", exit, tc.wantExit, out)
			}
			if !strings.Contains(string(out), tc.wantOutput) {
				t.Fatalf("output missing %q:\n%s", tc.wantOutput, out)
			}
		})
	}
}

func requireBash(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("requires bash")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
}

func writeRenderedInstallScript(t *testing.T, repoURL string) string {
	t.Helper()
	script := generateInstallScript(repoURL)
	scriptPath := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return scriptPath
}
