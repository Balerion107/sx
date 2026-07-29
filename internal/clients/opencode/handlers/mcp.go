package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/sleuth-io/sx/v2/internal/asset"
	"github.com/sleuth-io/sx/v2/internal/bootstrap"
	"github.com/sleuth-io/sx/v2/internal/handlers/dirasset"
	"github.com/sleuth-io/sx/v2/internal/metadata"
	"github.com/sleuth-io/sx/v2/internal/utils"
)

// commandArray builds the [command, ...args] form OpenCode expects under
// the `command` key of a local MCP entry. Centralized so packaged,
// config-only, and bootstrap MCP entries always emit the same shape.
func commandArray(command string, args []string) []any {
	arr := make([]any, 0, 1+len(args))
	arr = append(arr, command)
	for _, a := range args {
		arr = append(arr, a)
	}
	return arr
}

var mcpOps = dirasset.NewOperations(DirMCPServers, &asset.TypeMCP)

// MCPHandler installs MCP server entries into OpenCode's opencode.json.
// Packaged servers extract files into <targetBase>/mcp-servers/<name>/ and
// register an absolute-path command; config-only servers only modify the
// JSON config.
type MCPHandler struct {
	metadata *metadata.Metadata
}

// NewMCPHandler creates a new MCP handler.
func NewMCPHandler(meta *metadata.Metadata) *MCPHandler {
	return &MCPHandler{metadata: meta}
}

// Install registers the MCP server in opencode.json, extracting files first
// for packaged servers.
func (h *MCPHandler) Install(ctx context.Context, zipData []byte, targetBase string) error {
	hasContent, err := utils.HasContentFiles(zipData)
	if err != nil {
		return fmt.Errorf("failed to inspect zip contents: %w", err)
	}

	var entry map[string]any
	if hasContent {
		serverDir := filepath.Join(targetBase, DirMCPServers, h.metadata.Asset.Name)
		if err := utils.ExtractZip(zipData, serverDir); err != nil {
			return fmt.Errorf("failed to extract MCP server: %w", err)
		}
		entry = h.generatePackagedMCPEntry(serverDir)
	} else {
		entry = h.generateConfigOnlyMCPEntry()
	}

	configPath := ConfigFilePath(targetBase)
	return AddMCPServer(configPath, h.metadata.Asset.Name, entry)
}

// Remove removes the MCP entry from opencode.json and cleans up any extracted
// packaged-server files.
func (h *MCPHandler) Remove(ctx context.Context, targetBase string) error {
	configPath := ConfigFilePath(targetBase)
	if err := RemoveMCPServer(configPath, h.metadata.Asset.Name); err != nil {
		return err
	}

	serverDir := filepath.Join(targetBase, DirMCPServers, h.metadata.Asset.Name)
	// Best-effort cleanup of extracted files: the MCP entry is already
	// out of opencode.json, so a stale dir is cosmetic, not functional.
	_ = os.RemoveAll(serverDir)

	return nil
}

// VerifyInstalled reports whether the MCP server is registered (and, for
// packaged servers, whether the extracted directory is on disk).
func (h *MCPHandler) VerifyInstalled(targetBase string) (bool, string) {
	installDir := filepath.Join(targetBase, DirMCPServers, h.metadata.Asset.Name)
	if utils.IsDirectory(installDir) {
		return mcpOps.VerifyInstalled(targetBase, h.metadata.Asset.Name, h.metadata.Asset.Version)
	}

	configPath := ConfigFilePath(targetBase)
	config, err := ReadOpenCodeConfig(configPath)
	if err != nil {
		return false, "failed to read opencode.json: " + err.Error()
	}
	if _, exists := config.MCP[h.metadata.Asset.Name]; !exists {
		return false, "MCP server not registered"
	}
	return true, "installed"
}

func (h *MCPHandler) generatePackagedMCPEntry(serverDir string) map[string]any {
	mcpConfig := h.metadata.MCP

	command := utils.ResolveCommand(mcpConfig.Command, serverDir)
	args := utils.ResolveArgs(mcpConfig.Args, serverDir)

	entry := map[string]any{
		"type":    "local",
		"enabled": true,
		"command": commandArray(command, args),
	}

	if len(mcpConfig.Env) > 0 {
		entry["environment"] = mcpConfig.Env
	}

	return entry
}

func (h *MCPHandler) generateConfigOnlyMCPEntry() map[string]any {
	mcpConfig := h.metadata.MCP

	if mcpConfig.IsRemote() {
		// OpenCode's remote MCP shape only carries `url` and `headers`; it
		// has no `environment` field. sx's metadata only exposes Env, which
		// has no analogue here, so any env vars set on a remote MCP are
		// dropped rather than silently written into an unrecognized field.
		return map[string]any{
			"type":    "remote",
			"enabled": true,
			"url":     mcpConfig.URL,
		}
	}

	entry := map[string]any{
		"type":    "local",
		"enabled": true,
		"command": commandArray(mcpConfig.Command, mcpConfig.Args),
	}

	if len(mcpConfig.Env) > 0 {
		entry["environment"] = mcpConfig.Env
	}

	return entry
}

