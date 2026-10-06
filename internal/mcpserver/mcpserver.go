// Package mcpserver exposes ragalay to AI agents over the Model Context
// Protocol (stdio): search, status, list_documents, list_folders and scan
// (plan1 §4.8). The tools call back into the CLI's functions, injected as
// Backend, so the server has no knowledge of storage or models.
package mcpserver

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/satlavida/ragalay/internal/search"
)

// Backend is what the tools need. Every function is called per request.
type Backend struct {
	Search        func(ctx context.Context, query string, o search.Options) (search.Response, error)
	Status        func(ctx context.Context) (any, error)
	ListDocuments func(ctx context.Context, status, kind string) (any, error)
	ListFolders   func(ctx context.Context) (any, error)
	Scan          func(ctx context.Context) (any, error)
}

// SearchInput is the search tool's arguments.
type SearchInput struct {
	Query    string   `json:"query" jsonschema:"what to look for, in natural language or keywords"`
	K        int      `json:"k,omitempty" jsonschema:"number of results (default 10)"`
	Mode     string   `json:"mode,omitempty" jsonschema:"hybrid (default), vector or keyword"`
	Modality []string `json:"modality,omitempty" jsonschema:"only these kinds of hits: text, image, pdf_page"`
	PathGlob string   `json:"path_glob,omitempty" jsonschema:"only files whose path matches, e.g. papers/*.pdf"`
	GroupBy  string   `json:"group_by,omitempty" jsonschema:"chunk (default) or doc for one result per document"`
	MaxChars int      `json:"max_chars,omitempty" jsonschema:"trim each result's text (default 2000)"`
}

// SearchOutput is the search tool's result.
type SearchOutput struct {
	Mode    string          `json:"mode"`
	Notice  string          `json:"notice,omitempty"`
	Results []search.Result `json:"results"`
}

// ListDocumentsInput filters list_documents.
type ListDocumentsInput struct {
	Status string `json:"status,omitempty" jsonschema:"pending, processing, done, failed or stale"`
	Kind   string `json:"kind,omitempty" jsonschema:"markdown, pdf or image"`
}

// Empty is for tools without arguments.
type Empty struct{}

// Result wraps arbitrary JSON output.
type Result struct {
	Data any `json:"data"`
}

// New builds the MCP server.
func New(b Backend, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "ragalay", Version: version}, &mcp.ServerOptions{
		Instructions: "ragalay searches the user's local documents (Markdown, PDF, images). " +
			"Use search to find passages; cite results by path and page. " +
			"Results with parent_path are images shown inside that Markdown file; " +
			"paired_path names a Markdown transcription of the PDF.",
	})
	mcp.AddTool(s, &mcp.Tool{
		Name: "search",
		Description: "Search the user's indexed documents. Returns ranked passages with path, page, heading and text. " +
			"Use specific queries; run several searches for different aspects of a question.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		o := search.Options{K: in.K, Mode: in.Mode, Modalities: in.Modality, PathGlob: in.PathGlob,
			GroupByDoc: strings.EqualFold(in.GroupBy, "doc"), MaxChars: in.MaxChars}
		resp, err := b.Search(ctx, in.Query, o)
		if err != nil {
			return nil, SearchOutput{}, err
		}
		return nil, SearchOutput{Mode: resp.Mode, Notice: resp.Notice, Results: resp.Results}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "status",
		Description: "How much is indexed, what is queued, whether indexing is running, and whether the models are set up.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, Result, error) {
		v, err := b.Status(ctx)
		return nil, Result{Data: v}, err
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_documents",
		Description: "List indexed documents with their status, kind and paired transcription.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListDocumentsInput) (*mcp.CallToolResult, Result, error) {
		v, err := b.ListDocuments(ctx, in.Status, in.Kind)
		return nil, Result{Data: v}, err
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_folders",
		Description: "The folders ragalay indexes, with document counts.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, Result, error) {
		v, err := b.ListFolders(ctx)
		return nil, Result{Data: v}, err
	})
	mcp.AddTool(s, &mcp.Tool{
		Name: "scan",
		Description: "Look for new, changed or deleted files and index them. Can take minutes for many new files; " +
			"fails if another ragalay process is already indexing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, Result, error) {
		v, err := b.Scan(ctx)
		return nil, Result{Data: v}, err
	})
	return s
}
