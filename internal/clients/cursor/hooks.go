package cursor

import (
	"bytes"
	"encoding/json"
	"io"
	"os"

	"github.com/sleuth-io/sx/v2/internal/logger"
)

// stdinCache stores stdin data so it can be read multiple times
var stdinCache []byte

// ParseWorkspaceDir attempts to parse workspace directory from Cursor hook stdin.
// This is used by the install command when running in Cursor hook mode to determine
// the correct workspace context (since Cursor runs hooks from ~/.cursor).
// It caches stdin so it can be read multiple times.
func ParseWorkspaceDir() string {
	// cursorHookInput represents the JSON structure passed by Cursor hooks via stdin
	type cursorHookInput struct {
		WorkspaceRoots []string `json:"workspace_roots"`
	}

	// Check if stdin has data
	stat, err := os.Stdin.Stat()
	if err != nil {
		return ""
	}
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		// stdin is a terminal, not a pipe
		return ""
	}

	// Read stdin once and cache it
	if stdinCache == nil {
		stdinCache, err = io.ReadAll(os.Stdin)
		if err != nil {
			return ""
		}
	}

	// Parse from cached data
	var input cursorHookInput
	if err := json.Unmarshal(stdinCache, &input); err != nil {
		return ""
	}

	// Log warning if multiple workspace roots (not yet supported)
	if len(input.WorkspaceRoots) > 1 {
		log := logger.Get()
		log.Warn("multiple workspace roots detected, using first one", "count", len(input.WorkspaceRoots), "roots", input.WorkspaceRoots)
	}

	// Return first workspace root if available
	if len(input.WorkspaceRoots) > 0 {
		return input.WorkspaceRoots[0]
	}

	return ""
}

// GetCachedStdin returns a reader for the cached stdin data.
// This allows other parts of the code to read stdin even after ParseWorkspaceDir has consumed it.
func GetCachedStdin() io.Reader {
	if stdinCache == nil {
		return nil
	}
	return bytes.NewReader(stdinCache)
}
