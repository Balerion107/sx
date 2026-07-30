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

	// Update our command where it already sits, and collapse any extra copies to
	// one. Copies accumulate because an absolute path goes stale when the app
	// moves and older versions wrote a bare "sx"; leaving them behind runs the
	// install again on every session start. Updating in place rather than
	// removing and re-adding preserves a user-set "matcher", which decides when
	// the hook fires, and keys such as "timeout" on the command object. Byte
	// equality is checked alongside Managed so a false negative there cannot grow
	// the list without bound.
	kept := make([]any, 0, len(sessionStart))
	managedFound := 0
	upToDate := false
	placed := false
	for _, item := range sessionStart {
		// No own-matcher for SessionStart: sx appends without one, so any matcher
		// present is the user's and is left alone.
		remainder, found, current := updateManagedCommands(item, hookCommand, "install", "", &placed)
		managedFound += found
		if current {
			upToDate = true
		}
		if remainder != nil {
			kept = append(kept, remainder)
		}
	}

	// Nothing to do only when there was exactly one and it is already current.
	if upToDate && managedFound == 1 {
		return nil
	}

	// Get current working directory for context logging
	cwd, _ := os.Getwd()

	if !placed {
		kept = append(kept, map[string]any{
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": hookCommand,
				},
			},
		})
	}
	sessionStart = kept
	hooks["SessionStart"] = sessionStart
	if managedFound > 0 {
		log.Info("hook updated", "hook", "SessionStart", "command", hookCommand,
			"replaced", managedFound, "cwd", cwd)
	} else {
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

// updateManagedCommands rewrites sx's install command inside one SessionStart
// entry, in place.
//
// It returns what should remain of the entry (nil when nothing does), how many
// managed commands it saw, and whether one already carried the current command.
// placed tracks whether the current command has been written somewhere yet, so
// the first managed command found is updated and any further copies are dropped.
//
// Updating in place rather than removing and re-adding is what preserves a
// user-set "matcher" on the entry and keys such as "timeout" on the command
// object; only the "command" value itself is touched.
func updateManagedCommands(item any, hookCommand, subcommand, ownMatcher string, placed *bool) (remainder any, found int, current bool) {
	hookMap, ok := item.(map[string]any)
	if !ok {
		return item, 0, false
	}
	hooksArray, ok := hookMap["hooks"].([]any)
	if !ok {
		return item, 0, false
	}

	keptCommands := make([]any, 0, len(hooksArray))
	holdsOurs := false
	for _, h := range hooksArray {
		hMap, ok := h.(map[string]any)
		if !ok {
			keptCommands = append(keptCommands, h)
			continue
		}
		cmd, ok := hMap["command"].(string)
		if !ok {
			keptCommands = append(keptCommands, h)
			continue
		}
		// Byte-equality alongside Managed, so a false negative there cannot make
		// the list grow without bound.
		if cmd != hookCommand && !clipath.Managed(cmd, subcommand) {
			keptCommands = append(keptCommands, h)
			continue
		}

		found++
		if cmd == hookCommand {
			current = true
		}
		if *placed {
			// A duplicate: drop it.
			continue
		}
		hMap["command"] = hookCommand
		keptCommands = append(keptCommands, hMap)
		*placed = true
		holdsOurs = true
	}

	if found == 0 {
		return item, 0, false
	}
	if len(keptCommands) == 0 {
		return nil, found, current
	}
	hookMap["hooks"] = keptCommands
	// Where the matcher is sx's own config rather than the user's, re-assert it:
	// otherwise an entry carrying a stale or hand-edited matcher keeps it
	// forever, and the hook silently fires on the wrong set of events.
	//
	// Gated on this entry actually keeping our command, not on the shared placed
	// flag: a duplicate entry whose sx command was dropped now holds only the
	// user's hooks, and rewriting its matcher would change when those fire.
	if ownMatcher != "" && holdsOurs {
		if existing, _ := hookMap["matcher"].(string); existing != ownMatcher {
			hookMap["matcher"] = ownMatcher
		}
	}
	return hookMap, found, current
}

// removeSxHooks strips sx's commands from the given hook entries, in either the
// legacy bare-"sx" form or the absolute-path form current versions write, and
// drops an entry only when nothing of the user's remains in it. currentCommand
// is matched byte-for-byte alongside the subcommand predicate, the same pairing
// install uses: if Managed ever has a false negative for a command sx wrote,
// uninstall would otherwise leave that hook behind.
func removeSxHooks(hooks []any, currentCommand string, subcommands ...string) (filteredOut []any, removed int) {
	var filtered []any
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

	// Same in-place update and collapse as SessionStart: rewriting one "old"
	// entry left any duplicates to report usage repeatedly, and replacing whole
	// entries dropped a user-set matcher or timeout.
	kept := make([]any, 0, len(postToolUse))
	managedFound := 0
	upToDate := false
	placed := false
	for _, item := range postToolUse {
		remainder, found, current := updateManagedCommands(item, hookCommand, "report-usage", postToolUseMatcher, &placed)
		managedFound += found
		if current {
			upToDate = true
		}
		if remainder != nil {
			kept = append(kept, remainder)
		}
	}

	if upToDate && managedFound == 1 {
		return nil
	}

	if !placed {
		kept = append(kept, map[string]any{
			"matcher": postToolUseMatcher,
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": hookCommand,
				},
			},
		})
	}
	postToolUse = kept
	hooks["PostToolUse"] = postToolUse
	if managedFound > 0 {
		log.Info("hook updated", "hook", "PostToolUse", "command", hookCommand, "replaced", managedFound)
	} else {
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
