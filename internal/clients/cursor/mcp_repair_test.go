package cursor

import (
	"testing"

	"github.com/sleuth-io/sx/v2/internal/clipath"
)

// An entry written by an older desktop-app build names the Wails GUI binary,
// which can never answer MCP. Skipping registration because "an entry exists"
// would leave those users broken permanently, since nothing else rewrites it.
//
// Only the host-independent cases live here. Whether a bare "sx" gets upgraded
// depends on what resolves on the machine, which clipath's own tests cover with
// the resolver stubbed.
func TestMCPEntryNeedsRewrite(t *testing.T) {
	cases := []struct {
		name  string
		entry any
		want  bool
	}{
		{
			name:  "gui binary written by an older app build",
			entry: map[string]any{"command": "/Applications/sx.app/Contents/MacOS/sx-app", "args": []any{"serve"}},
			want:  true,
		},
		{
			name:  "cli path that no longer exists",
			entry: map[string]any{"command": "/gone/sx", "args": []any{"serve"}},
			want:  true,
		},
		{
			name:  "hand-written third-party server must be preserved",
			entry: map[string]any{"command": "npx", "args": []any{"-y", "@acme/mcp"}},
			want:  false,
		},
		{
			name:  "malformed entry is left alone",
			entry: "not-an-object",
			want:  false,
		},
		{
			name:  "entry without a command is left alone",
			entry: map[string]any{"args": []any{"serve"}},
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clipath.MCPEntryNeedsRewrite(tc.entry); got != tc.want {
				t.Fatalf("clipath.MCPEntryNeedsRewrite(%v) = %v, want %v", tc.entry, got, tc.want)
			}
		})
	}
}

// A bare "sx" with the user's own args is their invocation of the same binary;
// upgrading its path must not rewrite what it does.
func TestMCPEntryLeavesHandWrittenArgsAlone(t *testing.T) {
	entry := map[string]any{"command": "sx", "args": []any{"cloud", "serve"}}
	if clipath.MCPEntryNeedsRewrite(entry) {
		t.Fatal("a bare sx with non-default args must be left alone")
	}
	// A broken command is replaced regardless of args — it cannot work as-is.
	broken := map[string]any{
		"command": "/Applications/sx.app/Contents/MacOS/sx-app",
		"args":    []any{"cloud", "serve"},
	}
	if !clipath.MCPEntryNeedsRewrite(broken) {
		t.Fatal("the GUI binary is unusable whatever its args")
	}
}
