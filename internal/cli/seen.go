package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/mkrajinov/fritzbox/internal/ui"
	"github.com/mkrajinov/fritzbox/internal/watch"
)

func seenCmd(a *app) *cobra.Command {
	var homeOnly bool
	var limit int
	cmd := &cobra.Command{
		Use:   "seen [device]",
		Short: "Who is home: arrivals and departures recorded by fb watch",
		Long: "Shows which devices are home and since when, from the data `fb watch` records\n" +
			"(it must be running to keep this current). With a device name or MAC it also lists\n" +
			"the latest arrivals and departures.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			name, _, err := cfg.Resolve(a.routerName)
			if err != nil {
				return err
			}
			path, err := watch.DefaultKnownPath(name)
			if err != nil {
				return err
			}
			p, err := watch.LoadPresence(path)
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("no presence data for %s yet: run `fb watch` (it records arrivals and departures)", name)
			}
			if err != nil {
				return err
			}
			now := time.Now()
			if len(args) == 1 {
				return a.seenOne(p, path, args[0], limit, now)
			}
			devs := slices.Clone(p.Devices)
			if homeOnly {
				devs = slices.DeleteFunc(devs, func(d watch.Presence) bool { return !d.Home })
			}
			// Home first, most recent change first.
			slices.SortStableFunc(devs, func(x, y watch.Presence) int {
				if x.Home != y.Home {
					if x.Home {
						return -1
					}
					return 1
				}
				return y.Since.Compare(x.Since)
			})
			if a.asJSON {
				return a.printJSON(map[string]any{"router": name, "updated": p.Updated, "stale": p.Stale(now), "devices": devs})
			}
			a.staleNote(p, now)
			rows := make([][]string, 0, len(devs))
			for _, d := range devs {
				last := ""
				if !d.Home && !d.LastSeen.IsZero() {
					last = ui.When(d.LastSeen, now)
				}
				rows = append(rows, []string{ui.Presence(d.Home, d.Since, now), orDash(d.Name), d.MAC, last})
			}
			a.printTable([]string{"STATE", "NAME", "MAC", "LAST SEEN"}, rows)
			return nil
		},
	}
	cmd.Flags().BoolVar(&homeOnly, "home", false, "only devices that are home")
	cmd.Flags().IntVarP(&limit, "number", "n", 10, "arrivals/departures to show for one device")
	return cmd
}

func (a *app) staleNote(p watch.PresenceFile, now time.Time) {
	if p.Stale(now) {
		fmt.Fprintln(a.err, ui.Warn.Render(fmt.Sprintf("fb watch last updated this %s ago (%s); the state may be out of date",
			ui.Ago(now.Sub(p.Updated)), ui.When(p.Updated, now))))
	}
}

func (a *app) seenOne(p watch.PresenceFile, path, q string, limit int, now time.Time) error {
	d, err := p.Find(q)
	if err != nil {
		return err
	}
	hist, err := watch.ReadHistory(watch.HistoryPath(path), d.MAC, limit)
	if err != nil {
		return err
	}
	if a.asJSON {
		return a.printJSON(map[string]any{"device": d, "history": hist, "updated": p.Updated, "stale": p.Stale(now)})
	}
	a.staleNote(p, now)
	a.kv(
		[2]string{"device", orDash(d.Name) + "  " + ui.Muted.Render(d.MAC)},
		[2]string{"state", ui.Presence(d.Home, d.Since, now)},
		[2]string{"last seen", map[bool]string{true: "now", false: ui.When(d.LastSeen, now)}[d.Home && !p.Stale(now)]},
		[2]string{"first seen", ui.When(d.FirstSeen, now)},
	)
	if len(hist) == 0 {
		return nil
	}
	fmt.Fprintln(a.out)
	for _, h := range hist {
		verb := ui.OK.Render("arrived")
		if h.Kind == watch.KindLeft {
			verb = ui.Muted.Render("left   ")
		}
		fmt.Fprintf(a.out, "  %s  %s\n", verb, h.Time.Local().Format("Mon 2006-01-02 15:04"))
	}
	return nil
}
