package mcpserver

import (
	"context"
	"reflect"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sleuth-io/sx/v2/internal/logger"
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

// cacheableFieldName is the embedded struct the SDK uses to carry caching
// hints. Every cacheable result type embeds it by value.
const cacheableFieldName = "Cacheable"

// PrivateCacheScopeMiddleware marks every cacheable result private and gives
// it a TTL, overriding the SDK's "public" default.
//
// Applied as receiving middleware rather than by wrapping each handler so it
// can't be forgotten when a new tool or vault type is added — every cacheable
// result on its way out of the server passes through here.
//
// This middleware owns TTLMs as well as CacheScope: it overwrites whatever the
// handler set. No handler sets one today, and a single owner is what keeps the
// freshness hint consistent across every method. A handler that needs its own
// TTL should stop routing through here rather than fight it.
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

// applyPrivateCacheHints sets the caching fields on any result that carries
// them.
//
// Deliberately reflective rather than a type switch over the six cacheable
// types the SDK defines today. A switch fails *open*: a cacheable type added by
// a future SDK bump would fall through and keep the "public" default, silently
// undoing the control this whole file exists to provide, and an enumerated test
// couldn't catch the gap either. Going through mcp.CacheableResult means any
// type the SDK declares cacheable is covered the moment it exists.
func applyPrivateCacheHints(res mcp.Result, ttlMs int) {
	if _, ok := res.(mcp.CacheableResult); !ok {
		return
	}

	// Results are always pointers to structs embedding mcp.Cacheable by value.
	value := reflect.ValueOf(res)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return
	}
	cacheable := value.Elem().FieldByName(cacheableFieldName)
	if !cacheable.IsValid() || !cacheable.CanSet() {
		// The SDK changed shape underneath us. Say so loudly: the alternative
		// is shipping "public" on an authenticated per-user result.
		logger.Get().Error(
			"cannot set cache hints on result; it may be served with the SDK's public default",
			"type", reflect.TypeOf(res).String(),
		)
		return
	}

	// Kinds are checked, not just settability: SetString and SetInt panic on a
	// mismatch, and an SDK field-type change is precisely the scenario this
	// reflective path exists to survive. Panicking here would take down the
	// request instead of degrading to a logged warning.
	scope := cacheable.FieldByName("CacheScope")
	ttl := cacheable.FieldByName("TTLMs")
	if !scope.CanSet() || scope.Kind() != reflect.String ||
		!ttl.CanSet() || !isIntKind(ttl.Kind()) {
		logger.Get().Error(
			"unexpected mcp.Cacheable shape; cache hints not applied, result may carry the SDK's public default",
			"type", reflect.TypeOf(res).String(),
		)
		return
	}
	scope.SetString(cacheScopePrivate)
	ttl.SetInt(int64(ttlMs))
}

func isIntKind(k reflect.Kind) bool {
	return k == reflect.Int || k == reflect.Int8 || k == reflect.Int16 ||
		k == reflect.Int32 || k == reflect.Int64
}
