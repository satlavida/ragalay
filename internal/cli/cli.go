// Package cli implements the ragalay command line.
package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/store"
	"github.com/satlavida/ragalay/internal/tui"
)

// Exit codes (plan1 §4.7). Agents rely on these.
const (
	ExitOK              = 0
	ExitError           = 1
	ExitModelMismatch   = 2
	ExitNotInitialized  = 3
	ExitSetupIncomplete = 4
	ExitLocked          = 5
)

// Version is set at build time with -ldflags "-X .../cli.Version=v1.2.3".
var Version = "dev"

// exitError carries a specific exit code up to Execute.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

type app struct {
	rootFlag string
	// yes allows sending documents to the folder's embedding service without
	// asking (--yes on scan and reembed; plan2 S10).
	yes    bool
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	isTTY  func() bool // nil: check os.Stdin
}

// Execute runs the CLI with os.Args and returns the process exit code.
func Execute() int {
	return run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, nil)
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, isTTY func() bool) int {
	a := &app{stdin: stdin, stdout: stdout, stderr: stderr, isTTY: isTTY}
	cmd := a.rootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := cmd.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	fmt.Fprintln(stderr, "error:", err)
	var ee *exitError
	switch {
	case errors.As(err, &ee):
		return ee.code
	case errors.Is(err, config.ErrNotInitialized):
		return ExitNotInitialized
	case errors.Is(err, store.ErrBusy):
		return ExitLocked
	}
	return ExitError
}

func (a *app) rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ragalay",
		Short: "Search your Markdown, PDF and image files, locally",
		Long: `ragalay indexes the documents in this folder so people (via the TUI) and
AI agents (via --json output or MCP) can search them. Nothing leaves your machine.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Double-clicked or run bare in a terminal: the window interface
			// with its first-run wizard. Piped or scripted: help.
			if !a.interactive() || !isTerminal(a.stdout) {
				return cmd.Help()
			}
			b := &tuiBackend{a: a}
			defer b.close()
			return tui.Run(b)
		},
	}
	cmd.PersistentFlags().StringVar(&a.rootFlag, "root", "",
		"ragalay directory (default: search upward from the current folder, then from the binary; env RAGALAY_ROOT)")
	cmd.AddCommand(a.initCmd(), a.foldersCmd(), a.setupCmd(), a.scanCmd(), a.reembedCmd(), a.searchCmd(), a.mcpCmd(), a.docsCmd(), a.statusCmd(), a.updateCmd(), a.versionCmd())
	return cmd
}

// root finds the ragalay folder and brings its index to the current schema,
// so folders made by an older ragalay keep working after an update.
func (a *app) root() (string, error) {
	root, err := config.ResolveRoot(a.rootFlag)
	if err != nil {
		return root, err
	}
	return root, upgradeIndex(root)
}

func upgradeIndex(root string) error {
	path := dbPath(root)
	if _, err := os.Stat(path); err != nil {
		return nil // not created yet: init does it
	}
	ctx := context.Background()
	return store.With(ctx, path, func(db *sql.DB) error {
		if v, err := store.Version(ctx, db); err != nil || v == store.SchemaVersion {
			return err
		}
		cfg, err := config.Load(root)
		if err != nil {
			cfg = config.Default()
		}
		return store.Migrate(ctx, db, cfg.Embed.Dim)
	})
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a *app) versionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the ragalay version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if asJSON {
				return a.printJSON(map[string]any{"version": Version, "schema_version": store.SchemaVersion})
			}
			fmt.Fprintf(a.stdout, "ragalay %s (index schema %d)\n", Version, store.SchemaVersion)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}
