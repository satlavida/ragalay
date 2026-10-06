package cli

import (
	"context"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/mcpserver"
	"github.com/satlavida/ragalay/internal/search"
)

func (a *app) mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run as an MCP server for AI agents (stdio)",
		Long: `Serves ragalay's tools (search, status, list_documents, list_folders, scan)
over the Model Context Protocol on stdin/stdout. Add it to Claude Code with:

  claude mcp add ragalay -- /path/to/ragalay mcp --root /path/to/your/folder`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			// The query model loads on the first search and stays loaded.
			var (
				once     sync.Once
				searcher *search.Searcher
				closeFn  = func() {}
			)
			defer func() { closeFn() }()
			getSearcher := func(cfg config.Config) *search.Searcher {
				once.Do(func() { searcher, closeFn, _ = newSearcher(root, cfg) })
				searcher.Cfg = cfg // pick up config edits
				return searcher
			}
			b := mcpserver.Backend{
				Search: func(ctx context.Context, q string, o search.Options) (search.Response, error) {
					cfg, err := config.Load(root)
					if err != nil {
						return search.Response{}, err
					}
					return getSearcher(cfg).Search(ctx, q, o)
				},
				Status: func(ctx context.Context) (any, error) { return statusReport(ctx, root) },
				ListDocuments: func(ctx context.Context, status, kind string) (any, error) {
					return docInfos(ctx, root, status, kind)
				},
				ListFolders: func(ctx context.Context) (any, error) {
					cfg, err := config.Load(root)
					if err != nil {
						return nil, err
					}
					infos, err := folderInfos(ctx, root, cfg)
					return map[string]any{"whole_directory": len(cfg.Scan.Folders) == 0, "folders": infos,
						"kept": cfg.Scan.Keep}, err
				},
				Scan: func(ctx context.Context) (any, error) { return a.scanAndIndex(ctx, root) },
			}
			return mcpserver.New(b, Version).Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
}
