package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	acp "github.com/coder/acp-go-sdk"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

var (
	mcpClients   = map[string]*mcpclient.Client{}
	mcpClientsMu sync.Mutex
)

// getMCPClient returns (or creates) an initialized MCP client for the named server.
func getMCPClient(serverName string) (*mcpclient.Client, error) {
	return getMCPClientForServers(serverName, nil)
}

// getMCPClientForServers returns an initialized client for a server definition
// supplied by the current ACP session. The mock agent can serve multiple ACP
// sessions at the same time. Their Kandev SSE URLs contain session-specific
// routing state, so the cache key must include the endpoint URL.
func getMCPClientForServers(serverName string, sessionServers map[string]mcpServerDef) (*mcpclient.Client, error) {
	mcpClientsMu.Lock()
	defer mcpClientsMu.Unlock()

	srv, ok := sessionServers[serverName]
	if !ok {
		srv, ok = mcpServers[serverName]
	}
	if !ok {
		return nil, fmt.Errorf("unknown MCP server: %s", serverName)
	}
	cacheKey := serverName + "\x00" + srv.URL
	if c, ok := mcpClients[cacheKey]; ok {
		return c, nil
	}

	c, err := mcpclient.NewSSEMCPClient(srv.URL)
	if err != nil {
		return nil, fmt.Errorf("create SSE client for %s: %w", serverName, err)
	}

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		return nil, fmt.Errorf("start MCP client %s: %w", serverName, err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "mock-agent",
		Version: "1.0",
	}

	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize MCP client %s: %w", serverName, err)
	}
	if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("list MCP tools %s: %w", serverName, err)
	}

	mcpClients[cacheKey] = c
	return c, nil
}

// callMCPTool calls a tool on the named MCP server and returns the result text.
func callMCPTool(serverName, toolName string, args map[string]any) (string, error) {
	return callMCPToolCtx(context.Background(), serverName, toolName, args)
}

// callMCPToolCtx calls a tool on the named MCP server with a caller-provided context.
// Use this when the caller needs to impose a timeout on the MCP call.
func callMCPToolCtx(ctx context.Context, serverName, toolName string, args map[string]any) (string, error) {
	return callMCPToolCtxForServers(ctx, nil, serverName, toolName, args)
}

// callMCPToolCtxForServers calls a tool using the current ACP session's MCP
// server definitions. A nil map keeps the command-line configured behavior.
func callMCPToolCtxForServers(ctx context.Context, sessionServers map[string]mcpServerDef, serverName, toolName string, args map[string]any) (string, error) {
	c, err := getMCPClientForServers(serverName, sessionServers)
	if err != nil {
		return "", err
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args

	result, err := c.CallTool(ctx, req)
	if err != nil {
		return "", fmt.Errorf("call tool %s/%s: %w", serverName, toolName, err)
	}

	return extractMCPResultText(result), nil
}

// extractMCPResultText extracts text from an MCP CallToolResult.
func extractMCPResultText(result *mcp.CallToolResult) string {
	var parts []string
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}

// registerACPMcpServers adds SSE MCP servers from an ACP NewSessionRequest to
// the global map and returns a session-owned copy for callers that need to
// preserve the endpoint selected for that ACP session.
func registerACPMcpServers(servers []acp.McpServer) map[string]mcpServerDef {
	registered := make(map[string]mcpServerDef)
	for _, s := range servers {
		if s.Sse != nil && s.Sse.Name != "" && s.Sse.Url != "" {
			registered[s.Sse.Name] = mcpServerDef{URL: s.Sse.Url, Type: "sse"}
		}
	}

	mcpClientsMu.Lock()
	defer mcpClientsMu.Unlock()
	if mcpServers == nil {
		mcpServers = make(map[string]mcpServerDef)
	}
	for name, server := range registered {
		mcpServers[name] = server
		_, _ = fmt.Fprintf(logOutput, "mock-agent: registered ACP MCP server %s at %s\n", name, server.URL)
	}
	return registered
}

func cloneMCPServerDefs(defs map[string]mcpServerDef) map[string]mcpServerDef {
	if len(defs) == 0 {
		return nil
	}
	cloned := make(map[string]mcpServerDef, len(defs))
	for name, def := range defs {
		cloned[name] = def
	}
	return cloned
}

// closeMCPClients closes all open MCP clients (called on shutdown).
func closeMCPClients() {
	mcpClientsMu.Lock()
	defer mcpClientsMu.Unlock()
	for _, c := range mcpClients {
		_ = c.Close()
	}
}
