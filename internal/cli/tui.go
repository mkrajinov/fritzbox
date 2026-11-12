package cli

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/mkrajinovic/fbctl/internal/tui"
)

func tuiCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Interactive dashboard (default when run without arguments in a terminal)",
		RunE:  func(cmd *cobra.Command, _ []string) error { return runTUI(cmd, a) },
	}
}

func runTUI(cmd *cobra.Command, a *app) error {
	cfg, err := a.config()
	if err != nil {
		return err
	}
	current, _, err := cfg.Resolve(a.routerName)
	if err != nil {
		return err
	}
	names := cfg.Names()
	if current == "env" {
		names = []string{"env"}
	}
	connect := func(ctx context.Context, name string) (tui.Backend, string, error) {
		_, r, err := cfg.Resolve(name)
		if name == "env" {
			_, r, err = cfg.Resolve("")
		}
		if err != nil {
			return nil, "", err
		}
		a.verbose = false // stderr logging would corrupt the TUI
		b, err := a.boxFor(ctx, name, r)
		if err != nil {
			return nil, "", err
		}
		return b, r.Host, nil
	}
	m := tui.New(tui.Options{Routers: names, Current: current, Connect: connect})
	_, err = tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(cmd.Context())).Run()
	return err
}
