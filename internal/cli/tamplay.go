package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mkrajinov/fritzbox/internal/fritz"
	"github.com/mkrajinov/fritzbox/internal/player"
	"github.com/mkrajinov/fritzbox/internal/ui"
)

func tamPlayCmd(a *app) *cobra.Command {
	var out string
	var keepNew bool
	cmd := &cobra.Command{
		Use: "play <tam:index>", Short: "Play a message (or save it with -o)",
		Long: "Downloads the recording and plays it with afplay (macOS) or paplay, aplay, ffplay or mpv\n" +
			"(Linux); $FB_PLAYER picks another player. The message is marked as heard afterwards\n" +
			"unless --keep-new is given.",
		Example: "  fb tam play 0:3\n  fb tam play 0:3 -o message.wav",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, i, err := parseMsgRef(args[0])
			if err != nil {
				return err
			}
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				rec, err := b.Recording(ctx, t, i)
				if err != nil {
					return err
				}
				m := rec.Message
				from := orDefault(m.Name, orDefault(m.Number, "unknown caller"))
				if out != "" {
					if !strings.Contains(out, ".") {
						out += rec.Ext
					}
					if err := os.WriteFile(out, rec.Data, 0o600); err != nil {
						return err
					}
					fmt.Fprintf(a.out, "saved %s (%s, %s, %d KB)\n", out, from, m.Date.Format("2006-01-02 15:04"), (len(rec.Data)+1023)/1024)
					return nil
				}
				fmt.Fprintf(a.err, "%s %s · %s · %s\n", ui.Muted.Render("playing"), from, m.Date.Format("2006-01-02 15:04"), m.Duration)
				// Playback takes as long as the message, not a request timeout.
				if err := player.PlayBytes(cmd.Context(), rec.Data, rec.Ext); err != nil {
					return err
				}
				if m.New && !keepNew {
					// ctx may have run out during a long message.
					mctx, cancel := context.WithTimeout(cmd.Context(), a.timeout)
					defer cancel()
					return b.MarkMessage(mctx, t, i, true)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "save the recording to this file instead of playing it")
	cmd.Flags().BoolVar(&keepNew, "keep-new", false, "don't mark the message as heard")
	return cmd
}
