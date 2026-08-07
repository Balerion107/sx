package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// execGitCommandWithEnv creates a git command with optional SSH key
// configuration and extra environment values.
// Returns the command ready to be executed (caller must call .Run(), .Output(), or .CombinedOutput())
func execGitCommandWithEnv(ctx context.Context, sshKeyPath string, extraEnv []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)

	// Start from the parent environment and disable interactive prompts so
	// git fails fast instead of hanging on /dev/tty when credentials are
	// missing or the repo is unreachable/private. GIT_TERMINAL_PROMPT=0
	// is the load-bearing setting; we deliberately do NOT set empty
	// GIT_ASKPASS/SSH_ASKPASS because their "unset vs empty" semantics
	// vary between git versions, and the terminal-prompt path is already
	// closed.
	//
	// Repo-selecting variables are stripped from the inherited
	// environment for every invocation — clone, probes, and repo-
	// addressed commands alike — because git exports them to hook
	// processes and rebase/bisect helpers, and an inherited GIT_DIR or
	// GIT_WORK_TREE would redirect any of these commands at the caller's
	// own repository (or make a clone write its index elsewhere).
	env := append(withoutEnv(os.Environ(), repoSelectingEnv), "GIT_TERMINAL_PROMPT=0")
	env = append(env, extraEnv...)

	if sshKeyPath != "" {
		// Validate SSH key (log warning but continue - will fail at exec time if invalid)
		if err := ValidateSSHKey(sshKeyPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		}

		// Configure SSH command
		sshCmd := buildSSHCommand(sshKeyPath)
		env = append(env, "GIT_SSH_COMMAND="+sshCmd)
	}

	cmd.Env = env
	return cmd
}

// execGitCommandWithURLAndEnv prepares a git command with URL conversion if
// needed and extra environment values.
// If sshKeyPath is provided and URL is HTTPS, converts URL to SSH.
// Returns the command, the final URL used, and any error.
func execGitCommandWithURLAndEnv(ctx context.Context, sshKeyPath string, extraEnv []string, url string, args ...string) (*exec.Cmd, string, error) {
	finalURL := url

	// Convert HTTPS to SSH if SSH key is provided
	if sshKeyPath != "" && IsHTTPSURL(url) {
		convertedURL, err := ConvertToSSH(url)
		if err != nil {
			return nil, "", fmt.Errorf("failed to convert URL to SSH: %w", err)
		}
		finalURL = convertedURL
	}

	// Create command with the final URL appended to args
	fullArgs := append(args, finalURL)
	cmd := execGitCommandWithEnv(ctx, sshKeyPath, extraEnv, fullArgs...)

	return cmd, finalURL, nil
}
