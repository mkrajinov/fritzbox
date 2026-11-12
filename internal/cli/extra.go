package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"

	"github.com/mkrajinovic/fbctl/internal/config"
	"github.com/mkrajinovic/fbctl/internal/exporter"
	"github.com/mkrajinovic/fbctl/internal/fritz"
	"github.com/mkrajinovic/fbctl/internal/ui"
)

func meshCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "mesh",
		Short: "Show the mesh: router, repeaters and which device is connected where",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			b, err := a.box(ctx)
			if err != nil {
				return err
			}
			nodes, err := b.Mesh(ctx)
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(nodes)
			}
			for i, n := range nodes {
				if i > 0 {
					fmt.Fprintln(a.out)
				}
				fmt.Fprintln(a.out, ui.MeshHeader(n))
				for _, c := range n.Clients {
					fmt.Fprintln(a.out, "  "+ui.MeshClientLine(c))
				}
			}
			return nil
		},
	}
}

func logCmd(a *app) *cobra.Command {
	var group string
	var limit int
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show the box's event log (newest first)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if group != "" && !slices.Contains(fritz.LogGroups, group) {
				return fmt.Errorf("unknown group %q (use %s)", group, strings.Join(fritz.LogGroups, "|"))
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			b, err := a.box(ctx)
			if err != nil {
				return err
			}
			evs, err := b.DeviceLog(ctx, group)
			if err != nil {
				return err
			}
			if limit > 0 && len(evs) > limit {
				evs = evs[:limit]
			}
			if a.asJSON {
				return a.printJSON(evs)
			}
			for _, e := range evs {
				fmt.Fprintf(a.out, "%s  %-4s  %s\n", ui.Muted.Render(e.Time.Format("2006-01-02 15:04:05")), ui.Muted.Render(e.Group), e.Msg)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&group, "group", "g", "", "filter: "+strings.Join(fritz.LogGroups, "|"))
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "number of events (0 = all)")
	_ = cmd.RegisterFlagCompletionFunc("group", cobra.FixedCompletions(fritz.LogGroups, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

func hostsRenameCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <mac|ip|name> <new-name>",
		Short: "Change the name the box shows for a device",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			b, err := a.box(ctx)
			if err != nil {
				return err
			}
			hosts, err := b.Hosts(ctx)
			if err != nil {
				return err
			}
			h, err := fritz.FindHost(hosts, args[0])
			if err != nil {
				return err
			}
			if err := b.RenameHost(ctx, h.MAC, args[1]); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s (%s) renamed to %s\n", orDefault(h.Name, h.IP), h.MAC, args[1])
			return nil
		},
	}
}

func wlanPasswordCmd(a *app) *cobra.Command {
	var set string
	var length int
	var yes bool
	cmd := &cobra.Command{
		Use:   "password <2.4|5|6|guest|index>",
		Short: "Show or rotate a WLAN password (default: show)",
		Long: "Without flags, prints the current passphrase. --generate N or --set PW changes it;\n" +
			"devices on that WLAN must reconnect with the new password.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			b, err := a.box(ctx)
			if err != nil {
				return err
			}
			ws, err := b.WLANs(ctx)
			if err != nil {
				return err
			}
			w, err := fritz.ResolveWLAN(ws, args[0])
			if err != nil {
				return err
			}
			newPW := set
			if cmd.Flags().Changed("generate") {
				if newPW, err = fritz.GeneratePassword(length); err != nil {
					return err
				}
			}
			if newPW == "" {
				pw, err := b.WLANPassword(ctx, w.Index)
				if err != nil {
					return err
				}
				fmt.Fprintln(a.out, pw)
				return nil
			}
			if !confirm(a, yes, fmt.Sprintf("Change the password of %s (%s)? Connected devices will drop.", w.Label(), w.SSID)) {
				return errors.New("aborted")
			}
			if err := b.SetWLANPassword(ctx, w.Index, newPW); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s (%s) password set to: %s\n", w.Label(), w.SSID, newPW)
			fmt.Fprintf(a.out, "%s\n", ui.Muted.Render("show a join QR code with: fb wlan qr "+args[0]))
			return nil
		},
	}
	cmd.Flags().StringVar(&set, "set", "", "new passphrase (8–63 characters)")
	cmd.Flags().IntVar(&length, "generate", 16, "generate a random passphrase of this length")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask")
	cmd.MarkFlagsMutuallyExclusive("set", "generate")
	return cmd
}

