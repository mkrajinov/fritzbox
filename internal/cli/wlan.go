package cli

import (
	"fmt"
	"strconv"

	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"

	"github.com/mkrajinovic/fbctl/internal/fritz"
	"github.com/mkrajinovic/fbctl/internal/ui"
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
	cmd.AddCommand(qr, wlanPasswordCmd(a))
	return cmd
}

func chanStr(c int) string {
	if c == 0 {
		return "auto"
	}
	return strconv.Itoa(c)
}
