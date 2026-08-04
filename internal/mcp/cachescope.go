package mcpserver

import (
	"context"
	"fmt"
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

// Compile-time pin on the fields setCacheHints writes through. The reflection
// below exists for the *dynamic* half of the problem — which result types are
// cacheable — but the shape of mcp.Cacheable is knowable right here, and a
// rename, removal, or type change should fail `go build` rather than degrade
// into a runtime log line. That matters because the runtime signal is weak:
// it lands in a 1MB rotating debug file alongside every Debug line in the
// process, so the practical outcome of an unnoticed SDK bump would be
// `cacheScope: "public"` on authenticated per-user results with nothing
// visible to an operator or to CI.
var (
	_ string = mcp.Cacheable{}.CacheScope
	_ int    = mcp.Cacheable{}.TTLMs
)

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
	if reason := setCacheHints(res, ttlMs); reason != "" {
		// Never silent: a result going out without our hints may carry the
		// SDK's "public" default, which is shareable across authorization
		// contexts — the exact failure this file prevents.
		logger.Get().Error(
			"cache hints not applied; result may carry the SDK's public cacheScope default",
			"reason", reason,
			"type", reflect.TypeOf(res).String(),
		)
	}
}

// setCacheHints writes the private cacheScope and TTL onto a result's embedded
// mcp.Cacheable. Returns "" on success, or the reason it gave up.
//
// Takes `any` rather than mcp.Result so the give-up branches are reachable from
// a test: mcp.Result has an unexported method, so no fake outside the SDK can
// implement it, and the SDK ships no wrong-shaped type to feed in. Without this
// seam every guard below would be untestable — and an untestable guard is one
// that can be deleted without anything failing.
//
// The per-step checks below are fast, specific diagnostics, but they are an
// enumeration and enumerations of "how could reflect panic" don't close.
// FieldByName alone has a second mode the Kind checks miss — a field promoted
// through a nil embedded pointer panics via FieldByIndex — and the next SDK
// shape could introduce a third. The deferred recover is the part that is
// actually total: any reflect panic, known or not, becomes a logged give-up
// instead of a dead request.
func setCacheHints(target any, ttlMs int) (reason string) {
	defer func() {
		if r := recover(); r != nil {
			reason = fmt.Sprintf("reflect panic: %v", r)
		}
	}()

	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return "result is not a non-nil pointer"
	}
	elem := value.Elem()
	if elem.Kind() != reflect.Struct {
		return "result does not point at a struct"
	}
	cacheable := elem.FieldByName(cacheableFieldName)
	if !cacheable.IsValid() || !cacheable.CanSet() {
		return "no settable Cacheable field"
	}
	if cacheable.Kind() != reflect.Struct {
		return "Cacheable field is not a struct"
	}

	scope := cacheable.FieldByName("CacheScope")
	ttl := cacheable.FieldByName("TTLMs")
	if !scope.CanSet() || scope.Kind() != reflect.String ||
		!ttl.CanSet() || !isIntKind(ttl.Kind()) {
		return "unexpected CacheScope/TTLMs shape"
	}
	scope.SetString(cacheScopePrivate)
	ttl.SetInt(int64(ttlMs))
	return ""
}

func isIntKind(k reflect.Kind) bool {
	return k == reflect.Int || k == reflect.Int8 || k == reflect.Int16 ||
		k == reflect.Int32 || k == reflect.Int64
}
