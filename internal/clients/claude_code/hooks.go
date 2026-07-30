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

// installSessionStartHook installs the SessionStart hook for auto-updating assets
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

	// Collapse to exactly one managed entry rather than rewriting a single "old"
	// one. Several can accumulate — an absolute path goes stale when the app
	// moves, and older versions wrote a bare "sx" — and rewriting one leaves the
	// rest to run the install again on every session start. Byte-equality is
	// checked alongside Managed so a false negative there cannot grow the list
	// without bound.
	kept := make([]any, 0, len(sessionStart))
	managedFound := 0
	upToDate := false
	placed := false
	for _, item := range sessionStart {
		remainder, found, current := stripManagedCommands(item, hookCommand)
		managedFound += found
		if current {
			upToDate = true
		}
		switch {
		case found == 0 || remainder != nil:
			// Untouched, or it also holds the user's own hooks and survives with
			// just our command removed.
			if remainder != nil {
				kept = append(kept, remainder)
			}
		case !placed:
			// This entry held only our command. Reuse it rather than dropping it
			// and appending a fresh one, so a user-set "matcher" — which decides
			// when the hook fires — is preserved instead of silently widened.
			if hookMap, ok := item.(map[string]any); ok {
				hookMap["hooks"] = []any{
					map[string]any{"type": "command", "command": hookCommand},
				}
				kept = append(kept, hookMap)
				placed = true
			}
		default:
			// A further duplicate: drop it.
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
					filtered := removeSxHooks(sessionStart, "install")
					if len(filtered) != len(sessionStart) {
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
					filtered := removeSxHooks(postToolUse, "report-usage")
					if len(filtered) != len(postToolUse) {
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

// stripManagedCommands removes sx's install command from one SessionStart entry.
//
// It returns what should remain of the entry (nil when nothing does), how many
// managed commands were removed, and whether one of them was already the current
// command. Entries can mix sx's hook with the user's own, so operating on the
// commands inside an entry — rather than accepting or rejecting whole entries —
// is what lets a shared entry be upgraded instead of duplicated.
func stripManagedCommands(item any, hookCommand string) (remainder any, found int, current bool) {
	hookMap, ok := item.(map[string]any)
	if !ok {
		return item, 0, false
	}
	hooksArray, ok := hookMap["hooks"].([]any)
	if !ok {
		return item, 0, false
	}

	keptCommands := make([]any, 0, len(hooksArray))
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
		if cmd == hookCommand || clipath.Managed(cmd, "install") {
			found++
			if cmd == hookCommand {
				current = true
			}
			continue
		}
		keptCommands = append(keptCommands, h)
	}

	if found == 0 {
		return item, 0, false
	}
	if len(keptCommands) == 0 {
		return nil, found, current
	}
	hookMap["hooks"] = keptCommands
	return hookMap, found, current
}

// removeSxHooks filters out hook entries that invoke sx with any of the given
// subcommands, in either the legacy bare-"sx" form or the absolute-path form
// current versions write.
func removeSxHooks(hooks []any, subcommands ...string) []any {
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
		removed := false
		for _, h := range hooksArray {
			if hMap, ok := h.(map[string]any); ok {
				if cmd, ok := hMap["command"].(string); ok && clipath.Managed(cmd, subcommands...) {
					removed = true
					continue
				}
			}
			keptCommands = append(keptCommands, h)
		}

		switch {
		case !removed:
			filtered = append(filtered, item)
		case len(keptCommands) > 0:
			hookMap["hooks"] = keptCommands
			filtered = append(filtered, hookMap)
		}
		// Entry held only our commands: drop it.
	}
	return filtered
}

// installUsageReportingHook installs the PostToolUse hook for usage tracking
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

	// Check if our hook already exists (check for both old and new command formats)
	hookExists := false
	var oldHookRef map[string]any
	for _, item := range postToolUse {
		if hookMap, ok := item.(map[string]any); ok {
			if hooksArray, ok := hookMap["hooks"].([]any); ok {
				for _, h := range hooksArray {
					if hMap, ok := h.(map[string]any); ok {
						if cmd, ok := hMap["command"].(string); ok {
							if cmd == hookCommand {
								hookExists = true
								break
							}
							if clipath.Managed(cmd, "report-usage") {
								oldHookRef = hMap // Remember for updating
							}
						}
					}
				}
			}
		}
		if hookExists {
			break
		}
	}

	// Already have exact match, nothing to do
	if hookExists {
		return nil
	}

	// Update old hook if found, otherwise add new
	if oldHookRef != nil {
		oldHookRef["command"] = hookCommand
		log.Info("hook updated", "hook", "PostToolUse", "command", hookCommand)
	} else {
		newHook := map[string]any{
			"matcher": "Skill|Task|SlashCommand|mcp__.*",
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": hookCommand,
				},
			},
		}
		postToolUse = append(postToolUse, newHook)
		hooks["PostToolUse"] = postToolUse
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
