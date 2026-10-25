// Tiny stdio MCP server for anneal's MCP e2e gate: one echo tool.
package main

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	s := server.NewMCPServer("anneal-demo", "0.1.0")
	tool := mcp.NewTool("echo",
		mcp.WithDescription("Echo text back, uppercased."),
		mcp.WithString("text", mcp.Required(), mcp.Description("text to echo")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		text, _ := req.RequireString("text")
		return mcp.NewToolResultText(fmt.Sprintf("ECHO(%s)", text)), nil
	})
	if err := server.ServeStdio(s); err != nil {
		fmt.Println("serve:", err)
	}
}
