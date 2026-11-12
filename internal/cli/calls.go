package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mkrajinovic/fbctl/internal/callmon"
	"github.com/mkrajinovic/fbctl/internal/fritz"
	"github.com/mkrajinovic/fbctl/internal/ui"
)

// CallGlyph renders a call type with its colour (incoming = down colour, outgoing = up colour).
func callGlyph(t int) string {
	switch t {
	case fritz.CallIn, fritz.CallActiveIn:
		return ui.Down.Render(ui.GlyphIn)
	case fritz.CallOut, fritz.CallActiveOut:
		return ui.Up.Render(ui.GlyphOut)
	case fritz.CallMissed, fritz.CallRejected:
		return ui.Bad.Render(ui.GlyphMissed)
	}
	return " "
}

func callsCmd(a *app) *cobra.Command {
	var days int
	var missed, in, out bool
	cmd := &cobra.Command{
		Use:   "calls",
		Short: "Show the call list",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			b, err := a.box(ctx)
			if err != nil {
				return err
			}
			var types []int
			if missed {
				types = append(types, fritz.CallMissed)
			}
			if in {
				types = append(types, fritz.CallIn)
			}
			if out {
				types = append(types, fritz.CallOut)
			}
			calls, err := b.Calls(ctx, days, types...)
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(calls)
			}
			rows := make([][]string, 0, len(calls))
			for _, c := range calls {
				dur := ""
				if c.Type != fritz.CallMissed {
					dur = fmt.Sprintf("%d:%02d", int(c.Duration.Hours()), int(c.Duration.Minutes())%60)
				}
				rows = append(rows, []string{callGlyph(c.Type), c.Date.Format("2006-01-02 15:04"), orDefault(c.Number, "unknown"), c.Name, dur, c.Device, c.Own})
			}
			a.printTable([]string{"", "DATE", "NUMBER", "NAME", "DURATION", "DEVICE", "OWN NUMBER"}, rows)
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVarP(&days, "days", "d", 0, "only the last N days (0 = all)")
	f.BoolVar(&missed, "missed", false, "only missed calls")
	f.BoolVar(&in, "in", false, "only received calls")
	f.BoolVar(&out, "out", false, "only outgoing calls")
	cmd.AddCommand(monitorCmd(a))
	return cmd
}

func monitorCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "monitor",
		Short: "Stream live call events (enable once with #96*5* on a phone)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			_, r, err := cfg.Resolve(a.routerName)
			if err != nil {
				return err
			}
			events := make(chan callmon.Event, 16)
			errs := make(chan error, 4)
			go callmon.Watch(cmd.Context(), r.Host, events, errs)
			fmt.Fprintln(a.err, ui.Muted.Render("watching "+r.Host+":1012, Ctrl-C to stop"))
			enc := json.NewEncoder(a.out)
			for {
				select {
				case <-cmd.Context().Done():
					return nil
				case err := <-errs:
					fmt.Fprintln(a.err, ui.Warn.Render(err.Error()))
				case e := <-events:
					if a.asJSON {
						_ = enc.Encode(e)
						continue
					}
					fmt.Fprintln(a.out, formatEvent(e))
				}
			}
		},
	}
}

func formatEvent(e callmon.Event) string {
	ts := ui.Muted.Render(e.Time.Format("15:04:05"))
	switch e.Kind {
	case "RING":
		return fmt.Sprintf("%s %s ring    %s → %s", ts, ui.Down.Render(ui.GlyphIn), orDefault(e.Caller, "unknown"), e.Callee)
	case "CALL":
		return fmt.Sprintf("%s %s call    %s → %s (ext %s)", ts, ui.Up.Render(ui.GlyphOut), e.Caller, e.Callee, e.Extension)
	case "CONNECT":
		return fmt.Sprintf("%s %s connect %s (ext %s)", ts, ui.OK.Render(ui.GlyphOn), e.Caller, e.Extension)
	case "DISCONNECT":
		return fmt.Sprintf("%s %s hangup  after %s", ts, ui.Muted.Render(ui.GlyphOff), e.Duration)
	}
	return ts + " " + e.Raw
}

func phonebookCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "phonebook", Aliases: []string{"pb"}, Short: "List phonebooks and contacts"}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List phonebooks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			b, err := a.box(ctx)
			if err != nil {
				return err
			}
			pbs, err := b.Phonebooks(ctx)
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(pbs)
			}
			rows := make([][]string, 0, len(pbs))
			for _, p := range pbs {
				rows = append(rows, []string{strconv.Itoa(p.ID), p.Name})
			}
			a.printTable([]string{"ID", "NAME"}, rows)
			return nil
		},
	})
	var search string
	show := &cobra.Command{
		Use: "show <id>", Short: "Show contacts of a phonebook", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("phonebook id must be a number (see `fb phonebook list`)")
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			b, err := a.box(ctx)
			if err != nil {
				return err
			}
			_, contacts, err := b.Contacts(ctx, id)
			if err != nil {
				return err
			}
			if search != "" {
				q := strings.ToLower(search)
				kept := contacts[:0]
				for _, c := range contacts {
					hit := strings.Contains(strings.ToLower(c.Name), q)
					for _, n := range c.Numbers {
						hit = hit || strings.Contains(n.Number, search)
					}
					if hit {
						kept = append(kept, c)
					}
				}
				contacts = kept
			}
			if a.asJSON {
				return a.printJSON(contacts)
			}
			var rows [][]string
			for _, c := range contacts {
				for i, n := range c.Numbers {
					name := c.Name
					if i > 0 {
						name = ""
					}
					rows = append(rows, []string{name, n.Number, ui.Muted.Render(n.Type)})
				}
			}
			a.printTable([]string{"NAME", "NUMBER", "TYPE"}, rows)
			return nil
		},
	}
	show.Flags().StringVarP(&search, "search", "s", "", "filter by name or number")
	cmd.AddCommand(show)
	return cmd
}
