# SX MCP Server Specification

## Overview

sx provides a built-in MCP (Model Context Protocol) server that exposes tools to AI coding assistants. When you run `sx serve`, it starts an MCP server over stdio that provides:

1. **query** - Query integrated services (GitHub, CircleCI, Linear) using natural language

The MCP server is automatically configured when you install sx assets, allowing AI assistants to query external services without additional setup.

> **Note:** Skills are now discovered natively by AI clients (Cursor, Claude Code) from their respective skill directories (`.cursor/skills/`, `.claude/skills/`). The `read_skill` tool is no longer exposed but the code remains available for clients that don't support native skill discovery.

## Starting the MCP Server

```bash
sx serve
```

This starts the MCP server over stdio, ready to accept tool calls from AI clients.

## Built-in Tools

### query

Query integrated services using natural language. This is the primary tool for AI assistants to interact with external development services.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `query` | string | Yes | Natural language query describing what you want to know |
| `integration` | string | Yes | Service to query: `github`, `circleci`, or `linear` |

**Automatic Context Detection:**

The query tool automatically detects git context from the current working directory:
- Repository URL (from git remote)
- Current branch
- Current commit SHA

This context is passed to the query, so you don't need to specify which repository you're asking about.

**Returns:** Plain text response with the query results.

## Query Tool Usage Guide

The query tool is designed for AI assistants to gather information about the current development context. It works best with simple, focused queries.

### Supported Integrations

#### GitHub

Query pull requests, issues, reviews, and repository information.

**Example queries:**
- "Get comments on this PR"
- "List open pull requests"
- "Get review status for PR #123"
- "Show failing checks on this branch"
- "Get issues assigned to me"

#### CircleCI

Query build status, pipeline information, and test results.

**Example queries:**
- "Get failed CI checks"
- "Show recent pipeline runs"
- "What tests failed in the last build?"
- "Get build status for this branch"

#### Linear

Query issues, projects, and sprint information.

**Example queries:**
- "Get my assigned issues"
- "Show issues in the current sprint"
- "List high priority bugs"
- "Get project status"

### Best Practices

**Keep queries atomic:**

```
Good: "Get PR comments"
Bad:  "Get PR comments and also check if CI passed and list any related issues"
```

**Be specific:**

```
Good: "Get review comments on PR #42"
Bad:  "Tell me about the PR"
```

**Let context do the work:**

The tool auto-detects your repository, branch, and commit. Don't include these in your query unless you need to override them:

```
Good: "Get open PRs"  (uses current repo)
Bad:  "Get open PRs for github.com/user/repo on branch main"
```

### Example Tool Calls

**Check PR review status:**

```json
{
  "name": "query",
  "arguments": {
    "query": "Get review comments and approval status",
    "integration": "github"
  }
}
```

**Get CI failures:**

```json
{
  "name": "query",
  "arguments": {
    "query": "What checks failed on this branch?",
    "integration": "circleci"
  }
}
```

**Find related issues:**

```json
{
  "name": "query",
  "arguments": {
    "query": "Get issues related to authentication",
    "integration": "linear"
  }
}
```

## Using MCP Tools in Skills

Skills can leverage MCP tools to enhance their capabilities. When creating skills that use these tools, document the dependency clearly.

### Skill Example: PR Review Helper

```markdown
# PR Review Helper

This skill helps review pull requests by gathering context from GitHub and CI.

## Required Tools

This skill uses:
- `mcp__sx__query` - Query GitHub and CircleCI for PR context

## Workflow

1. First, gather PR context:
   - Use the query tool with GitHub to get PR comments and review status
   - Use the query tool with CircleCI to check build status

2. Then provide review feedback based on the gathered context
```

### Tool Naming Convention

When skills reference MCP tools, they follow the pattern: `mcp__<server>__<tool>`

For sx tools:
- `mcp__sx__query` - Query integrations

### Automatic Dependency Detection

