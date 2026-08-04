package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	mcpserver "github.com/sleuth-io/sx/v2/internal/mcp"
)

// roundTripEnvelope pushes one MCP request envelope down the relay WebSocket
// and returns the JSON-RPC body sx sends back, exercising the full path pulse
// uses: envelope -> in-memory transport -> SDK server -> envelope.
func roundTripEnvelope(t *testing.T, method string, params map[string]any) map[string]any {
	t.Helper()

	type outbound struct {
		Type      string         `json:"type"`
		RequestID string         `json:"request_id"`
		Body      map[string]any `json:"body"`
	}

	respCh := make(chan outbound, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()

		envBytes, err := json.Marshal(map[string]any{
			"type":       "mcp-request",
			"request_id": "req-1",
			"jsonrpc_id": 7,
			"method":     method,
			"params":     params,
		})
		if err != nil {
			t.Errorf("marshal envelope: %v", err)
			return
		}

		writeCtx, writeCancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer writeCancel()
		if err := ws.Write(writeCtx, websocket.MessageText, envBytes); err != nil {
			t.Errorf("write req: %v", err)
			return
		}

		readCtx, readCancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer readCancel()
		_, data, err := ws.Read(readCtx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("read resp: %v", err)
			}
			return
		}
		var out outbound
		if err := json.Unmarshal(data, &out); err != nil {
			t.Errorf("unmarshal resp: %v", err)
			return
		}
		respCh <- out
	}))
	defer srv.Close()

	// Built through the same constructor `sx cloud serve` uses, so this
	// exercises the real wiring rather than a hand-rolled equivalent.
	factory := func() (*mcp.Server, error) {
		s := mcpserver.NewMCPServer(&mcp.Implementation{Name: "test-sx", Version: "0.1"})
		mcp.AddTool(s, &mcp.Tool{Name: "probe", Description: "probe"},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
			})
		return s, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- Serve(ctx, ServeOptions{
			Credential: &Credential{
				RelayBaseURL: strings.TrimSuffix(srv.URL, "/") + "/relay/SRtest/",
				RelayGID:     "SRtest",
				MachineToken: "tok",
			},
			MCPServerFactory: factory,
		})
	}()

	var body map[string]any
	select {
	case out := <-respCh:
		body = out.Body
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for MCP response envelope")
	}

	cancel()
	if err := <-serveErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("Serve returned non-cancel error: %v", err)
	}
	return body
}

// modernParams builds the per-request `_meta` block the stateless revision
// requires. Pulse forwards `params` verbatim, so this is exactly what sx sees.
func modernParams(extra map[string]any) map[string]any {
	params := map[string]any{}
	maps.Copy(params, extra)
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		"io.modelcontextprotocol/clientInfo": map[string]any{
			"name":    "test-client",
			"version": "1.0.0",
		},
	}
	return params
}

func resultOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	if errVal, has := body["error"]; has {
		t.Fatalf("unexpected JSON-RPC error: %v", errVal)
	}
	result, ok := body["result"].(map[string]any)
	if !ok {
		t.Fatalf("response has no result object: %+v", body)
	}
	return result
}

// TestServe_ServerDiscoverCrossesTheRelay is the load-bearing case for the
// stateless revision: `server/discover` replaces `initialize`, and it has to
// survive the pulse -> WebSocket -> sx round trip intact.
func TestServe_ServerDiscoverCrossesTheRelay(t *testing.T) {
	result := resultOf(t, roundTripEnvelope(t, "server/discover", modernParams(nil)))

	versions, ok := result["supportedVersions"].([]any)
	if !ok || len(versions) == 0 {
		t.Fatalf("server/discover returned no supportedVersions: %+v", result)
	}
	found := false
	for _, v := range versions {
		if v == "2026-07-28" {
			found = true
		}
	}
	if !found {
		t.Errorf("supportedVersions %v does not include 2026-07-28", versions)
	}
}

func TestServe_ModernToolsListIsPrivatelyScoped(t *testing.T) {
	// The relay serves one user's vault. A "public" hint would let a client or
	// proxy hand this response to somebody else.
	result := resultOf(t, roundTripEnvelope(t, "tools/list", modernParams(nil)))

	if scope := result["cacheScope"]; scope != "private" {
		t.Errorf("cacheScope = %v, want \"private\"", scope)
	}
	if ttl, ok := result["ttlMs"].(float64); !ok || ttl <= 0 {
		t.Errorf("ttlMs = %v, want a positive freshness hint", result["ttlMs"])
	}
}

func TestServe_ModernResultsCarryResultType(t *testing.T) {
	result := resultOf(t, roundTripEnvelope(t, "tools/list", modernParams(nil)))

	if rt := result["resultType"]; rt != "complete" {
		t.Errorf("resultType = %v, want \"complete\"", rt)
	}
}

func TestServe_ModernToolsCallCrossesTheRelay(t *testing.T) {
	body := roundTripEnvelope(t, "tools/call", modernParams(map[string]any{
		"name":      "probe",
		"arguments": map[string]any{},
	}))

	result := resultOf(t, body)
	if result["resultType"] != "complete" {
		t.Errorf("resultType = %v, want \"complete\"", result["resultType"])
	}
}

// TestServe_LegacyInitializeStillWorks guards the compatibility half: a chat
// client that hasn't migrated still opens with `initialize`.
func TestServe_LegacyInitializeStillWorks(t *testing.T) {
	result := resultOf(t, roundTripEnvelope(t, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "old-client", "version": "0"},
	}))

	if _, ok := result["serverInfo"]; !ok {
		t.Errorf("legacy initialize returned no serverInfo: %+v", result)
	}
}
