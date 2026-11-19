package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"

	"github.com/mkrajinov/fritzbox/internal/fritz"
	"github.com/mkrajinov/fritzbox/internal/ui"
)

func wlanCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "wlan", Aliases: []string{"wifi"}, Short: "List and toggle WLANs, show QR codes"}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List WLANs",
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			if a.asJSON {
				return a.printJSON(ws)
			}
			rows := make([][]string, 0, len(ws))
			for _, w := range ws {
				ssid := w.SSID
				if w.Hidden {
					ssid += ui.Muted.Render(" (hidden)")
				}
				rows = append(rows, []string{ui.OnOff(w.Enabled), strconv.Itoa(w.Index), w.Label(), ssid, chanStr(w.Channel), w.Standard, w.Beacon, strconv.Itoa(w.Clients)})
			}
			a.printTable([]string{"", "#", "NETWORK", "SSID", "CHANNEL", "STANDARD", "SECURITY", "CLIENTS"}, rows)
			return nil
		},
	})
	for _, on := range []bool{true, false} {
		use, short := "off <2.4|5|6|guest|index>", "Disable a WLAN"
		if on {
			use, short = "on <2.4|5|6|guest|index>", "Enable a WLAN"
		}
		cmd.AddCommand(&cobra.Command{
			Use: use, Short: short, Args: cobra.ExactArgs(1),
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
				if err := b.SetWLAN(ctx, w.Index, on); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "%s (%s) %s\n", w.Label(), w.SSID, map[bool]string{true: ui.OK.Render("enabled"), false: ui.Muted.Render("disabled")}[on])
				return nil
			},
		})
	}
	var showPW bool
	var png string
	qr := &cobra.Command{
		Use: "qr <2.4|5|6|guest|index>", Short: "Show a join QR code for a WLAN", Args: cobra.ExactArgs(1),
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
			pw, err := b.WLANPassword(ctx, w.Index)
			if err != nil {
				return err
			}
			payload := fritz.WiFiQRPayload(w, pw)
			if png != "" {
				if err := qrcode.WriteFile(payload, qrcode.Medium, 512, png); err != nil {
					return err
				}
				fmt.Fprintln(a.out, "wrote", png)
				return nil
			}
			q, err := qrcode.New(payload, qrcode.Medium)
			if err != nil {
				return err
			}
			fmt.Fprint(a.out, q.ToSmallString(false))
			fmt.Fprintf(a.out, "%s  %s\n", ui.Bold.Render(w.SSID), ui.Muted.Render(w.Label()))
			if showPW {
				fmt.Fprintln(a.out, "password:", pw)
			}
			return nil
		},
	}
	qr.Flags().BoolVar(&showPW, "show-password", false, "also print the passphrase")
	qr.Flags().StringVar(&png, "png", "", "write the QR code to a PNG file instead")
	cmd.AddCommand(qr, wlanPasswordCmd(a), wlanClientsCmd(a))
	return cmd
}

func chanStr(c int) string {
	if c == 0 {
		return "auto"
	}
	return strconv.Itoa(c)
}

func wlanClientsCmd(a *app) *cobra.Command {
	var sortBy string
	cmd := &cobra.Command{
		Use: "clients", Aliases: []string{"devices"}, Short: "Devices on the box's WLANs with signal strength and link rate",
		Long: "Lists devices connected to the box's own radios with signal strength (0–100), current\n" +
			"rates in Mbit/s (TX = box to device, RX = device to box) and the best rate the link\n" +
			"supports. Devices on mesh repeaters are not reported by the box; they are listed by\n" +
			"name below the table.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if sortBy != "signal" && sortBy != "name" && sortBy != "wlan" {
				return fmt.Errorf("--sort must be signal, name or wlan")
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			b, err := a.box(ctx)
			if err != nil {
				return err
			}
			cs, err := b.WLANClients(ctx)
			if err != nil {
				return err
			}
			sortClients(cs, sortBy)
			if a.asJSON {
				return a.printJSON(cs)
			}
			rows := make([][]string, 0, len(cs))
			for _, c := range cs {
				net := c.Band + " GHz"
				if c.Guest {
					net = "Guest"
				} else if c.Band == "" {
					net = "WLAN " + strconv.Itoa(c.WLAN)
				}
				rate := fmt.Sprintf("%d / %d", c.Speed, c.SpeedRX)
				best := ""
				if c.SpeedMax > 0 {
					best = strconv.Itoa(c.SpeedMax)
				}
				rows = append(rows, []string{ui.Signal(c.Signal), fritz.SignalLabel(c.Signal), orDash(c.Name), c.IP, c.MAC, net, rate, best})
			}
			if len(rows) > 0 {
				a.printTable([]string{"SIGNAL", "", "NAME", "IP", "MAC", "NETWORK", "TX / RX MBIT/S", "MAX"}, rows)
			} else {
				fmt.Fprintln(a.out, ui.Muted.Render("no devices on the box's WLANs"))
			}
			if hosts, err := b.Hosts(ctx); err == nil {
				if rest := fritz.WiFiHostsNotOnBox(hosts, cs); len(rest) > 0 {
					names := make([]string, len(rest))
					for i, h := range rest {
						names[i] = orDash(h.Name)
					}
					fmt.Fprintln(a.out, ui.Muted.Render(fmt.Sprintf("\n%d more Wi-Fi devices are active but not on the box's radios (likely on a repeater, no signal data): %s",
						len(rest), strings.Join(names, ", "))))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sortBy, "sort", "wlan", "wlan (then strongest first), signal (weakest first) or name")
	return cmd
}

func sortClients(cs []fritz.WLANClient, by string) {
	switch by {
	case "signal":
		slices.SortStableFunc(cs, func(x, y fritz.WLANClient) int { return x.Signal - y.Signal })
	case "name":
		slices.SortStableFunc(cs, func(x, y fritz.WLANClient) int {
			return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
		})
	}
}