sx automatically detects when skills use MCP tools based on tool call patterns in conversation history. This helps track which skills depend on which MCP capabilities.

## Streaming and Progress

The query tool uses Server-Sent Events (SSE) for streaming responses. During long-running
queries, the tool emits progress events describing what it is doing:

```
event: progress
message: Querying GitHub API...

event: progress
message: Processing 15 comments...

event: complete
message: Query completed
```

On the **stdio** transport (`sx mcp`) these are delivered as MCP
`notifications/progress` on the response stream of the originating request.

On the **relay** transport (`sx cloud serve`) they are not delivered at all. The relay
bridge correlates responses by JSON-RPC id and drops everything else
(`internal/cloud/bridge.go`), and the pulse endpoint answers each request with a single
JSON body rather than a stream — so there is nowhere for a notification to go. Progress
events are logged locally and otherwise dropped on that path.

**Progress is opt-in.** The client asks for it by including a `progressToken` in the
request's `_meta`; without one there is nothing to correlate a notification to, so sx
sends none and logs locally instead. This replaced the previous mechanism, which sent
`notifications/message` (log notifications) — MCP `2026-07-28` deprecated the Logging
feature (SEP-2577) and forbids emitting log notifications for a request that did not
ask for them.

Progress is **not** a keepalive. Events fire on tool-call boundaries, so a query that
spends its time inside a single API call emits nothing even with a token present, and
the relay path emits nothing regardless. The mechanism it replaced (`notifications/message`
via `Session.Log`) was equally conditional — it required the client to have called
`logging/setLevel`. There is no protocol-level keepalive available: `ping` was removed in
`2026-07-28` and the Go SDK rejects it for new-protocol peers.

## Caching hints

Per MCP `2026-07-28` (SEP-2549), list and read results carry caching hints:

- `ttlMs` — how long the client may treat the result as fresh (60s).
- `cacheScope` — always `private`. An sx server serves exactly one person's vault, so
  its results must never be cached across authorization contexts. The Go SDK defaults
  this to `public`; sx overrides it for every cacheable result.

## Error Handling

**Not in a git repository:**

```
Error: not in a git repository
```

The query tool requires git context. Run it from within a git repository.

**Missing required parameters:**

```
Error: query is required
Error: integration is required
```

Both `query` and `integration` parameters must be provided.

**API errors:**

If the underlying service API fails, the error message will include details about what went wrong. Common issues:
- Authentication failures (check API tokens)
- Rate limiting (wait and retry)
- Invalid queries (rephrase the question)

## Configuration

The MCP server uses the vault configuration from `sx init`. For the query tool to work, you must be using the Sleuth vault:

```bash
sx init --type sleuth
```

The query tool is only available with Sleuth vault, as it connects to Sleuth's AI query service which integrates with your configured GitHub, CircleCI, and Linear accounts.

## Architecture

```
┌─────────────────┐     stdio      ┌─────────────────┐
│   AI Client     │◄──────────────►│   sx serve      │
│ (Claude Code)   │                │   MCP Server    │
└─────────────────┘                └────────┬────────┘
                                            │
                                   ┌────────▼────────┐
                                   │     query       │
                                   │     Tool        │
                                   └────────┬────────┘
                                            │
                                   ┌────────▼────────┐
                                   │   Sleuth API    │
                                   │     (SSE)       │
                                   └────────┬────────┘
                                            │
                          ┌─────────────────┼─────────────────┐
                          │                 │                 │
                     ┌────▼───┐       ┌─────▼─────┐     ┌─────▼────┐
                     │ GitHub │       │ CircleCI  │     │  Linear  │
                     └────────┘       └───────────┘     └──────────┘
```

## Future Enhancements

Potential additions for future versions:

- **Additional integrations** - Jira, Slack, PagerDuty
- **Custom tool registration** - Allow skills to register custom MCP tools
- **Tool composition** - Chain multiple tool calls in a single request
- **Caching** - Cache query results for frequently accessed data
