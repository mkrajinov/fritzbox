// Package cli wires cobra commands to the fritz domain layer.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mkrajinov/fritzbox/internal/config"
	"github.com/mkrajinov/fritzbox/internal/fritz"
	"github.com/mkrajinov/fritzbox/internal/tr064"
)

// Version is set via -ldflags.
var Version = "dev"

type app struct {
	routerName string
	asJSON     bool
	timeout    time.Duration
	verbose    bool
	cfgPath    string

	out io.Writer
	err io.Writer
}

func (a *app) config() (*config.Config, error) { return config.Load(a.cfgPath) }

// box connects to the selected router.
func (a *app) box(ctx context.Context) (*fritz.Box, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	name, r, err := cfg.Resolve(a.routerName)
	if err != nil {
		return nil, err
	}
	return a.boxFor(ctx, name, r)
}

func (a *app) boxFor(ctx context.Context, name string, r *config.Router) (*fritz.Box, error) {
	pw, err := config.Password(name, r)
	if err != nil {
		return nil, err
	}
	var dbg io.Writer
	if a.verbose {
		dbg = a.err
	}
	b, err := fritz.Connect(ctx, name, r, pw, fritz.ConnectOptions{Timeout: a.timeout, Debug: dbg})
	if err == nil {
		b.SetSecondFactorHandler(a.secondFactorPrompt)
	}
	return b, err
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a *app) ctx(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	return context.WithTimeout(cmd.Context(), a.timeout*3)
}

// NewRoot builds the command tree writing to stdout/stderr.
func NewRoot() *cobra.Command { return newRoot(os.Stdout, os.Stderr) }

func newRoot(out, errOut io.Writer) *cobra.Command {
	a := &app{out: out, err: errOut}
	root := &cobra.Command{
		Use:           "fritzbox",
		Short:         "Manage AVM FRITZ!Box routers from the terminal (CLI + TUI). Short name: fb",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if term.IsTerminal(int(os.Stdout.Fd())) {
				return runTUI(cmd, a)
			}
			return cmd.Help()
		},
	}
	pf := root.PersistentFlags()
	pf.StringVarP(&a.routerName, "router", "r", "", "router profile (default: $FBCTL_ROUTER, the default profile, or the only one)")
	pf.BoolVar(&a.asJSON, "json", false, "print JSON")
	pf.DurationVar(&a.timeout, "timeout", 10*time.Second, "per-request timeout")
	pf.BoolVarP(&a.verbose, "verbose", "v", false, "log TR-064 requests to stderr (secrets redacted)")
	pf.StringVar(&a.cfgPath, "config", config.DefaultPath(), "config file")
	_ = root.RegisterFlagCompletionFunc("router", func(c *cobra.Command, _ []string, s string) ([]string, cobra.ShellCompDirective) {
		return a.completeRouters(c, nil, s)
	})

	root.AddCommand(
		routerCmd(a), statusCmd(a), hostsCmd(a), reconnectCmd(a), rebootCmd(a),
		wlanCmd(a), callsCmd(a), phonebookCmd(a), homeCmd(a), rawCmd(a), tuiCmd(a),
		meshCmd(a), logCmd(a), exporterCmd(a), watchCmd(a), seenCmd(a), serviceCmd(a), captureCmd(a),
	)
	adminCmds(a, root)
	return root
}

// Execute runs the CLI and maps errors to exit codes.
func Execute(ctx context.Context) int {
	root := NewRoot()
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		if errors.Is(err, tr064.ErrUnauthorized) {
			return 3
		}
		return 1
	}
	return 0
}

func confirm(a *app, yes bool, prompt string) bool {
	if yes {
		return true
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(a.err, "refusing without --yes (stdin is not a terminal)")
		return false
	}
	fmt.Fprintf(a.err, "%s [y/N] ", prompt)
	var s string
	_, _ = fmt.Fscanln(os.Stdin, &s)
	return s == "y" || s == "Y" || s == "yes"
}