// statusAll prints one line per router profile.
func (a *app) statusAll(cmd *cobra.Command) error {
	cfg, err := a.config()
	if err != nil {
		return err
	}
	if len(cfg.Routers) == 0 {
		return errors.New("no router configured")
	}
	ctx, cancel := a.ctx(cmd)
	defer cancel()
	res := fritz.StatusAll(ctx, cfg, func(ctx context.Context, name string, r *config.Router) (*fritz.Box, error) {
		return a.boxFor(ctx, name, r)
	})
	if a.asJSON {
		return a.printJSON(res)
	}
	var rows [][]string
	for _, r := range res {
		name := r.Router
		if r.Router == cfg.Default {
			name = ui.GradientPill(r.Router)
		}
		if r.Error != "" {
			rows = append(rows, []string{name, r.Host, ui.Bad.Render(ui.GlyphOff + " unreachable"), "", "", ui.Muted.Render(trunc(r.Error, 60))})
			continue
		}
		st := r.Status
		wan := ui.Muted.Render("no WAN")
		if st.HasWAN {
			wan = ui.Bad.Render(ui.GlyphOff + " offline")
			if st.Connected {
				wan = ui.OK.Render(ui.GlyphOn + " online")
			}
		}
		rates := ""
		if st.HasWAN {
			rates = ui.Up.Render(ui.GlyphUp+" "+ui.Rate(st.Rates.Up)) + "  " + ui.Down.Render(ui.GlyphDown+" "+ui.Rate(st.Rates.Down))
		}
		rows = append(rows, []string{name, r.Host, wan, rates, st.Model + " " + ui.Muted.Render(st.Firmware), ui.Duration(st.Uptime)})
	}
	a.printTable([]string{"ROUTER", "HOST", "INTERNET", "CURRENT", "MODEL", "UPTIME"}, rows)
	return nil
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func exporterCmd(a *app) *cobra.Command {
	var listen string
	var smart bool
	cmd := &cobra.Command{
		Use:   "exporter",
		Short: "Serve Prometheus metrics for all router profiles on /metrics",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			names := cfg.Names()
			if a.routerName != "" {
				names = []string{a.routerName}
			}
			var targets []exporter.Target
			for _, n := range names {
				r, ok := cfg.Routers[n]
				if !ok {
					return fmt.Errorf("unknown router %q", n)
				}
				b, err := a.boxFor(cmd.Context(), n, r)
				if err != nil {
					return fmt.Errorf("%s: %w", n, err)
				}
				targets = append(targets, exporter.Target{Name: n, Box: b})
			}
			if len(targets) == 0 {
				return errors.New("no router configured")
			}
			reg := prometheus.NewRegistry()
			reg.MustRegister(exporter.New(targets, exporter.Options{Timeout: a.timeout * 2, SmartHome: smart}))
			mux := http.NewServeMux()
			mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
			mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintln(w, `fb exporter: metrics at /metrics`)
			})
			srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
			go func() {
				<-cmd.Context().Done()
				sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(sctx)
			}()
			fmt.Fprintf(a.err, "serving %d router(s) on http://%s/metrics\n", len(targets), listen)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:9133", "address to listen on")
	cmd.Flags().BoolVar(&smart, "smarthome", true, "also collect smart home devices")
	return cmd
}

func hostsAccessCmds(a *app) []*cobra.Command {
	resolve := func(cmd *cobra.Command, q string) (*fritz.Box, fritz.Host, context.Context, context.CancelFunc, error) {
		ctx, cancel := a.ctx(cmd)
		b, err := a.box(ctx)
		if err != nil {
			return nil, fritz.Host{}, ctx, cancel, err
		}
		hosts, err := b.Hosts(ctx)
		if err != nil {
			return nil, fritz.Host{}, ctx, cancel, err
		}
		h, err := fritz.FindHost(hosts, q)
		return b, h, ctx, cancel, err
	}
	access := &cobra.Command{
		Use: "access <mac|ip|name>", Short: "Show whether a host may reach the internet", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, h, ctx, cancel, err := resolve(cmd, args[0])
			defer cancel()
			if err != nil {
				return err
			}
			acc, blocked, err := b.WANAccess(ctx, h.IP)
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(map[string]any{"host": h.Name, "ip": h.IP, "access": acc, "blocked": blocked})
			}
			st := ui.OK.Render(acc)
			if acc != "granted" {
				st = ui.Bad.Render(acc)
			}
			note := ""
			if acc == "denied" && !blocked {
				note = ui.Muted.Render("  (denied by a parental-control filter profile, not by a block)")
			}
			fmt.Fprintf(a.out, "%s (%s): internet %s%s\n", orDefault(h.Name, h.IP), h.IP, st, note)
			return nil
		},
	}
	set := func(block bool) *cobra.Command {
		use, short := "unblock <mac|ip|name>", "Allow internet access for a host again"
		if block {
			use, short = "block <mac|ip|name>", "Block internet access for a host (LAN keeps working)"
		}
		return &cobra.Command{
			Use: use, Short: short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				b, h, ctx, cancel, err := resolve(cmd, args[0])
				defer cancel()
				if err != nil {
					return err
				}
				if err := b.SetInternetBlocked(ctx, h.IP, block); err != nil {
					return err
				}
				verb := map[bool]string{true: "blocked", false: "allowed"}[block]
				fmt.Fprintf(a.out, "internet %s for %s (%s); the box applies it within a few seconds\n", verb, orDefault(h.Name, h.IP), h.IP)
				return nil
			},
		}
	}
	return []*cobra.Command{access, set(true), set(false)}
}
