package vault

import (
	"encoding/json"
	"errors"
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

	// The fixture mirrors what sx actually writes: json.MarshalIndent
	// output with the active profile's repositoryUrl duplicated at the
	// top level for old-binary compat (internal/config/profile.go), so
	// the script's extraction is tested against the real spacing and
	// against multiple URL lines.
	type testProfile struct {
		Type          string `json:"type,omitempty"`
		RepositoryURL string `json:"repositoryUrl,omitempty"`
	}
	type testConfig struct {
		DefaultProfile string                 `json:"defaultProfile,omitempty"`
		ActiveProfiles []string               `json:"activeProfiles,omitempty"`
		Type           string                 `json:"type,omitempty"`
		RepositoryURL  string                 `json:"repositoryUrl,omitempty"`
		Profiles       map[string]testProfile `json:"profiles"`
	}
	marshal := func(cfg testConfig) string {
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	configFor := func(url string) string {
		return marshal(testConfig{
			DefaultProfile: "default",
			ActiveProfiles: []string{"default"},
			Type:           "git",
			RepositoryURL:  url,
			Profiles: map[string]testProfile{
				"default": {Type: "git", RepositoryURL: url},
			},
		})
	}
	// defaultURL belongs to profile "other" (the default); workURL to
	// profile "work". activeProfiles controls which are active.
	multiProfileConfig := func(activeProfiles []string, defaultURL, workURL string) string {
		return marshal(testConfig{
			DefaultProfile: "other",
			ActiveProfiles: activeProfiles,
			Type:           "git",
			RepositoryURL:  defaultURL,
			Profiles: map[string]testProfile{
				"other": {Type: "git", RepositoryURL: defaultURL},
				"work":  {Type: "git", RepositoryURL: workURL},
			},
		})
	}

	cases := []struct {
		name        string
		configJSON  string
		extraEnv    []string
		wantExit    int
		wantOutputs []string
	}{
		{
			name:        "same vault exits 0",
			configJSON:  configFor("https://github.com/acme/vault"),
			wantExit:    0,
			wantOutputs: []string{"already configured for this vault"},
		},
		{
			name:        "ssh spelling of same vault exits 0",
			configJSON:  configFor("git@github.com:acme/vault.git"),
			wantExit:    0,
			wantOutputs: []string{"already configured for this vault"},
		},
		{
			name:        "gitless spelling of same vault exits 0",
			configJSON:  configFor("https://github.com/acme/vault.git"),
			wantExit:    0,
			wantOutputs: []string{"already configured for this vault"},
		},
		{
			name:        "prefix neighbour is a different vault",
			configJSON:  configFor("https://github.com/acme/vault-team"),
			wantExit:    1,
			wantOutputs: []string{"sx profile activate"},
		},
		{
			// A match in an inactive profile is NOT success: sx install
			// only loads active profiles, so exiting 0 here would be the
			// silent-nothing outcome the check exists to prevent.
			name:        "inactive profile matching this vault exits 1 with activate guidance",
			configJSON:  multiProfileConfig([]string{"other"}, "https://github.com/acme/other", "git@github.com:acme/vault.git"),
			wantExit:    1,
			wantOutputs: []string{"not active", "sx profile activate"},
		},
		{
			// sx install merges every ACTIVE profile, not just the
			// default — this is exactly the state the script's own
			// remediation (profile add + activate) creates, so it must
			// read as success.
			name:        "second active profile matching this vault exits 0",
			configJSON:  multiProfileConfig([]string{"other", "work"}, "https://github.com/acme/other", "git@github.com:acme/vault.git"),
			wantExit:    0,
			wantOutputs: []string{"already configured for this vault"},
		},
		{
			// SX_PROFILE overrides the active set outright on sx's read
			// side, so a file-active profile it excludes must not count.
			name:        "SX_PROFILE excluding the matching profile exits 1",
			configJSON:  multiProfileConfig([]string{"other", "work"}, "https://github.com/acme/other", "git@github.com:acme/vault.git"),
			extraEnv:    []string{"SX_PROFILE=other"},
			wantExit:    1,
			wantOutputs: []string{"not active"},
		},
		{
			// …and a profile it includes counts even when the file's
			// activeProfiles doesn't list it.
			name:        "SX_PROFILE selecting the matching profile exits 0",
			configJSON:  multiProfileConfig([]string{"other"}, "https://github.com/acme/other", "git@github.com:acme/vault.git"),
			extraEnv:    []string{"SX_PROFILE=work"},
			wantExit:    0,
			wantOutputs: []string{"already configured for this vault"},
		},
		{
			name:       "no profile matches lists every configured vault",
			configJSON: multiProfileConfig([]string{"other"}, "https://github.com/acme/other", "https://github.com/acme/third"),
			wantExit:   1,
			wantOutputs: []string{
				"sx profile activate",
				"https://github.com/acme/other",
				"https://github.com/acme/third",
			},
		},
		{
			name:        "no config falls through to sx init",
			configJSON:  "",
			wantExit:    0,
			wantOutputs: []string{"stub-sx init --type git --repo-url https://github.com/acme/vault"},
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
			// Strip any host SX_PROFILE so only the case's extraEnv can
			// set it — the script honors it, so a stray value would
			// change the non-override cases.
			env := make([]string, 0, len(os.Environ()))
			for _, kv := range os.Environ() {
				if strings.HasPrefix(kv, "SX_PROFILE=") {
					continue
				}
				env = append(env, kv)
			}
			cmd.Env = append(env,
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"SX_CONFIG_DIR="+cfgDir,
			)
			cmd.Env = append(cmd.Env, tc.extraEnv...)
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
			for _, want := range tc.wantOutputs {
				if !strings.Contains(string(out), want) {
					t.Fatalf("output missing %q:\n%s", want, out)
				}
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
