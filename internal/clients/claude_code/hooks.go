package claude_code

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sleuth-io/sx/v2/internal/bootstrap"
	"github.com/sleuth-io/sx/v2/internal/clients/claude_code/handlers"
	"github.com/sleuth-io/sx/v2/internal/clipath"
	"github.com/sleuth-io/sx/v2/internal/logger"
	"github.com/sleuth-io/sx/v2/internal/utils"
)

// installBootstrap installs Claude Code infrastructure (hooks and MCP servers).
// This sets up hooks for auto-update/usage tracking and registers MCP servers.
// Only installs options that are present in the opts slice.
func installBootstrap(opts []bootstrap.Option) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}
	claudeDir := filepath.Join(home, ".claude")

	log := logger.Get()

	// Install session start hook for auto-update (if enabled)
	if bootstrap.ContainsKey(opts, bootstrap.SessionHookKey) {
		if err := installSessionStartHook(claudeDir); err != nil {
			log.Error("failed to install session start hook", "error", err)
			return fmt.Errorf("failed to install session start hook: %w", err)
		}
	}

	// Install usage reporting hook (if enabled)
	if bootstrap.ContainsKey(opts, bootstrap.AnalyticsHookKey) {
		if err := installUsageReportingHook(claudeDir); err != nil {
			log.Error("failed to install usage reporting hook", "error", err)
			return fmt.Errorf("failed to install usage reporting hook: %w", err)
		}
	}

	// Install MCP servers from options that have MCPConfig
	for _, opt := range opts {
		if opt.MCPConfig != nil {
			if err := installMCPServerFromConfig(claudeDir, opt.MCPConfig); err != nil {
				log.Error("failed to install MCP server", "server", opt.MCPConfig.Name, "error", err)
				return fmt.Errorf("failed to install MCP server %s: %w", opt.MCPConfig.Name, err)
			}
		}
	}

	return nil
}

// installMCPServerFromConfig installs an MCP server from a bootstrap.MCPServerConfig
func installMCPServerFromConfig(claudeDir string, config *bootstrap.MCPServerConfig) error {
	log := logger.Get()

	serverConfig := map[string]any{
		"type":    "stdio",
		"command": config.Command,
		"args":    config.Args,
	}

	// Add env if present
	if len(config.Env) > 0 {
		envMap := make(map[string]any)
		for k, v := range config.Env {
			envMap[k] = v
		}
		serverConfig["env"] = envMap
	} else {
		serverConfig["env"] = map[string]any{}
	}

	if err := handlers.AddMCPServer(claudeDir, config.Name, serverConfig); err != nil {
		return err
	}

	log.Info("MCP server installed", "server", config.Name, "command", config.Command)
	return nil
}

// installSessionStartHook converges the SessionStart hook on the current command:
// it updates sx's command wherever it already sits, collapses duplicates to one,
// and adds the hook when none is present. A user's matcher, their sibling hooks,
// and keys such as timeout on sx's own command are all preserved.
func installSessionStartHook(claudeDir string) error {
	settingsPath := filepath.Join(claudeDir, "settings.json")
	log := logger.Get()

	// Read existing settings or create new
	var settings map[string]any
	if data, err := os.ReadFile(settingsPath); err == nil {
		if err := utils.UnmarshalJSONC(data, &settings); err != nil {
			log.Error("failed to parse settings.json for SessionStart hook", "error", err)
			return fmt.Errorf("failed to parse settings.json: %w", err)
		}
	} else {
		settings = make(map[string]any)
	}

	// Get or create hooks section
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		hooks = make(map[string]any)
		settings["hooks"] = hooks
	}

	// Get or create SessionStart array
	sessionStart, ok := hooks["SessionStart"].([]any)
	if !ok {
		sessionStart = []any{}
	}

	hookCommand := clipath.CommandOrBare("install", "--hook-mode", "--client=claude-code")

	// SessionStart carries no matcher of sx's own, so an entry sx reuses keeps
	// whatever matcher the user put on it.
	converged, changed, existed, replaced := convergeHookArray(sessionStart, hookCommand, "install", "")
	if !changed {
		return nil
	}

	// Get current working directory for context logging
	cwd, _ := os.Getwd()

	hooks["SessionStart"] = converged
	switch {
	case replaced > 0:
		log.Info("hook updated", "hook", "SessionStart", "command", hookCommand,
			"replaced", replaced, "cwd", cwd)
	case existed > 0:
		// The command was already current; something around it changed.
		log.Info("hook repaired", "hook", "SessionStart", "command", hookCommand, "cwd", cwd)
	default:
		log.Info("hook installed", "hook", "SessionStart", "command", hookCommand, "cwd", cwd)
	}

	// Write back to file
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		log.Error("failed to marshal settings for SessionStart hook", "error", err)
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(settingsPath, data, 0644); err != nil {
		log.Error("failed to write settings.json for SessionStart hook", "error", err, "path", settingsPath)
		return fmt.Errorf("failed to write settings.json: %w", err)
	}

	return nil
}

