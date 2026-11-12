package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mkrajinovic/fbctl/internal/fritz"
	"github.com/mkrajinovic/fbctl/internal/ui"
)

func statusCmd(a *app) *cobra.Command {
	var watch time.Duration
	var all bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show model, firmware, WAN state, external IP and live rates",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if all {
				return a.statusAll(cmd)
			}
			b, err := a.box(cmd.Context())
			if err != nil {
				return err
			}
			for {
				ctx, cancel := a.ctx(cmd)
				st, err := b.Status(ctx)
				cancel()
				if err != nil && st.Model == "" {
					return err
				}
				if watch > 0 && !a.asJSON {
					fmt.Fprint(a.out, "\033[H\033[2J")
				}
				if a.asJSON {
					if err := a.printJSON(st); err != nil {
						return err
					}
				} else {
					a.printStatus(b.Name, st)
					if err != nil {
						fmt.Fprintln(a.err, ui.Warn.Render("partial: "+err.Error()))
					}
				}
				if watch <= 0 {
					return nil
				}
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(watch):
				}
			}
		},
	}
	cmd.Flags().DurationVarP(&watch, "watch", "w", 0, "refresh interval (e.g. 2s)")
	cmd.Flags().BoolVarP(&all, "all", "A", false, "one line per router profile, polled concurrently")
	return cmd
}

func (a *app) printStatus(name string, st fritz.Status) {
	fmt.Fprintf(a.out, "%s %s  %s\n\n", ui.GradientPill(ui.GlyphRouter+" "+name), ui.Bold.Render(st.Model), ui.Muted.Render("FRITZ!OS "+st.Firmware))
	pairs := [][2]string{{"box uptime", ui.Duration(st.Uptime)}}
	if st.UpdateAvailable != "" {
		pairs = append(pairs, [2]string{"update", ui.Warn.Render(ui.GlyphWarning + " " + st.UpdateAvailable + " available")})
	}
	if !st.HasWAN {
		pairs = append(pairs, [2]string{"WAN", ui.Muted.Render("none (repeater/mesh node)")})
		a.kv(pairs...)
		return
	}
	conn := ui.Bad.Render(ui.GlyphOff + " " + orDefault(st.ConnStatus, "unknown"))
	if st.Connected {
		conn = ui.OK.Render(ui.GlyphOn+" connected") + ui.Muted.Render(" for "+ui.Duration(st.ConnUptime))
	}
	link := ui.Bad.Render("down")
	if st.LinkUp {
		link = ui.OK.Render("up")
	}
	pairs = append(pairs,
		[2]string{"connection", conn + ui.Muted.Render("  "+strings.TrimSuffix(st.ConnectionType, "1"))},
		[2]string{"link", link + ui.Muted.Render("  "+st.AccessType)},
		[2]string{"external IPv4", orDefault(st.ExternalIPv4, "–")},
	)
	if st.ExternalIPv6 != "" {
		pairs = append(pairs, [2]string{"external IPv6", st.ExternalIPv6})
	}
	if st.LastError != "" {
		pairs = append(pairs, [2]string{"last error", ui.Bad.Render(st.LastError)})
	}
	// Upload always left, download always right.
	pairs = append(pairs,
		[2]string{"line rate", ui.Up.Render(ui.GlyphUp+" "+ui.Bits(st.MaxUpBits)) + "   " + ui.Down.Render(ui.GlyphDown+" "+ui.Bits(st.MaxDownBits))},
		[2]string{"current", ui.Up.Render(ui.GlyphUp+" "+ui.Rate(st.Rates.Up)) + "   " + ui.Down.Render(ui.GlyphDown+" "+ui.Rate(st.Rates.Down))},
		[2]string{"total", ui.Up.Render(ui.GlyphUp+" "+ui.Bytes(st.TotalSent)) + "   " + ui.Down.Render(ui.GlyphDown+" "+ui.Bytes(st.TotalReceived))},
	)
	if len(st.Rates.DownHist) > 1 {
		up, down := slices.Clone(st.Rates.UpHist), slices.Clone(st.Rates.DownHist)
		slices.Reverse(up)
		slices.Reverse(down)
		pairs = append(pairs, [2]string{"last 100 s", ui.Sparkline(up, 20, ui.Up) + "   " + ui.Sparkline(down, 20, ui.Down)})
	}
	a.kv(pairs...)
}

func hostsCmd(a *app) *cobra.Command {
	var active bool
	var sortBy string
	cmd := &cobra.Command{
		Use:   "hosts",
		Short: "List devices known to the box",
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			if active {
				hosts = slices.DeleteFunc(hosts, func(h fritz.Host) bool { return !h.Active })
			}
			fritz.SortHosts(hosts, sortBy)
			if a.asJSON {
				return a.printJSON(hosts)
			}
			rows := make([][]string, 0, len(hosts))
			for _, h := range hosts {
				speed := ""
				if h.SpeedMbit > 0 {
					speed = fmt.Sprintf("%d Mbit/s", h.SpeedMbit)
				}
				guest := ""
				if h.Guest {
					guest = "guest"
				}
				rows = append(rows, []string{ui.OnOff(h.Active), h.Name, h.IP, h.MAC, h.Interface, speed, guest})
			}
			a.printTable([]string{"", "NAME", "IP", "MAC", "INTERFACE", "SPEED", ""}, rows)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&active, "active", "a", false, "only active hosts")
	cmd.Flags().StringVar(&sortBy, "sort", "ip", "sort by ip|name")
	cmd.AddCommand(hostsRenameCmd(a))
	cmd.AddCommand(hostsAccessCmds(a)...)
	cmd.AddCommand(&cobra.Command{
		Use:   "wake <mac|ip|name>",
		Short: "Send a Wake-on-LAN packet via the box",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			b, err := a.box(ctx)
			if err != nil {
				return err
			}
			mac := args[0]
			if !looksLikeMAC(mac) {
				hosts, err := b.Hosts(ctx)
				if err != nil {
					return err
				}
				h, err := fritz.FindHost(hosts, mac)
				if err != nil {
					return err
				}
				mac = h.MAC
			}
			if err := b.WakeOnLAN(ctx, mac); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "magic packet sent to", mac)
			return nil
		},
	})
	return cmd
}

func looksLikeMAC(s string) bool {
	return len(s) == 17 && strings.Count(s, ":") == 5
}

func reconnectCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "reconnect",
		Short: "Drop and re-establish the internet connection (new external IP)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.destructive(cmd, yes, "Reconnect %s? The internet connection drops for a few seconds.", func(ctx context.Context, b *fritz.Box) error {
				return b.Reconnect(ctx)
			}, "reconnect triggered")
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask")
	return cmd
}

func rebootCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "reboot",
		Short: "Reboot the box",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.destructive(cmd, yes, "Reboot %s? Network and phones are down for ~2 minutes.", func(ctx context.Context, b *fritz.Box) error {
				return b.Reboot(ctx)
			}, "reboot triggered")
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask")
	return cmd
}

func (a *app) destructive(cmd *cobra.Command, yes bool, prompt string, f func(context.Context, *fritz.Box) error, done string) error {
	ctx, cancel := a.ctx(cmd)
	defer cancel()
	b, err := a.box(ctx)
	if err != nil {
		return err
	}
	if !confirm(a, yes, fmt.Sprintf(prompt, b.Name+" ("+b.Host+")")) {
		return fmt.Errorf("aborted")
	}
	if err := f(ctx, b); err != nil {
		return err
	}
	fmt.Fprintln(a.out, done)
	return nil
}
