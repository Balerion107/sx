package mcpserver

import (
	"context"
	"errors"
	"reflect"
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

// TestNewMCPServerInstallsCacheScopeMiddleware covers the production wiring
// rather than a hand-built server: deleting the AddReceivingMiddleware call in
// NewMCPServer must fail something. Both `sx mcp` and `sx cloud serve` go
// through this constructor, so this one test covers both.
func TestNewMCPServerInstallsCacheScopeMiddleware(t *testing.T) {
	ctx := context.Background()
	server := NewMCPServer(&mcp.Implementation{Name: "test", Version: "0.1"})
	mcp.AddTool(server, &mcp.Tool{Name: "probe", Description: "probe"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() { _ = server.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	if res.CacheScope != "private" {
		t.Errorf("CacheScope = %q, want %q — is the middleware still wired into NewMCPServer?", res.CacheScope, "private")
	}
	if res.TTLMs != int(DefaultCacheTTL/time.Millisecond) {
		t.Errorf("TTLMs = %d, want %d", res.TTLMs, int(DefaultCacheTTL/time.Millisecond))
	}
}

// TestApplyPrivateCacheHintsSurvivesUnexpectedShape guards the reflective path
// against the scenario it exists for: an SDK whose Cacheable fields change type
// or disappear. Reflection's SetString/SetInt panic on a kind mismatch, so a
// naive implementation would take down the request rather than degrade.
func TestApplyPrivateCacheHintsSurvivesUnexpectedShape(t *testing.T) {
	// A result whose Cacheable-shaped fields have the wrong kinds. Not an
	// mcp.CacheableResult, so it short-circuits before reflection — the point
	// is that neither branch panics.
	cases := []struct {
		name string
		res  mcp.Result
	}{
		{"nil result", nil},
		{"non-cacheable result", &mcp.CallToolResult{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panicked on %s: %v", tc.name, r)
				}
			}()
			applyPrivateCacheHints(tc.res, 60000)
		})
	}
}

func TestIsIntKindAcceptsOnlySignedInts(t *testing.T) {
	// TTLMs is an int today; if the SDK widens or narrows it we still want to
	// write it, but a change to string or float must fall through to the
	// warning rather than panic.
	for _, k := range []reflect.Kind{reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64} {
		if !isIntKind(k) {
			t.Errorf("isIntKind(%v) = false, want true", k)
		}
	}
	for _, k := range []reflect.Kind{reflect.String, reflect.Float64, reflect.Bool, reflect.Uint} {
		if isIntKind(k) {
			t.Errorf("isIntKind(%v) = true, want false", k)
		}
	}
}
