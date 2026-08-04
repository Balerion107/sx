package mcpserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// handlerReturning builds a MethodHandler that yields the given result.
func handlerReturning(res mcp.Result) mcp.MethodHandler {
	return func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return res, nil
	}
}

func TestPrivateCacheScopeMiddlewareMarksListToolsPrivate(t *testing.T) {
	// The SDK defaults cacheScope to "public", which licenses any client or
	// proxy to serve this response to a different user. An sx server always
	// serves exactly one person's vault, so that default is wrong for us.
	res := &mcp.ListToolsResult{}
	mw := PrivateCacheScopeMiddleware(60 * time.Second)

	got, err := mw(handlerReturning(res))(context.Background(), "tools/list", nil)
	if err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}

	tools, ok := got.(*mcp.ListToolsResult)
	if !ok {
		t.Fatalf("got %T, want *mcp.ListToolsResult", got)
	}
	if tools.CacheScope != "private" {
		t.Errorf("CacheScope = %q, want %q", tools.CacheScope, "private")
	}
	if tools.TTLMs != 60000 {
		t.Errorf("TTLMs = %d, want 60000", tools.TTLMs)
	}
}

func TestPrivateCacheScopeMiddlewareOverridesSDKPublicDefault(t *testing.T) {
	res := &mcp.ListToolsResult{}
	res.CacheScope = "public" // what the SDK fills in on its own

	got, _ := PrivateCacheScopeMiddleware(time.Minute)(handlerReturning(res))(
		context.Background(), "tools/list", nil,
	)

	if scope := got.(*mcp.ListToolsResult).CacheScope; scope != "private" {
		t.Errorf("CacheScope = %q, want the SDK default to be overridden", scope)
	}
}

func TestPrivateCacheScopeMiddlewareCoversEveryCacheableResult(t *testing.T) {
	// Every result type the 2026-07-28 spec marks cacheable must be handled;
	// missing one leaks a "public" hint for that method.
	cases := map[string]mcp.Result{
		"server/discover":          &mcp.DiscoverResult{},
		"tools/list":               &mcp.ListToolsResult{},
		"prompts/list":             &mcp.ListPromptsResult{},
		"resources/list":           &mcp.ListResourcesResult{},
		"resources/templates/list": &mcp.ListResourceTemplatesResult{},
		"resources/read":           &mcp.ReadResourceResult{},
	}

	mw := PrivateCacheScopeMiddleware(time.Minute)
	for method, res := range cases {
		t.Run(method, func(t *testing.T) {
			got, err := mw(handlerReturning(res))(context.Background(), method, nil)
			if err != nil {
				t.Fatalf("middleware returned error: %v", err)
			}
			cacheable, ok := got.(mcp.CacheableResult)
			if !ok {
				t.Fatalf("%T does not implement mcp.CacheableResult", got)
			}
			if cacheable.GetCacheScope() != "private" {
				t.Errorf("CacheScope = %q, want %q", cacheable.GetCacheScope(), "private")
			}
			if cacheable.GetTTLMs() != 60000 {
				t.Errorf("TTLMs = %d, want 60000", cacheable.GetTTLMs())
			}
		})
	}
}

func TestPrivateCacheScopeMiddlewareLeavesNonCacheableResultsAlone(t *testing.T) {
	// tools/call carries no caching hints; the middleware must not panic on
	// or otherwise disturb it.
	res := &mcp.CallToolResult{}

	got, err := PrivateCacheScopeMiddleware(time.Minute)(handlerReturning(res))(
		context.Background(), "tools/call", nil,
	)
	if err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}
	if got != mcp.Result(res) {
		t.Errorf("result was replaced; want it passed through unchanged")
	}
}

func TestPrivateCacheScopeMiddlewarePropagatesErrors(t *testing.T) {
	wantErr := errors.New("handler failed")
	failing := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return nil, wantErr
	}

	_, err := PrivateCacheScopeMiddleware(time.Minute)(failing)(context.Background(), "tools/list", nil)

	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}
