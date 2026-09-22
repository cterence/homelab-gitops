// e2e client: connects to a local mcp-telegram server over streamable HTTP
// and exercises the send_message tool.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-client", Version: "0"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: "http://127.0.0.1:8000/mcp"}
	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect failed:", err)
		os.Exit(1)
	}
	defer sess.Close()

	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "list tools failed:", err)
		os.Exit(1)
	}
	for _, t := range tools.Tools {
		fmt.Println("tool:", t.Name)
	}

	cases := []struct {
		name      string
		args      map[string]any
		wantError bool
	}{
		{"plain text", map[string]any{"text": "mcp-telegram Go port: e2e plain-text message"}, false},
		{"valid html", map[string]any{"text": "mcp-telegram Go port: <b>e2e</b> <code>HTML</code> message", "parse_mode": "HTML"}, false},
		{"broken html falls back to plain", map[string]any{"text": "mcp-telegram Go port: e2e <b>unclosed html fallback message", "parse_mode": "HTML"}, false},
		{"invalid parse mode", map[string]any{"text": "never delivered", "parse_mode": "RichText"}, true},
	}

	failed := false
	for _, tc := range cases {
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: tc.args})
		switch {
		case tc.wantError && err == nil:
			fmt.Printf("FAIL %s: expected protocol error, got result %+v\n", tc.name, res)
			failed = true
		case tc.wantError && err != nil:
			fmt.Printf("PASS %s: rejected as expected: %v\n", tc.name, err)
		case !tc.wantError && err != nil:
			fmt.Printf("FAIL %s: %v\n", tc.name, err)
			failed = true
		case !tc.wantError:
			if res.IsError {
				fmt.Printf("FAIL %s: tool reported error: %+v\n", tc.name, res)
				failed = true
			} else {
				fmt.Printf("PASS %s: %s\n", tc.name, res.Content[0])
			}
		}
	}

	if failed {
		os.Exit(1)
	}
	fmt.Println("ALL E2E CASES PASSED")
}
