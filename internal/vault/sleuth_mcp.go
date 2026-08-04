package vault

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sleuth-io/sx/v2/internal/bootstrap"
	"github.com/sleuth-io/sx/v2/internal/gitutil"
	"github.com/sleuth-io/sx/v2/internal/logger"
)

// ToolDef represents an MCP tool with its handler
type ToolDef struct {
	Tool    *mcp.Tool
	Handler func(context.Context, *mcp.CallToolRequest, QueryInput) (*mcp.CallToolResult, any, error)
}

// QueryInput is the input type for query tool
type QueryInput struct {
	Query       string `json:"query" jsonschema:"A simple, focused natural language query. Keep queries atomic - ask for one specific thing (e.g., 'Get PR comments', 'Get failed CI checks', 'Get open issues assigned to me'). Avoid complex multi-part queries."`
	Integration string `json:"integration" jsonschema:"which integration to query (github, circleci, linear, or datadog)"`
}

// GetMCPTools returns the query tool for Sleuth vault
func (s *SleuthVault) GetMCPTools() any {
	return []ToolDef{
		{
			Tool: &mcp.Tool{
				Name:        "query",
				Description: "Query integrated services (GitHub, CircleCI, Linear, Datadog) using natural language. Context (repo, branch, commit) is automatically detected from git. For best performance, use simple atomic queries that ask for one specific thing - complex queries take longer and may timeout.",
			},
			Handler: s.handleQueryTool,
		},
	}
}

// handleQueryTool handles the query tool invocation
func (s *SleuthVault) handleQueryTool(ctx context.Context, req *mcp.CallToolRequest, input QueryInput) (*mcp.CallToolResult, any, error) {
	log := logger.Get()
	log.Debug("query tool invoked", "query", input.Query, "integration", input.Integration, "sleuth_url", s.serverURL)

	if input.Query == "" {
		return nil, nil, errors.New("query is required")
	}
	if input.Integration == "" {
		return nil, nil, errors.New("integration is required")
	}

	// Detect git context using existing gitutil
	gitCtx, err := gitutil.DetectContext(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to detect git context: %w", err)
	}

	if !gitCtx.IsRepo {
		return nil, nil, errors.New("not in a git repository")
	}

	// Get current branch
	branch, err := gitutil.GetCurrentBranch(ctx, gitCtx.RepoRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get current branch: %w", err)
	}

	// Get current commit
	commit, err := gitutil.GetCurrentCommit(ctx, gitCtx.RepoRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get current commit: %w", err)
	}

	// Build context for API call (matches AiQueryContextInput schema)
	apiContext := map[string]any{
		"repoUrl":   gitCtx.RepoURL,
		"branch":    branch,
		"commitSha": commit,
	}

	log.Debug("calling sleuth query API with SSE streaming", "repoUrl", gitCtx.RepoURL, "branch", branch, "commit", commit)

	// Create event callback that streams progress back to the client.
	//
	// This used to send `notifications/message` via Session.Log. MCP
	// 2026-07-28 deprecated the Logging feature (SEP-2577) and — more
	// pointedly — forbids emitting log notifications for a request that didn't
	// ask for them. Progress notifications are the request-scoped mechanism
	// that survived, and they carry the same "still working" signal.
	//
	// Progress is opt-in: the client requests it with a progressToken in the
	// request's `_meta`. Without one there's nothing to correlate a
	// notification to, so we log locally and send nothing over the wire.
	//
	// Note this is *not* a guaranteed keepalive, and the code it replaced
	// wasn't either — Session.Log returned early unless the client had called
	// logging/setLevel. Two gaps remain: a client that sends no progressToken
	// gets no traffic at all, and QueryIntegrationStream only invokes onEvent
	// on a ToolCallEvent, so a query that spends its time inside one API call
	// emits nothing even with a token.
	//
	// No protocol-level keepalive is available for new-protocol peers: `ping`
	// was removed in 2026-07-28 and the SDK rejects it for them
	// (server.go:1880). `ServerOptions.KeepAlive` does still work for legacy
	// peers, but it's a per-server setting and would tear down modern
	// sessions, so sx doesn't enable it. If idle timeouts become a real
	// problem the fix belongs in the streaming API — periodic heartbeat
	// events — not in this callback.
	progressToken := req.Params.GetProgressToken()
	var progressSeq float64
	onEvent := func(eventType, content string) {
		log.Debug("query progress", "type", eventType, "content", content)

		if progressToken == nil {
			return
		}
		progressSeq++
		_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: progressToken,
			// Total stays unset: the API streams until it's done, so progress
			// climbs without ever forming a completion ratio. The spec allows
			// exactly this ("should increase every time progress is made, even
			// if the total is unknown").
			Progress: progressSeq,
			Message:  fmt.Sprintf("%s: %s", eventType, content),
		})
	}

	// Call Sleuth API with SSE streaming
	result, err := s.QueryIntegrationStream(ctx, input.Query, input.Integration, apiContext, onEvent)
	if err != nil {
		log.Warn("sleuth query API failed", "error", err)
		return nil, nil, fmt.Errorf("sleuth query failed: %w", err)
	}

	log.Debug("sleuth query API succeeded", "result_length", len(result))

	// Return result as plain text
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: result},
		},
	}, nil, nil
}

// GetBootstrapOptions returns bootstrap options for the Sleuth vault.
// This includes the Sleuth AI Query MCP server.
func (s *SleuthVault) GetBootstrapOptions(ctx context.Context) []bootstrap.Option {
	return []bootstrap.Option{bootstrap.SleuthAIQueryMCP()}
}
