package cline

import (
	"strings"
	"testing"
)

// A quoted absolute path is parsed by PowerShell as a string expression, not a
// command, so the body needs the call operator or the hook silently never runs.
func TestGenerateHookScriptWindowsInvokesCommand(t *testing.T) {
	cmd := `"C:\Program Files\sx\sx.exe" install --hook-mode --client=cline`
	body := generateHookScriptFor("windows", cmd)

	var line string
	for l := range strings.SplitSeq(body, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			line = l
			break
		}
	}
	if !strings.HasPrefix(line, "& ") {
		t.Fatalf("Windows body must invoke via the call operator, got %q", line)
	}
	if !strings.Contains(line, cmd) {
		t.Fatalf("command lost from Windows body: %q", line)
	}
	if !strings.Contains(body, sxHookMarker) {
		t.Fatal("marker missing; sx would not recognize its own hook")
	}
}

// The POSIX body is a shell script, where a quoted path is already a command.
func TestGenerateHookScriptPosixIsShellScript(t *testing.T) {
	body := generateHookScriptFor("darwin", "/opt/homebrew/bin/sx install --hook-mode")
	if !strings.HasPrefix(body, "#!/bin/sh") {
		t.Fatalf("POSIX body must start with a shebang, got %q", body)
	}
	if strings.Contains(body, "& /opt") {
		t.Fatal("call operator is PowerShell-only and must not leak into sh")
	}
}
