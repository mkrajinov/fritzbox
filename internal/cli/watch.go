package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/mkrajinovic/fbctl/internal/callmon"
	"github.com/mkrajinovic/fbctl/internal/fritz"
	"github.com/mkrajinovic/fbctl/internal/ui"
	"github.com/mkrajinovic/fbctl/internal/watch"
)

const minWatchInterval = 5 * time.Second

func watchCmd(a *app) *cobra.Command {
	var interval time.Duration
	var noNotify bool
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Notify about new devices, internet outages, missed and incoming calls",
		Long: "Polls the selected router and shows a desktop notification (macOS: osascript,\n" +
			"Linux: notify-send) plus a timestamped line for: new devices (MAC never seen before),\n" +
			"internet down/up, missed calls, and incoming calls via the call monitor (#96*5*).\n" +
			"Known devices are remembered across restarts. Stop with ctrl+c.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if interval < minWatchInterval {
				return fmt.Errorf("--interval must be at least %s", minWatchInterval)
			}
			ctx := cmd.Context()
			cctx, cancel := a.ctx(cmd)
			b, err := a.box(cctx)
			cancel()
			if err != nil {
				return err
			}
			path, err := watch.DefaultKnownPath(b.Name)
			if err != nil {
				return err
			}
			state, err := watch.NewState(path)
			if err != nil {
				return err
			}
			w := &watcher{a: a, b: b, state: state, notifier: watch.SystemNotifier(), lastErr: map[string]string{}}
			if noNotify {
				w.notifier = watch.NopNotifier
			}
			return w.run(ctx, interval)
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 30*time.Second, "poll interval (minimum 5s)")
	cmd.Flags().BoolVar(&noNotify, "no-notify", false, "only print events, no desktop notifications")
	return cmd
}

type watcher struct {
	a        *app
	b        *fritz.Box
	state    *watch.State
	notifier watch.Notifier

	lastErr       map[string]string // per source, to avoid repeating the same error every poll
	notifyFailed  bool
	callmonWarned bool
}

func (w *watcher) run(ctx context.Context, interval time.Duration) error {
	events := make(chan callmon.Event, 16)
	errs := make(chan error, 1)
	go callmon.Watch(ctx, w.b.Host, events, errs)

	w.note(fmt.Sprintf("watching %s every %s (ctrl+c to stop)", w.b.Name, interval))
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			w.poll(ctx)
			timer.Reset(interval)
		case ev := <-events:
			if e, ok := watch.RingEvent(ev); ok {
				w.emit(ctx, e)
			}
		case err := <-errs:
			if !w.callmonWarned {
				w.callmonWarned = true
				w.note(fmt.Sprintf("call monitor unavailable, no incoming-call alerts until it connects: %v", err))
			}
		}
	}
}

func (w *watcher) poll(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, w.a.timeout*3)
	defer cancel()
	snap := watch.Snapshot{Time: time.Now()}

	hosts, err := w.b.Hosts(ctx)
	w.report(parent, "hosts", err)
	snap.HostsOK, snap.Hosts = err == nil, hosts

	st, err := w.b.Status(ctx)
	w.report(parent, "status", err)
	// Status joins the errors of all sub-calls; some (e.g. external IP) may fail exactly
	// when the WAN is down. The connection state is valid once GetStatusInfo answered.
	snap.StatusOK = err == nil || (st.HasWAN && st.ConnStatus != "")
	snap.HasWAN, snap.Connected = st.HasWAN, st.Connected

	calls, err := w.b.Calls(ctx, 1)
	w.report(parent, "calls", err)
	snap.CallsOK, snap.Calls = err == nil, calls

	evs, err := w.state.Update(snap)
	w.report(parent, "known devices", err)
	for _, e := range evs {
		w.emit(parent, e)
	}
}

// report prints a poll error once per distinct message, and a note when the source recovers.
func (w *watcher) report(ctx context.Context, src string, err error) {
	if ctx.Err() != nil {
		return
	}
	prev := w.lastErr[src]
	switch {
	case err == nil && prev != "":
		delete(w.lastErr, src)
		w.note(src + ": ok again")
	case err != nil && err.Error() != prev:
		w.lastErr[src] = err.Error()
		fmt.Fprintf(w.a.err, "%s  %s\n", ui.Muted.Render(stamp(time.Now())), ui.Bad.Render(src+": "+err.Error()))
	}
}

func (w *watcher) emit(ctx context.Context, e watch.Event) {
	if w.a.asJSON {
		_ = json.NewEncoder(w.a.out).Encode(e)
	} else {
		fmt.Fprintf(w.a.out, "%s  %s  %s\n", ui.Muted.Render(stamp(e.Time)), eventGlyph(e.Kind), e.Message)
	}
	if err := w.notifier.Notify(ctx, "fb", w.b.Name, e.Message); err != nil && !w.notifyFailed && ctx.Err() == nil {
		w.notifyFailed = true
		w.note("desktop notification failed (printing only): " + err.Error())
	}
}

func (w *watcher) note(msg string) {
	fmt.Fprintf(w.a.err, "%s  %s\n", ui.Muted.Render(stamp(time.Now())), ui.Muted.Render(msg))
}

func stamp(t time.Time) string { return t.Format("2006-01-02 15:04:05") }

func eventGlyph(k watch.Kind) string {
	switch k {
	case watch.KindNewDevice:
		return ui.Warn.Render(ui.GlyphHosts)
	case watch.KindWANDown:
		return ui.Bad.Render(ui.GlyphOff)
	case watch.KindWANUp:
		return ui.OK.Render(ui.GlyphOn)
	case watch.KindMissedCall:
		return ui.Bad.Render(ui.GlyphMissed)
	case watch.KindIncomingCall:
		return ui.Down.Render(ui.GlyphIn)
	}
	return " "
}
