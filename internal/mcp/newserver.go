package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewMCPServer builds an MCP server configured the way every sx surface needs
// it — currently `sx mcp` over stdio and `sx cloud serve` over the relay.
//
// This exists so the cacheScope middleware can't be omitted. Wiring it at each
// construction site meant a third site added later would ship the SDK's
// "public" default, which is precisely the cross-user cache hint the
// middleware is there to prevent — and the omission would be invisible, since
// every test that builds its own server would still pass.
func NewMCPServer(impl *mcp.Implementation) *mcp.Server {
	server := mcp.NewServer(impl, nil)
	server.AddReceivingMiddleware(PrivateCacheScopeMiddleware(DefaultCacheTTL))
	return server
}
