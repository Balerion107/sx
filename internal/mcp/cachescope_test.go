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
	// Checks the reflective path resolves correctly against every cacheable
	// type the SDK ships today — something the goodShape fake can't prove.
	// It is no longer an exhaustiveness check: applyPrivateCacheHints covers
	// anything satisfying mcp.CacheableResult, so a seventh type in a future
	// SDK is handled whether or not this list mentions it.
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

// The shapes below stand in for SDK changes this code must survive. They can't
// be fed through applyPrivateCacheHints — mcp.Result has an unexported method,
// so nothing outside the SDK can implement it — which is why setCacheHints
// takes `any`.
type goodShape struct {
	Cacheable struct {
		CacheScope string
		TTLMs      int
	}
}

type wrongTTLKind struct {
	Cacheable struct {
		CacheScope string
		TTLMs      string
	}
}

type wrongScopeKind struct {
	Cacheable struct {
		CacheScope int
		TTLMs      int
	}
}

type pointerCacheable struct {
	Cacheable *struct {
		CacheScope string
		TTLMs      int
	}
}

type noCacheableField struct {
	Other string
}

// embedsCacheable is embedded by pointer below, so Cacheable is promoted
// through it — FieldByName then delegates to FieldByIndex, which panics on the
// nil pointer. This is the panic mode a Kind() == Struct check does not cover.
type embedsCacheable struct {
	Cacheable struct {
		CacheScope string
		TTLMs      int
	}
}

type nilEmbeddedPointer struct {
	*embedsCacheable
}

// TestSetCacheHintsWritesTheHints is the happy path through the reflection.
func TestSetCacheHintsWritesTheHints(t *testing.T) {
	target := &goodShape{}

	if reason := setCacheHints(target, 60000); reason != "" {
		t.Fatalf("gave up unexpectedly: %s", reason)
	}
	if target.Cacheable.CacheScope != "private" || target.Cacheable.TTLMs != 60000 {
		t.Errorf("got (%q, %d), want (\"private\", 60000)", target.Cacheable.CacheScope, target.Cacheable.TTLMs)
	}
}

// TestSetCacheHintsGivesUpWithoutPanicking covers every guard.
//
// Asserting the *specific* reason, not merely "some reason", is what keeps the
// per-step checks load-bearing. The deferred recover would otherwise catch all
// of these itself, so deleting a guard would still produce a non-empty reason
// and this test would pass — the guards would quietly become decoration.
// Pinning the reason means a deleted guard surfaces as "reflect panic: …"
// instead of its specific diagnostic, and fails here.
func TestSetCacheHintsGivesUpWithoutPanicking(t *testing.T) {
	cases := []struct {
		name       string
		target     any
		wantReason string
	}{
		{"nil", nil, "result is not a non-nil pointer"},
		{"non-pointer", goodShape{}, "result is not a non-nil pointer"},
		{"typed nil pointer", (*goodShape)(nil), "result is not a non-nil pointer"},
		{"pointer to non-struct", new(int), "result does not point at a struct"},
		{"no Cacheable field", &noCacheableField{}, "no settable Cacheable field"},
		{"Cacheable is a pointer", &pointerCacheable{}, "Cacheable field is not a struct"},
		{"TTLMs is not an int", &wrongTTLKind{}, "unexpected CacheScope/TTLMs shape"},
		{"CacheScope is not a string", &wrongScopeKind{}, "unexpected CacheScope/TTLMs shape"},
		// The one case the recover is genuinely for: no per-step check can see
		// it coming, because FieldByName only panics once it walks the index.
		{
			"Cacheable promoted through a nil embedded pointer",
			&nilEmbeddedPointer{},
			"reflect panic: reflect: indirection through nil pointer to embedded struct",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panicked: %v", r)
				}
			}()
			if reason := setCacheHints(tc.target, 60000); reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

// TestApplyPrivateCacheHintsIgnoresNonCacheableResults keeps tools/call — which
// carries no caching hints — passing through untouched.
func TestApplyPrivateCacheHintsIgnoresNonCacheableResults(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked: %v", r)
		}
	}()
	applyPrivateCacheHints(&mcp.CallToolResult{}, 60000)
	applyPrivateCacheHints(nil, 60000)
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