// uninstallBootstrap removes Claude Code infrastructure (hooks and MCP servers).
// Only uninstalls options that are present in the opts slice.
func uninstallBootstrap(opts []bootstrap.Option) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}
	claudeDir := filepath.Join(home, ".claude")
	settingsPath := filepath.Join(claudeDir, "settings.json")

	log := logger.Get()

	// Build a set of options to uninstall
	uninstallSession := false
	uninstallAnalytics := false
	var mcpToUninstall []string

	for _, opt := range opts {
		switch opt.Key {
		case bootstrap.SessionHookKey:
			uninstallSession = true
		case bootstrap.AnalyticsHookKey:
			uninstallAnalytics = true
		default:
			if opt.MCPConfig != nil {
				mcpToUninstall = append(mcpToUninstall, opt.MCPConfig.Name)
			}
		}
	}

	// Read existing settings for hook removal
	data, err := os.ReadFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read settings.json: %w", err)
	}

	if len(data) > 0 && (uninstallSession || uninstallAnalytics) {
		var settings map[string]any
		if err := utils.UnmarshalJSONC(data, &settings); err != nil {
			return fmt.Errorf("failed to parse settings.json: %w", err)
		}

		hooks, ok := settings["hooks"].(map[string]any)
		if ok {
			modified := false

			// Remove SessionStart hook if requested
			if uninstallSession {
				if sessionStart, ok := hooks["SessionStart"].([]any); ok {
					filtered, removed := removeSxHooks(sessionStart,
						clipath.CommandOrBare("install", "--hook-mode", "--client=claude-code"), "install")
					if removed > 0 {
						modified = true
						if len(filtered) == 0 {
							delete(hooks, "SessionStart")
						} else {
							hooks["SessionStart"] = filtered
						}
						log.Info("hook removed", "hook", "SessionStart")
					}
				}
			}

			// Remove PostToolUse hook if requested
			if uninstallAnalytics {
				if postToolUse, ok := hooks["PostToolUse"].([]any); ok {
					filtered, removed := removeSxHooks(postToolUse,
						clipath.CommandOrBare("report-usage", "--client=claude-code"), "report-usage")
					if removed > 0 {
						modified = true
						if len(filtered) == 0 {
							delete(hooks, "PostToolUse")
						} else {
							hooks["PostToolUse"] = filtered
						}
						log.Info("hook removed", "hook", "PostToolUse")
					}
				}
			}

			// Remove empty hooks section
			if len(hooks) == 0 {
				delete(settings, "hooks")
			}

			if modified {
				data, err = json.MarshalIndent(settings, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal settings: %w", err)
				}

				if err := os.WriteFile(settingsPath, data, 0644); err != nil {
					return fmt.Errorf("failed to write settings.json: %w", err)
				}
			}
		}
	}

	// Remove MCP servers
	for _, name := range mcpToUninstall {
		if err := uninstallMCPServerByName(claudeDir, name); err != nil {
			log.Error("failed to uninstall MCP server", "server", name, "error", err)
			return fmt.Errorf("failed to uninstall MCP server %s: %w", name, err)
		}
	}

	return nil
}

