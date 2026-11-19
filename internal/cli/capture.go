package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/mkrajinov/fritzbox/internal/capture"
	"github.com/mkrajinov/fritzbox/internal/fritz"
	"github.com/mkrajinov/fritzbox/internal/ui"
)

func captureCmd(a *app) *cobra.Command {
	var out string
	var hideDevices bool
	cmd := &cobra.Command{
		Use:   "capture",
		Short: "Save the box's answers to read-only requests, scrubbed, as test data",
		Long: "Calls a fixed list of read-only TR-064 actions (status, hosts, WLAN, calls, phonebook,\n" +
			"answering machine, …), downloads the lists they point to and writes everything to a\n" +
			"folder with session IDs, passwords, serial numbers, MAC addresses, public IPs, phone\n" +
			"numbers, contact names, email addresses and SSIDs replaced by placeholders. It then runs\n" +
			"fb's parsers over the data and reports what they found (README.txt).\n\n" +
			"Nothing on the box is changed. Review the folder before sharing it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd, func(_ context.Context, b *fritz.Box) error {
				dir := out
				if dir == "" {
					dir = fmt.Sprintf("fritzbox-capture-%s-%s", b.Name, time.Now().Format("20060102-1504"))
				}
				if _, err := os.Stat(dir); err == nil {
					return fmt.Errorf("%s already exists", dir)
				}
				// Many requests: allow more than one request timeout overall.
				ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
				defer cancel()
				res, err := capture.Run(ctx, b.TR, dir, capture.NewScrubber(hideDevices), func(step string) {
					if a.verbose {
						fmt.Fprintln(a.err, ui.Muted.Render(step))
					}
				})
				if err != nil {
					return err
				}
				abs, _ := filepath.Abs(dir)
				fmt.Fprintf(a.out, "captured %d actions and %d documents into %s\n\n", res.Calls, res.Docs, abs)
				for _, f := range res.Findings {
					fmt.Fprintln(a.out, "  "+f)
				}
				if len(res.Failed) > 0 {
					fmt.Fprintln(a.out, ui.Muted.Render(fmt.Sprintf("\n%d not captured (missing services or rights), see README.txt", len(res.Failed))))
				}
				fmt.Fprintln(a.out, ui.Warn.Render("\nReview the files before sharing them. Device names are kept unless --hide-device-names."))
				return nil
			})
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "folder to create (default fritzbox-capture-<router>-<date>)")
	cmd.Flags().BoolVar(&hideDevices, "hide-device-names", false, "also replace device and host names")
	return cmd
}
