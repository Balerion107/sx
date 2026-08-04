package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP 2026-07-28 (SEP-2549) requires servers to attach caching hints to list
// and read results. The Go SDK fills these in for us, but it defaults
// cacheScope to "public" — and "public" is a licence for any client, gateway,
// or caching proxy to serve the response to a *different* user, even though it
// came from an authenticated endpoint.
//
// That default is wrong for us. An sx server — whether it's `sx mcp` over
// stdio or a `sx cloud serve` relay — serves exactly one person's vault, and
// what it exposes varies with the vault behind it (SleuthVault publishes a
// bespoke `query` tool; PathVault and GitVault publish the asset-shim
// toolset). There is also nothing to gain from "public": with one user per
// server, a shared cache has no second user to serve.
const cacheScopePrivate = "private"

// DefaultCacheTTL is how long clients may treat a list result as fresh.
// Deliberately short: an asset added to the vault should show up in the chat
// client within a minute, not on the next restart.
const DefaultCacheTTL = 60 * time.Second

// PrivateCacheScopeMiddleware marks every cacheable result private and gives
// it a TTL, overriding the SDK's "public" default.
//
// Applied as receiving middleware rather than by wrapping each handler so it
// can't be forgotten when a new tool or vault type is added — every cacheable
// result on its way out of the server passes through here.
func PrivateCacheScopeMiddleware(ttl time.Duration) mcp.Middleware {
	ttlMs := int(ttl / time.Millisecond)
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if err != nil {
				return res, err
			}
			applyPrivateCacheHints(res, ttlMs)
			return res, nil
		}
	}
}

// applyPrivateCacheHints sets the caching fields on any result type that
// carries them. The fields live on an embedded `mcp.Cacheable` value, so there
// is no interface to set them through — a type switch is the available seam.
func applyPrivateCacheHints(res mcp.Result, ttlMs int) {
	switch r := res.(type) {
	case *mcp.ListToolsResult:
		r.CacheScope, r.TTLMs = cacheScopePrivate, ttlMs
	case *mcp.DiscoverResult:
		r.CacheScope, r.TTLMs = cacheScopePrivate, ttlMs
	case *mcp.ListPromptsResult:
		r.CacheScope, r.TTLMs = cacheScopePrivate, ttlMs
	case *mcp.ListResourcesResult:
		r.CacheScope, r.TTLMs = cacheScopePrivate, ttlMs
	case *mcp.ListResourceTemplatesResult:
		r.CacheScope, r.TTLMs = cacheScopePrivate, ttlMs
	case *mcp.ReadResourceResult:
		r.CacheScope, r.TTLMs = cacheScopePrivate, ttlMs
	}
}
