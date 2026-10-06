// Package cli implements the ragalay command line.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/store"
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
	stdout   io.Writer
	stderr   io.Writer
}

// Execute runs the CLI with os.Args and returns the process exit code.
func Execute() int {
	return run(os.Args[1:], os.Stdout, os.Stderr)
}

func run(args []string, stdout, stderr io.Writer) int {
	a := &app{stdout: stdout, stderr: stderr}
	cmd := a.rootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	err := cmd.Execute()
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
			// The TUI (and its setup wizard) arrives in plan1 Phase 7.
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().StringVar(&a.rootFlag, "root", "",
		"ragalay directory (default: search upward from the current folder, then from the binary; env RAGALAY_ROOT)")
	cmd.AddCommand(a.initCmd(), a.foldersCmd(), a.statusCmd(), a.versionCmd())
	return cmd
}

func (a *app) root() (string, error) { return config.ResolveRoot(a.rootFlag) }

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
