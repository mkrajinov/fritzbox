package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mkrajinov/fritzbox/internal/config"
	"github.com/mkrajinov/fritzbox/internal/service"
	"github.com/mkrajinov/fritzbox/internal/ui"
)

// newServiceManager is replaced in tests.
var newServiceManager = service.New

func jobNames() []string {
	names := make([]string, 0, len(service.Jobs))
	for n := range service.Jobs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (a *app) serviceSpec(job string) service.Spec {
	s := service.Spec{Job: job, Router: a.routerName}
	if a.cfgPath != config.DefaultPath() {
		s.Args = []string{"--config", a.cfgPath}
	}
	return s
}

func serviceCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Run watch, exporter or scheduled backups in the background (launchd / systemd)",
		Long: "Installs fritzbox jobs as user services: launchd agents on macOS, systemd user units on\n" +
			"Linux. They start at login and restart when they stop. Jobs: watch (notifications,\n" +
			"presence, forwarding), exporter (Prometheus metrics) and backup (fb backup run on a\n" +
			"schedule). With -r/--router the service is bound to that router profile.",
	}
	var every time.Duration
	var dryRun bool
	install := &cobra.Command{
		Use:   "install <" + strings.Join(jobNames(), "|") + "> [-- job flags]",
		Short: "Install and start a background job",
		Example: "  fb service install watch -- --track anna-phone --ntfy https://ntfy.sh/my-topic\n" +
			"  fb service install exporter -- --listen 127.0.0.1:9133\n" +
			"  fb -r home service install backup --every 24h -- --keep 30",
		Args:      cobra.MinimumNArgs(1),
		ValidArgs: jobNames(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dash := cmd.ArgsLenAtDash(); dash >= 0 && dash != 1 || dash < 0 && len(args) != 1 {
				return errors.New("put job flags after --, e.g. fb service install watch -- --track phone")
			}
			s := a.serviceSpec(args[0])
			s.Extra = args[1:]
			if service.Jobs[s.Job].Periodic && every == 0 {
				every = 24 * time.Hour
			}
			s.Every = every
			if err := s.Validate(); err != nil {
				return err
			}
			s.Env = map[string]string{}
			for _, k := range service.SecretEnv {
				if v := os.Getenv(k); v != "" {
					s.Env[k] = v
				}
			}
			if s.Job == "backup" && s.Env["FB_BACKUP_PASSWORD"] == "" {
				name, err := a.profileName()
				if err != nil {
					return err
				}
				if pw, _ := config.BackupPassword(name); pw == "" {
					return fmt.Errorf("store the backup password first: fb -r %s backup password set", name)
				}
			}
			m, err := newServiceManager()
			if err != nil {
				return err
			}
			if dryRun {
				files, err := m.Files(s)
				if err != nil {
					return err
				}
				for _, f := range files {
					fmt.Fprintf(a.out, "%s (mode %o)\n%s\n", ui.Bold.Render(f.Path), f.Mode, f.Content)
				}
				return nil
			}
			files, err := m.Install(s)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s %s: %s\n", ui.OK.Render("installed"), s.Name(), strings.Join(m.Command(s)[1:], " "))
			for _, f := range files {
				fmt.Fprintln(a.out, ui.Muted.Render("  "+f.Path))
			}
			if len(s.Env) > 0 {
				keys := make([]string, 0, len(s.Env))
				for k := range s.Env {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				fmt.Fprintln(a.out, ui.Muted.Render("  environment copied (file readable only by you): "+strings.Join(keys, ", ")))
			}
			if m.GOOS == "linux" {
				fmt.Fprintln(a.out, ui.Muted.Render("  to keep it running while you are logged out: loginctl enable-linger $USER"))
			}
			fmt.Fprintf(a.out, "check it with: fb service status %s · fb service logs %s\n", s.Job, s.Job)
			return nil
		},
	}
	install.Flags().DurationVar(&every, "every", 0, "backup: how often to run (default 24h)")
	install.Flags().BoolVar(&dryRun, "dry-run", false, "print the service files instead of installing")

	uninstall := &cobra.Command{
		Use: "uninstall <job>", Aliases: []string{"remove"}, Short: "Stop and remove a background job",
		Args: cobra.ExactArgs(1), ValidArgs: jobNames(),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := newServiceManager()
			if err != nil {
				return err
			}
			s := a.serviceSpec(args[0])
			found, err := m.Uninstall(s)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("%s is not installed (see fb service list)", s.Name())
			}
			fmt.Fprintf(a.out, "removed %s\n", s.Name())
			return nil
		},
	}

	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List installed background jobs", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := newServiceManager()
			if err != nil {
				return err
			}
			names, err := m.Installed()
			if err != nil {
				return err
			}
			if len(names) == 0 {
				fmt.Fprintln(a.out, ui.Muted.Render("no background jobs installed (fb service install watch|exporter|backup)"))
				return nil
			}
			for _, n := range names {
				fmt.Fprintln(a.out, n)
			}
			return nil
		},
	}

	status := &cobra.Command{
		Use: "status <job>", Short: "Show whether a background job is running", Args: cobra.ExactArgs(1), ValidArgs: jobNames(),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := newServiceManager()
			if err != nil {
				return err
			}
			out, err := m.Status(a.serviceSpec(args[0]))
			if err != nil {
				return err
			}
			fmt.Fprintln(a.out, out)
			return nil
		},
	}

	var lines int
	var follow bool
	logs := &cobra.Command{
		Use: "logs <job>", Short: "Show the log of a background job", Args: cobra.ExactArgs(1), ValidArgs: jobNames(),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := newServiceManager()
			if err != nil {
				return err
			}
			c := m.LogCommand(a.serviceSpec(args[0]), lines, follow)
			x := exec.CommandContext(cmd.Context(), c[0], c[1:]...)
			x.Stdout, x.Stderr = a.out, a.err
			return x.Run()
		},
	}
	logs.Flags().IntVarP(&lines, "lines", "n", 50, "number of lines")
	logs.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines")

	cmd.AddCommand(install, uninstall, list, status, logs)
	return cmd
}
