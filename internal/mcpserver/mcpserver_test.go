package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/satlavida/ragalay/internal/search"
)

func connect(t *testing.T, b Backend) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := New(b, "test")
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestToolsAreListed(t *testing.T) {
	cs := connect(t, Backend{})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if strings.Join(names, ",") != "list_documents,list_folders,scan,search,status" {
		t.Fatalf("tools = %v", names)
	}
}

func TestSearchTool(t *testing.T) {
	var got search.Options
	var gotQuery string
	cs := connect(t, Backend{Search: func(_ context.Context, q string, o search.Options) (search.Response, error) {
		gotQuery, got = q, o
		return search.Response{Mode: "hybrid", Results: []search.Result{{Score: 0.03, Path: "papers/bert.pdf", Kind: "pdf",
			Modality: "text", Page: 2, Text: "Masked language model"}}}, nil
	}})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "search", Arguments: map[string]any{
		"query": "masked LM", "k": 3, "modality": []string{"text"}, "group_by": "doc", "path_glob": "papers/*"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	if gotQuery != "masked LM" || got.K != 3 || !got.GroupByDoc || got.PathGlob != "papers/*" || got.Modalities[0] != "text" {
		t.Fatalf("options not passed through: %q %+v", gotQuery, got)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(b), `"path":"papers/bert.pdf"`) || !strings.Contains(string(b), `"page":2`) {
		t.Fatalf("structured result: %s", b)
	}
	// Text content is there too, for clients that ignore structured output.
	if len(res.Content) == 0 {
		t.Fatal("no text content")
	}
}

func TestToolErrorsAreReported(t *testing.T) {
	cs := connect(t, Backend{Scan: func(context.Context) (any, error) {
		return nil, errors.New("another ragalay process is indexing")
	}})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "scan", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("scan failure should be a tool error the agent can read")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "already indexing") && !strings.Contains(text, "another ragalay process") {
		t.Fatalf("error text: %q", text)
	}
}