// OpenCodeConfig is a minimal view of opencode.json that we care about.
// Unknown top-level fields are preserved in Other so writes don't drop them.
// WriteOpenCodeConfig also injects a `$schema` reference if the config
// doesn't already have one, so newly-materialized files are valid against
// the OpenCode JSON schema out of the box.
type OpenCodeConfig struct {
	MCP          map[string]any
	Instructions []string
	Other        map[string]any
}

// ReadOpenCodeConfig reads opencode.json (or returns an empty config if the
// file doesn't exist yet). Supports JSONC. If the file is present but the
// top-level `mcp` or `instructions` keys carry the wrong JSON type (e.g.
// a string where an object/array is expected), this returns a typed error
// rather than silently dropping the value — otherwise the next
// WriteOpenCodeConfig would clobber the user's data without warning. A
// JSON null at either key is treated as absent (the JSON convention).
func ReadOpenCodeConfig(path string) (*OpenCodeConfig, error) {
	config := &OpenCodeConfig{
		MCP:   make(map[string]any),
		Other: make(map[string]any),
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return config, nil
		}
		return nil, err
	}

	var raw map[string]any
	if err := utils.UnmarshalJSONC(data, &raw); err != nil {
		return nil, err
	}

	if rawMCP, present := raw["mcp"]; present && rawMCP != nil {
		servers, ok := rawMCP.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("opencode.json: `mcp` must be an object, got %T", rawMCP)
		}
		config.MCP = servers
	}

	if rawInstructions, present := raw["instructions"]; present && rawInstructions != nil {
		arr, ok := rawInstructions.([]any)
		if !ok {
			return nil, fmt.Errorf("opencode.json: `instructions` must be an array, got %T", rawInstructions)
		}
		for i, v := range arr {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("opencode.json: `instructions[%d]` must be a string, got %T", i, v)
			}
			config.Instructions = append(config.Instructions, s)
		}
	}

	for k, v := range raw {
		if k != "mcp" && k != "instructions" {
			config.Other[k] = v
		}
	}

	return config, nil
}

// WriteOpenCodeConfig writes opencode.json, preserving unknown fields and
// adding the $schema reference if it isn't already present. Top-level keys
// are written in a stable order — $schema first, then instructions, then
// mcp, then any remaining preserved keys sorted alphabetically — so
// repeated installs/uninstalls don't churn the file's git diff.
func WriteOpenCodeConfig(path string, config *OpenCodeConfig) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	type kv struct {
		key string
		val any
	}
	var pairs []kv

	schema, hasSchema := config.Other["$schema"]
	if !hasSchema {
		schema = "https://opencode.ai/config.json"
	}
	pairs = append(pairs, kv{"$schema", schema})

	if len(config.Instructions) > 0 {
		pairs = append(pairs, kv{"instructions", config.Instructions})
	}
	if len(config.MCP) > 0 {
		pairs = append(pairs, kv{"mcp", config.MCP})
	}

	otherKeys := make([]string, 0, len(config.Other))
	for k := range config.Other {
		if k == "$schema" {
			continue
		}
		otherKeys = append(otherKeys, k)
	}
	sort.Strings(otherKeys)
	for _, k := range otherKeys {
		pairs = append(pairs, kv{k, config.Other[k]})
	}

	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, p := range pairs {
		keyBytes, err := json.Marshal(p.key)
		if err != nil {
			return err
		}
		valBytes, err := json.MarshalIndent(p.val, "  ", "  ")
		if err != nil {
			return err
		}
		buf.WriteString("  ")
		buf.Write(keyBytes)
		buf.WriteString(": ")
		buf.Write(valBytes)
		if i < len(pairs)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("}")

	return os.WriteFile(path, buf.Bytes(), 0644)
}

// AddMCPServer adds or updates an entry in opencode.json under the mcp key.
func AddMCPServer(configPath, name string, entry map[string]any) error {
	config, err := ReadOpenCodeConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to read opencode.json: %w", err)
	}

	if config.MCP == nil {
		config.MCP = make(map[string]any)
	}
	config.MCP[name] = entry

	if err := WriteOpenCodeConfig(configPath, config); err != nil {
		return fmt.Errorf("failed to write opencode.json: %w", err)
	}
	return nil
}

// MCPServerEntryFromBootstrap renders a bootstrap.MCPServerConfig into the
// JSON shape OpenCode expects under the `mcp.<name>` key. Bootstrap MCPs are
// always local processes (bootstrap doesn't model remote URLs).
func MCPServerEntryFromBootstrap(cfg *bootstrap.MCPServerConfig) map[string]any {
	entry := map[string]any{
		"type":    "local",
		"enabled": true,
		"command": commandArray(cfg.Command, cfg.Args),
	}
	if len(cfg.Env) > 0 {
		entry["environment"] = cfg.Env
	}
	return entry
}

// RemoveMCPServer removes the named entry from opencode.json. If the config
// file doesn't exist or the entry isn't there, this is a no-op and the file
// is left untouched — so a stray uninstall doesn't materialize a config or
// rewrite an existing one just to inject $schema / reorder keys.
func RemoveMCPServer(configPath, name string) error {
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil
	}

	config, err := ReadOpenCodeConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to read opencode.json: %w", err)
	}

	if _, ok := config.MCP[name]; !ok {
		return nil
	}
	delete(config.MCP, name)

	if err := WriteOpenCodeConfig(configPath, config); err != nil {
		return fmt.Errorf("failed to write opencode.json: %w", err)
	}
	return nil
}
