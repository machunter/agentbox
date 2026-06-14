package mcpfs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestServerRoundtrip exercises the full MCP protocol in-process: a client
// connects to the server over an in-memory transport, lists tools, and calls
// read_file. No subprocess, no network — proves the server actually speaks MCP.
func TestServerRoundtrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "hello.txt"), "hi there")

	srv := mcp.NewServer(&mcp.Implementation{Name: "agentbox-fs", Version: "test"}, nil)
	registerTools(srv, root)

	serverT, clientT := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()

	lt, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]bool{}
	for _, tl := range lt.Tools {
		got[tl.Name] = true
	}
	for _, want := range []string{"list_directory", "read_file", "search_files"} {
		if !got[want] {
			t.Errorf("tool %q not advertised; got %v", want, got)
		}
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_file",
		Arguments: map[string]any{"path": "hello.txt"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("read_file reported an error: %v", textOf(res))
	}
	if got := textOf(res); got != "hi there" {
		t.Fatalf("read_file content = %q, want %q", got, "hi there")
	}

	// A traversal escape must come back as an in-band tool error, not a crash.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_file",
		Arguments: map[string]any{"path": "../../../etc/passwd"},
	})
	if err != nil {
		t.Fatalf("CallTool(escape): %v", err)
	}
	if !res.IsError {
		t.Error("path traversal should be reported as a tool error")
	}
}

func textOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
