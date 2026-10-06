// Drives `ragalay mcp` over real stdio with the official MCP client, the
// way Claude Code does. Usage: go run ./mcpclient <ragalay binary> <root>
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-check", Version: "1"}, nil)
	t0 := time.Now()
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(os.Args[1], "mcp", "--root", os.Args[2])}, nil)
	if err != nil {
		panic(err)
	}
	defer cs.Close()
	fmt.Printf("connected in %v; server instructions: %.80q...\n", time.Since(t0).Round(time.Millisecond), cs.InitializeResult().Instructions)
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		panic(err)
	}
	for _, t := range tools.Tools {
		fmt.Println("tool:", t.Name)
	}
	call := func(name string, args map[string]any) {
		t := time.Now()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			panic(err)
		}
		b, _ := json.Marshal(res.StructuredContent)
		if len(b) > 600 {
			b = append(b[:600], []byte("...")...)
		}
		fmt.Printf("\n%s %v (%v, error=%v):\n%s\n", name, args, time.Since(t).Round(time.Millisecond), res.IsError, b)
	}
	call("search", map[string]any{"query": "how many identical layers in the encoder", "k": 2, "max_chars": 160})
	call("search", map[string]any{"query": "masked language model", "k": 2, "max_chars": 120, "group_by": "doc"})
	call("status", map[string]any{})
	call("list_documents", map[string]any{"kind": "pdf"})
}