// convergeHookArray brings one Claude Code hook array to the state sx wants,
// and reports whether anything changed.
//
// The rule is that sx's command lives in its own entry. Sharing an entry with a
// user's hooks was the source of two irreconcilable pulls: sx wants to control
// the matcher on the entry holding its command, while the user's hooks in that
// same entry are governed by the matcher they wrote. Splitting resolves both —
// their entry keeps their matcher untouched, ours carries whatever sx owns.
//
// ownMatcher is the matcher sx owns for this hook, empty when it owns none. For
// PostToolUse it decides which tool events get reported, which is sx's concern,
// so it is re-asserted even when the command itself is already current. For
// SessionStart sx writes no matcher, so an entry it reuses keeps the user's, and
// an entry split out of a shared one inherits that entry's matcher.
//
// Keys the user added to sx's command object — "timeout" — survive: the object
// is carried over rather than rebuilt, and when several copies exist their extra
// keys are merged, since there is no way to tell which copy the user edited.
//
// Returns the converged array, whether anything changed, how many of sx's
// commands were already present, and how many were replaced.
func convergeHookArray(entries []any, hookCommand, subcommand, ownMatcher string) ([]any, bool, int, int) {
	kept := make([]any, 0, len(entries)+1)

	var ourEntry map[string]any // an entry that held only our command
	var ours []map[string]any   // every copy of our command found
	inheritedMatcher := ""
	alreadyCurrent := false
	replaced := 0
	shared := false

	for _, item := range entries {
		entry, theirs, mine := splitEntry(item, hookCommand, subcommand)
		if entry == nil {
			// Not an entry shape we understand; leave it alone.
			kept = append(kept, item)
			continue
		}
		ours = append(ours, mine...)
		for _, obj := range mine {
			if cmd, _ := obj["command"].(string); cmd == hookCommand {
				alreadyCurrent = true
			} else {
				replaced++
			}
		}

		switch {
		case len(mine) == 0:
			kept = append(kept, item)
		case len(theirs) > 0:
			// Shared: leave their hooks and their matcher exactly as they are, and
			// remember the matcher so the entry sx splits out inherits it —
			// otherwise sx's command would stop honoring the scope they set.
			if m, ok := entry["matcher"].(string); ok && inheritedMatcher == "" {
				inheritedMatcher = m
			}
			entry["hooks"] = theirs
			kept = append(kept, entry)
			shared = true
		case ourEntry == nil:
			// Held only our command, so it is ours to reuse.
			ourEntry = entry
		default:
			// A further duplicate: drop it.
		}
	}

	ourCommand := mergeCommandObjects(ours)
	ourCommand["command"] = hookCommand
	// The object can be inherited from a hand-written entry with no "type"; the
	// entry sx owns outright should be well-formed.
	if t, _ := ourCommand["type"].(string); t == "" {
		ourCommand["type"] = "command"
	}

	createdEntry := ourEntry == nil
	if createdEntry {
		ourEntry = map[string]any{}
	}
	priorMatcher, _ := ourEntry["matcher"].(string)
	ourEntry["hooks"] = []any{ourCommand}
	switch {
	case ownMatcher != "":
		ourEntry["matcher"] = ownMatcher
	case createdEntry && inheritedMatcher != "":
		ourEntry["matcher"] = inheritedMatcher
	}
	kept = append(kept, ourEntry)

	// Duplicates that were dropped are replacements too, whatever text they
	// carried; a matcher-only correction replaces nothing and reports zero.
	if dropped := len(ours) - 1; dropped > replaced {
		replaced = dropped
	}

	// Unchanged only when there was exactly one command, it was already current,
	// it was not sharing an entry, and any matcher sx owns already matched.
	unchanged := len(ours) == 1 && alreadyCurrent && !shared
	if unchanged && ownMatcher != "" && priorMatcher != ownMatcher {
		unchanged = false
	}
	return kept, !unchanged, len(ours), replaced
}

// splitEntry separates one hook entry's commands into the user's and sx's.
// It returns a nil entry when the item is not a shape this code understands.
func splitEntry(item any, hookCommand, subcommand string) (entry map[string]any, theirs []any, mine []map[string]any) {
	entry, ok := item.(map[string]any)
	if !ok {
		return nil, nil, nil
	}
	hooksArray, ok := entry["hooks"].([]any)
	if !ok {
		return nil, nil, nil
	}

	theirs = make([]any, 0, len(hooksArray))
	for _, h := range hooksArray {
		obj, ok := h.(map[string]any)
		if !ok {
			theirs = append(theirs, h)
			continue
		}
		cmd, ok := obj["command"].(string)
		// Byte equality alongside the predicate: a Managed false negative for a
		// command sx wrote would otherwise grow the array without bound.
		if !ok || (cmd != hookCommand && !clipath.Managed(cmd, subcommand)) {
			theirs = append(theirs, h)
			continue
		}
		mine = append(mine, obj)
	}
	return entry, theirs, mine
}

// mergeCommandObjects folds every copy of sx's command object into one, keeping
// the first value seen for each key. Copies are indistinguishable as to which
// the user edited, so a "timeout" set on any of them is honored rather than
// silently dropped because it sat on the copy that lost.
func mergeCommandObjects(objs []map[string]any) map[string]any {
	merged := map[string]any{}
	for _, obj := range objs {
		for k, v := range obj {
			if k == "command" {
				continue
			}
			if _, exists := merged[k]; !exists {
				merged[k] = v
			}
		}
	}
	return merged
}

