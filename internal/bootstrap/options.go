package bootstrap

import "github.com/sleuth-io/sx/v2/internal/clipath"

// Option describes a configurable bootstrap item
type Option struct {
	Key         string           // Unique key for config storage
	Description string           // What to show user
	Prompt      string           // Question to ask
	Default     bool             // Suggested answer
	DeclineNote string           // Note shown if declined (optional)
	MCPConfig   *MCPServerConfig // For MCP options - generic install config
}

// MCPServerConfig contains info to install an MCP server generically
type MCPServerConfig struct {
	Name    string            // Server name (e.g., "sx")
	Command string            // Command to run
	Args    []string          // Arguments
	Env     map[string]string // Environment variables
}

// Pre-defined options - clients/vaults return these
// Use Option.Key for comparisons (e.g., opt.Key == SessionHookKey)

// Option keys as constants for comparison
const (
	SessionHookKey      = "session_hook"
	AnalyticsHookKey    = "analytics_hook"
	SleuthAIQueryMCPKey = "sleuth_ai_query_mcp"
)

// SessionHook is the session start hook option for auto-update.
// Installs hooks for all detected clients (Claude Code, Copilot CLI, Cursor).
var SessionHook = Option{
	Key:         SessionHookKey,
	Description: "Session hook - Auto-update assets when sessions start",
	Prompt:      "Install session hooks? (recommended)",
	Default:     true,
	DeclineNote: "Without this hook, you'll need to run 'sx install' manually.",
}

// AnalyticsHook is the usage tracking hook option.
// Installs hooks for all detected clients (Claude Code, Copilot CLI, Cursor).
var AnalyticsHook = Option{
	Key:         AnalyticsHookKey,
	Description: "Analytics hook - Track skill usage for analytics",
	Prompt:      "Install analytics hooks?",
	Default:     true,
	DeclineNote: "Skill usage analytics will not be tracked.",
}

// SleuthAIQueryMCP returns the Sleuth AI query MCP server option
// Future: may split into multiple options to enable specific tools
func SleuthAIQueryMCP() Option {
	// The client execs this later, so it needs the CLI — not whichever binary is
	// running now. os.Executable() here was the Wails GUI binary when the desktop
	// app wrote the entry, and that binary has no subcommands, so the client would
	// launch a window and wait forever for stdio MCP traffic. It also discarded
	// its error, which yielded an empty Command.
	//
	// This is the widest-reach instance: the value flows into every client that
	// handles opt.MCPConfig.
	return Option{
		Key:         SleuthAIQueryMCPKey,
		Description: "Sleuth AI Query MCP - Enables 'sx query' tool for GitHub, CI, Linear, Datadog",
		Prompt:      "Install Sleuth AI Query MCP server?",
		Default:     false,
		MCPConfig: &MCPServerConfig{
			Name:    "sx",
			Command: clipath.ResolveOrBare(),
			Args:    []string{"serve"},
		},
	}
}

// ContainsKey returns true if the options slice contains an option with the given key
func ContainsKey(opts []Option, key string) bool {
	for _, opt := range opts {
		if opt.Key == key {
			return true
		}
	}
	return false
}

// Filter returns options where isEnabled returns true for the option's key
func Filter(opts []Option, isEnabled func(key string) bool) []Option {
	var result []Option
	for _, opt := range opts {
		if isEnabled(opt.Key) {
			result = append(result, opt)
		}
	}
	return result
}
