package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepoCommandsDoNotEscapeToAncestor pins the GIT_CEILING_DIRECTORIES
// guard in commandInRepo: a directory whose .git is unusable, nested
// inside a real repository, must make repo-addressed commands fail
// rather than fall through to the ancestor. Without the ceiling, fetch
// discovers the ancestor and exits 0 (no remotes to fetch), and
// checkout of a SHA the ancestor contains succeeds — detaching the
// HEAD of a repository sx does not own.
func TestRepoCommandsDoNotEscapeToAncestor(t *testing.T) {
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	ancestor := t.TempDir()
	run(ancestor, "init")
	run(ancestor, "config", "user.email", "alice@example.com")
	run(ancestor, "config", "user.name", "Alice")
	if err := os.WriteFile(filepath.Join(ancestor, "f.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(ancestor, "add", ".")
	run(ancestor, "commit", "-m", "c1")
	ancestorSHA := run(ancestor, "rev-parse", "HEAD")

	// A corrupt cache dir inside the ancestor: .git exists but is empty.
	cache := filepath.Join(ancestor, "cache", "git-repos", "hash")
	if err := os.MkdirAll(filepath.Join(cache, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	client := NewClient()
	ctx := context.Background()

	if err := client.Fetch(ctx, cache); err == nil {
		t.Fatal("Fetch on a corrupt cache dir resolved to the ancestor repository")
	}
	if err := client.Checkout(ctx, cache, ancestorSHA); err == nil {
		t.Fatal("Checkout on a corrupt cache dir resolved to the ancestor repository")
	}

	// The ancestor must be completely untouched.
	if got := run(ancestor, "rev-parse", "HEAD"); got != ancestorSHA {
		t.Fatalf("ancestor HEAD changed: %q -> %q", ancestorSHA, got)
	}
	if status := run(ancestor, "status", "--porcelain", "f.txt"); status != "" {
		t.Fatalf("ancestor working tree dirtied: %q", status)
	}

	// Git exports GIT_DIR to hook processes and rebase/bisect helpers,
	// and GIT_DIR outranks discovery entirely — so an sx run from inside
	// a git hook must not have its cache commands redirected at the
	// hook's repository.
	t.Setenv("GIT_DIR", filepath.Join(ancestor, ".git"))
	if err := client.Checkout(ctx, cache, ancestorSHA); err == nil {
		t.Fatal("Checkout honored an inherited GIT_DIR")
	}
	if err := client.Fetch(ctx, cache); err == nil {
		t.Fatal("Fetch honored an inherited GIT_DIR")
	}
	if got := run(ancestor, "rev-parse", "HEAD"); got != ancestorSHA {
		t.Fatalf("ancestor HEAD changed via GIT_DIR: %q -> %q", ancestorSHA, got)
	}
}