// removeSxHooks strips sx's commands from the given hook entries, in either the
// legacy bare-"sx" form or the absolute-path form current versions write, and
// drops an entry only when nothing of the user's remains in it. currentCommand
// is matched byte-for-byte alongside the subcommand predicate, the same pairing
// install uses: if Managed ever has a false negative for a command sx wrote,
// uninstall would otherwise leave that hook behind.
func removeSxHooks(hooks []any, currentCommand string, subcommands ...string) ([]any, int) {
	var filtered []any
	removed := 0
	for _, item := range hooks {
		hookMap, ok := item.(map[string]any)
		if !ok {
			filtered = append(filtered, item)
			continue
		}
		hooksArray, ok := hookMap["hooks"].([]any)
		if !ok {
			filtered = append(filtered, item)
			continue
		}

		// Strip only our commands. Dropping the whole entry would take a
		// co-located user hook with it — install deliberately preserves those, so
		// uninstall destroying them would be the worse half of an asymmetry.
		keptCommands := make([]any, 0, len(hooksArray))
		strippedHere := 0
		for _, h := range hooksArray {
			if hMap, ok := h.(map[string]any); ok {
				if cmd, ok := hMap["command"].(string); ok && (cmd == currentCommand || clipath.Managed(cmd, subcommands...)) {
					strippedHere++
					continue
				}
			}
			keptCommands = append(keptCommands, h)
		}
		removed += strippedHere

		switch {
		case strippedHere == 0:
			filtered = append(filtered, item)
		case len(keptCommands) > 0:
			hookMap["hooks"] = keptCommands
			filtered = append(filtered, hookMap)
		}
		// Entry held only our commands: drop it.
	}
	return filtered, removed
}

// postToolUseMatcher is sx's own matcher for the usage-reporting hook: it decides
// which tool events are worth reporting, which is sx's concern rather than the
// user's. Unlike a SessionStart matcher, it is re-asserted on update.
const postToolUseMatcher = "Skill|Task|SlashCommand|mcp__.*"

// installUsageReportingHook converges the PostToolUse usage-reporting hook the
// same way installSessionStartHook does, with one difference: the matcher on this
// hook is sx's own config, so it is re-asserted rather than preserved.
func installUsageReportingHook(claudeDir string) error {
	settingsPath := filepath.Join(claudeDir, "settings.json")
	log := logger.Get()

	// Read existing settings or create new
	var settings map[string]any
	if data, err := os.ReadFile(settingsPath); err == nil {
		if err := utils.UnmarshalJSONC(data, &settings); err != nil {
			log.Error("failed to parse settings.json for PostToolUse hook", "error", err)
			return fmt.Errorf("failed to parse settings.json: %w", err)
		}
	} else {
		settings = make(map[string]any)
	}

	// Get or create hooks section
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		hooks = make(map[string]any)
		settings["hooks"] = hooks
	}

	// Get or create PostToolUse array
	postToolUse, ok := hooks["PostToolUse"].([]any)
	if !ok {
		postToolUse = []any{}
	}

	hookCommand := clipath.CommandOrBare("report-usage", "--client=claude-code")

	converged, changed, existed, replaced := convergeHookArray(postToolUse, hookCommand, "report-usage", postToolUseMatcher)
	if !changed {
		return nil
	}
	hooks["PostToolUse"] = converged
	switch {
	case replaced > 0:
		log.Info("hook updated", "hook", "PostToolUse", "command", hookCommand, "replaced", replaced)
	case existed > 0:
		log.Info("hook repaired", "hook", "PostToolUse", "command", hookCommand)
	default:
		log.Info("hook installed", "hook", "PostToolUse", "command", hookCommand)
	}

	// Write back to file
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		log.Error("failed to marshal settings for PostToolUse hook", "error", err)
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(settingsPath, data, 0644); err != nil {
		log.Error("failed to write settings.json for PostToolUse hook", "error", err, "path", settingsPath)
		return fmt.Errorf("failed to write settings.json: %w", err)
	}

	return nil
}

// uninstallMCPServerByName removes an MCP server by name from ~/.claude.json
func uninstallMCPServerByName(claudeDir, name string) error {
	log := logger.Get()

	if err := handlers.RemoveMCPServer(claudeDir, name); err != nil {
		return err
	}

	log.Info("MCP server uninstalled", "server", name)
	return nil
}
