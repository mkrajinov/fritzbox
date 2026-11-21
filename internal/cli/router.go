package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mkrajinov/fritzbox/internal/callmon"
	"github.com/mkrajinov/fritzbox/internal/config"
	"github.com/mkrajinov/fritzbox/internal/tr064"
	"github.com/mkrajinov/fritzbox/internal/ui"
)

func routerCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "router", Aliases: []string{"routers"}, Short: "Manage router profiles"}
	cmd.AddCommand(routerListCmd(a), routerAddCmd(a), routerRemoveCmd(a), routerUseCmd(a), routerTestCmd(a), routerTrustCmd(a))
	return cmd
}

func routerListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List router profiles",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(cfg)
			}
			if len(cfg.Routers) == 0 {
				fmt.Fprintln(a.out, "no routers configured; run `fb router add home --host fritz.box`")
				return nil
			}
			var rows [][]string
			for _, n := range cfg.Names() {
				r := cfg.Routers[n]
				name := n
				if n == cfg.Default {
					name = ui.GradientPill(n)
				}
				tls := ""
				if r.TLS {
					tls = "tls"
				}
				rows = append(rows, []string{name, r.Host, r.User, tls})
			}
			a.printTable([]string{"NAME", "HOST", "USER", "TLS"}, rows)
			return nil
		},
	}
}

func routerAddCmd(a *app) *cobra.Command {
	var (
		r         config.Router
		pwStdin   bool
		noKeyring bool
		skipTest  bool
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add or update a router profile (password goes to the OS keychain)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := a.config()
			if err != nil {
				return err
			}
			if r.Host == "" {
				r.Host = "fritz.box"
			}
			if r.TLS {
				ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout)
				defer cancel()
				fp, err := tr064.FetchFingerprint(ctx, r.Host, r.Port)
				if err != nil {
					return fmt.Errorf("tls: %w", err)
				}
				fmt.Fprintf(a.err, "Certificate SHA-256 fingerprint of %s:\n  %s\n", r.Host, fp)
				if !confirm(a, false, "Trust and pin this certificate?") {
					return errors.New("aborted")
				}
				r.Fingerprint = fp
			}
			if !noKeyring && r.PasswordCmd == "" {
				pw, err := readPassword(a, pwStdin, fmt.Sprintf("Password for %s@%s: ", orDefault(r.User, "(last user)"), r.Host))
				if err != nil {
					return err
				}
				if pw != "" {
					file, err := config.StorePassword(name, pw)
					if err != nil {
						return fmt.Errorf("store password: %w (use --no-keychain with FRITZ_PASSWORD or --password-cmd)", err)
					}
					if file != "" {
						fmt.Fprintf(a.err, "warning: no keychain available, password stored in %s (readable only by you)\n", file)
					}
				}
			}
			rr := r
			cfg.Routers[name] = &rr
			if cfg.Default == "" {
				cfg.Default = name
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "saved router %q to %s\n", name, cfg.Path())
			if skipTest {
				return nil
			}
			return a.testRouter(cmd, name, &rr)
		},
	}
	f := cmd.Flags()
	f.StringVar(&r.Host, "host", "fritz.box", "hostname or IP")
	f.StringVarP(&r.User, "user", "u", "", "FRITZ!Box user (empty = last logged-in user)")
	f.BoolVar(&r.TLS, "tls", false, "use HTTPS (port 49443) with a pinned certificate")
	f.IntVar(&r.Port, "port", 0, "TR-064 port (default 49000, or 49443 with --tls)")
	f.StringVar(&r.PasswordCmd, "password-cmd", "", "shell command printing the password (instead of keychain)")
	f.BoolVar(&pwStdin, "password-stdin", false, "read the password from stdin")
	f.BoolVar(&noKeyring, "no-keychain", false, "don't store a password (use FRITZ_PASSWORD or --password-cmd)")
	f.BoolVar(&skipTest, "no-test", false, "skip the connectivity/permission test")
	return cmd
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func readPassword(a *app, fromStdin bool, prompt string) (string, error) {
	if fromStdin || !term.IsTerminal(int(os.Stdin.Fd())) {
		s, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && s == "" {
			return "", nil
		}
		return strings.TrimRight(s, "\r\n"), nil
	}
	fmt.Fprint(a.err, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(a.err)
	return string(b), err
}

func routerRemoveCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "remove <name>", Aliases: []string{"rm"}, Short: "Remove a router profile and its keychain entry",
		ValidArgsFunction: a.completeRouters,
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			if _, ok := cfg.Routers[args[0]]; !ok {
				return fmt.Errorf("unknown router %q", args[0])
			}
			delete(cfg.Routers, args[0])
			if cfg.Default == args[0] {
				cfg.Default = ""
			}
			if err := config.DeletePassword(args[0]); err != nil {
				fmt.Fprintln(a.err, "warning: stored password:", err)
			}
			return cfg.Save()
		},
	}
}

func routerUseCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "use <name>", Short: "Set the default router",
		ValidArgsFunction: a.completeRouters,
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			if _, ok := cfg.Routers[args[0]]; !ok {
				return fmt.Errorf("unknown router %q", args[0])
			}
			cfg.Default = args[0]
			return cfg.Save()
		},
	}
}

func routerTrustCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "trust [name]", Short: "(Re-)pin the TLS certificate of a router",
		ValidArgsFunction: a.completeRouters,
		Args:              cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			sel := a.routerName
			if len(args) == 1 {
				sel = args[0]
			}
			name, r, err := cfg.Resolve(sel)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout)
			defer cancel()
			fp, err := tr064.FetchFingerprint(ctx, r.Host, r.Port)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.err, "%s presents certificate SHA-256:\n  %s\n", r.Host, fp)
			if r.Fingerprint != "" && r.Fingerprint != fp {
				fmt.Fprintln(a.err, ui.Warn.Render("  differs from the pinned "+r.Fingerprint))
			}
			if !confirm(a, false, "Pin this certificate for "+name+"?") {
				return errors.New("aborted")
			}
			r.TLS, r.Fingerprint = true, fp
			return cfg.Save()
		},
	}
}

func routerTestCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "test [name]", Short: "Check connectivity, credentials and per-feature permissions",
		ValidArgsFunction: a.completeRouters,
		Args:              cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			sel := a.routerName
			if len(args) == 1 {
				sel = args[0]
			}
			name, r, err := cfg.Resolve(sel)
			if err != nil {
				return err
			}
			return a.testRouter(cmd, name, r)
		},
	}
}

type check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warning,omitempty"` // optional feature or caveat: shown, but not a failure
	Detail string `json:"detail"`
}

func (a *app) testRouter(cmd *cobra.Command, name string, r *config.Router) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	var checks []check
	add := func(n string, err error, okDetail string) bool {
		c := check{Name: n, OK: err == nil, Detail: okDetail}
		if err != nil {
			c.Detail = err.Error()
		}
		checks = append(checks, c)
		return err == nil
	}

	b, err := a.boxFor(ctx, name, r)
	if !add("connect", err, "") {
		return a.reportChecks(checks)
	}
	d, err := b.TR.Description(ctx)
	if !add("TR-064 enabled", err, func() string {
		if d == nil {
			return ""
		}
		return fmt.Sprintf("%s, FRITZ!OS %s, %d services", d.ModelName, d.Firmware, len(d.Services))
	}()) {
		return a.reportChecks(checks)
	}
	info, err := b.Info(ctx)
	if !add("login as "+orDefault(b.User, "?"), err, info.Model+" "+info.Firmware) {
		return a.reportChecks(checks)
	}
	_, err = b.Hosts(ctx)
	add("hosts (FRITZ!Box settings)", err, "")
	ws, err := b.WLANs(ctx)
	add("WLAN", err, fmt.Sprintf("%d networks", len(ws)))
	_, err = b.Calls(ctx, 1)
	add("call list (Voice messages, fax messages, FRITZ!App Fon and call list)", err, "")
	devs, err := b.SmartDevices(ctx)
	add("smart home (Smart Home)", err, fmt.Sprintf("%d devices", len(devs)))

	dc, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(r.Host, strconv.Itoa(callmon.Port)))
	if err == nil {
		dc.Close()
		add("call monitor :1012", nil, "reachable")
	} else {
		checks = append(checks, check{Name: "call monitor :1012 (optional)", OK: true, Warn: true,
			Detail: "off; for live call events dial #96*5* on a phone connected to the box"})
	}

	if b.TR.HasService(ctx, "X_AVM-DE_Auth1") {
		res, err := b.TR.Call(ctx, "X_AVM-DE_Auth1", "GetInfo", nil)
		if err == nil && res["NewEnabled"] == "1" {
			checks = append(checks, check{Name: "second factor", OK: true, Warn: true, Detail: "enabled: reboot, WLAN toggles and similar writes may be refused, because fb does not support the confirmation flow yet"})
		}
	}
	return a.reportChecks(checks)
}

func (a *app) reportChecks(checks []check) error {
	if a.asJSON {
		return a.printJSON(checks)
	}
	failed := 0
	for _, c := range checks {
		mark := ui.OK.Render("✓")
		if c.Warn {
			mark = ui.Warn.Render("!")
		}
		if !c.OK {
			mark = ui.Bad.Render("✗")
			failed++
		}
		line := fmt.Sprintf("%s %s", mark, ui.Bold.Render(c.Name))
		if c.Detail != "" {
			line += "  " + ui.Muted.Render(c.Detail)
		}
		fmt.Fprintln(a.out, line)
	}
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	return nil
}

// completeRouters offers router profile names (with host as description) for the first argument.
func (a *app) completeRouters(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := a.config()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := make([]string, 0, len(cfg.Routers))
	for _, n := range cfg.Names() {
		desc := cfg.Routers[n].Host
		if n == cfg.Default {
			desc += " (default)"
		}
		out = append(out, n+"\t"+desc)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
