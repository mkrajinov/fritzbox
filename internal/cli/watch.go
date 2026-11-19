package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/mkrajinov/fritzbox/internal/callmon"
	"github.com/mkrajinov/fritzbox/internal/fritz"
	"github.com/mkrajinov/fritzbox/internal/ui"
	"github.com/mkrajinov/fritzbox/internal/watch"
)

const minWatchInterval = 5 * time.Second

func watchCmd(a *app) *cobra.Command {
	var interval, awayAfter time.Duration
	var noNotify bool
	var track []string
	var webhook, ntfy, mqttURL, mqttPrefix string
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Notify about new devices, internet outages, missed and incoming calls",
		Long: "Polls the selected router and shows a desktop notification (macOS: osascript,\n" +
			"Linux: notify-send) plus a timestamped line for: new devices (MAC never seen before),\n" +
			"internet down/up, missed calls, and incoming calls via the call monitor (#96*5*).\n" +
			"Known devices are remembered across restarts. Stop with ctrl+c.\n\n" +
			"Presence: every device's arrivals and departures are recorded for `fb seen`. A device\n" +
			"counts as gone after --away-after without activity (phones sleep on Wi-Fi). Only\n" +
			"devices named with --track are announced. Phones with a private (rotating) Wi-Fi\n" +
			"address get a new MAC per network or after a while; turn that off for your home WLAN.\n\n" +
			"Forwarding (each optional, also from the environment so secrets stay out of `ps`):\n" +
			"  --webhook URL   or FB_WEBHOOK_URL   POST every event as JSON (Home Assistant webhook)\n" +
			"  --ntfy URL      or FB_NTFY_URL      push via ntfy (token: FB_NTFY_TOKEN)\n" +
			"  --mqtt URL      or FB_MQTT_URL      mqtt://user@host:1883 (password: FB_MQTT_PASSWORD)\n" +
			"MQTT topics: <prefix>/<router>/event (JSON), <prefix>/<router>/presence/<device>\n" +
			"(home|not_home, retained, matches Home Assistant's device_tracker) and\n" +
			"<prefix>/<router>/internet (online|offline, retained).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if interval < minWatchInterval {
				return fmt.Errorf("--interval must be at least %s", minWatchInterval)
			}
			if awayAfter < interval {
				return fmt.Errorf("--away-after must be at least the poll interval (%s)", interval)
			}
			sinks, err := buildSinks(orEnv(webhook, "FB_WEBHOOK_URL"), orEnv(ntfy, "FB_NTFY_URL"), orEnv(mqttURL, "FB_MQTT_URL"), mqttPrefix)
			if err != nil {
				return err
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
			state.AwayAfter, state.Interval = awayAfter, interval
			w := &watcher{a: a, b: b, state: state, notifier: watch.SystemNotifier(), lastErr: map[string]string{},
				track: watch.Tracker(track), history: watch.HistoryPath(path)}
			if len(sinks) > 0 {
				w.sinks = watch.NewDispatcher(ctx, sinks, w.sinkReport)
				names := make([]string, len(sinks))
				for i, s := range sinks {
					names[i] = s.Name()
				}
				w.note("forwarding events to " + strings.Join(names, ", "))
			}
			if noNotify {
				w.notifier = watch.NopNotifier
			}
			return w.run(ctx, interval)
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 30*time.Second, "poll interval (minimum 5s)")
	cmd.Flags().BoolVar(&noNotify, "no-notify", false, "only print events, no desktop notifications")
	cmd.Flags().StringSliceVar(&track, "track", nil, "announce arrivals/departures of these devices (names or MACs, comma-separated; * = all)")
	cmd.Flags().DurationVar(&awayAfter, "away-after", watch.DefaultAwayAfter, "inactive time before a device counts as gone")
	cmd.Flags().StringVar(&webhook, "webhook", "", "POST events as JSON to this URL (default $FB_WEBHOOK_URL)")
	cmd.Flags().StringVar(&ntfy, "ntfy", "", "ntfy topic URL, e.g. https://ntfy.sh/<topic> (default $FB_NTFY_URL)")
	cmd.Flags().StringVar(&mqttURL, "mqtt", "", "MQTT broker, mqtt://user@host:1883 or mqtts:// (default $FB_MQTT_URL)")
	cmd.Flags().StringVar(&mqttPrefix, "mqtt-prefix", "fb", "MQTT topic prefix")
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

	track       watch.Tracker
	history     string // arrivals/departures log
	sinks       *watch.Dispatcher
	initialSent bool
	dropWarned  bool

	sinkMu  sync.Mutex
	sinkErr map[string]string
	outMu   sync.Mutex // the dispatcher goroutine prints too
}

func orEnv(v, env string) string {
	if v != "" {
		return v
	}
	return os.Getenv(env)
}

func buildSinks(webhook, ntfy, mqttURL, prefix string) ([]watch.Sink, error) {
	var out []watch.Sink
	if webhook != "" {
		s, err := watch.NewWebhook(webhook)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if ntfy != "" {
		s, err := watch.NewNtfy(ntfy, os.Getenv("FB_NTFY_TOKEN"))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if mqttURL != "" {
		s, err := watch.NewMQTT(mqttURL, os.Getenv("FB_MQTT_PASSWORD"), prefix)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// sinkReport runs on the dispatcher goroutine: print a sink error once, and when it recovers.
func (w *watcher) sinkReport(sink string, err error) {
	w.sinkMu.Lock()
	defer w.sinkMu.Unlock()
	if w.sinkErr == nil {
		w.sinkErr = map[string]string{}
	}
	prev := w.sinkErr[sink]
	switch {
	case err == nil && prev != "":
		delete(w.sinkErr, sink)
		w.note(sink + ": ok again")
	case err != nil && err.Error() != prev:
		w.sinkErr[sink] = err.Error()
		w.note(sink + " failed: " + err.Error())
	}
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
	if err := watch.AppendHistory(w.history, evs); err != nil {
		w.report(parent, "presence history", err)
	}
	// A device the box has never shown before is announced as "new device"; its first
	// arrival only updates the retained MQTT state instead of notifying twice.
	fresh := map[string]bool{}
	for _, e := range evs {
		if e.Kind == watch.KindNewDevice && e.Host != nil {
			fresh[strings.ToUpper(e.Host.MAC)] = true
		}
	}
	for i, e := range evs {
		if e.Kind == watch.KindArrived && e.Host != nil && fresh[strings.ToUpper(e.Host.MAC)] {
			evs[i].Initial = true
		}
	}
	if snap.HostsOK && !w.initialSent && w.sinks != nil {
		// Retained MQTT states for tracked devices, so Home Assistant knows them right away.
		w.initialSent = true
		for _, e := range w.state.CurrentPresence(w.track, snap.Time) {
			e.Router = w.b.Name
			w.forward(e)
		}
	}
	for _, e := range evs {
		if (e.Kind == watch.KindArrived || e.Kind == watch.KindLeft) && !w.track.Match(e.Host) {
			continue // recorded in the history, not announced
		}
		if e.Initial {
			if w.sinks != nil {
				e.Router = w.b.Name
				w.forward(e)
			}
			continue
		}
		w.emit(parent, e)
	}
}

func (w *watcher) forward(e watch.Event) {
	if !w.sinks.Send(e) && !w.dropWarned {
		w.dropWarned = true
		w.note("forwarding queue full, dropping events")
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
		w.outMu.Lock()
		defer w.outMu.Unlock()
		fmt.Fprintf(w.a.err, "%s  %s\n", ui.Muted.Render(stamp(time.Now())), ui.Bad.Render(src+": "+err.Error()))
	}
}

func (w *watcher) emit(ctx context.Context, e watch.Event) {
	e.Router = w.b.Name
	if w.sinks != nil {
		w.forward(e)
	}
	w.outMu.Lock()
	if w.a.asJSON {
		_ = json.NewEncoder(w.a.out).Encode(e)
	} else {
		fmt.Fprintf(w.a.out, "%s  %s  %s\n", ui.Muted.Render(stamp(e.Time)), eventGlyph(e.Kind), e.Message)
	}
	w.outMu.Unlock()
	if err := w.notifier.Notify(ctx, "fb", w.b.Name, e.Message); err != nil && !w.notifyFailed && ctx.Err() == nil {
		w.notifyFailed = true
		w.note("desktop notification failed (printing only): " + err.Error())
	}
}

func (w *watcher) note(msg string) {
	w.outMu.Lock()
	defer w.outMu.Unlock()
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
	case watch.KindArrived:
		return ui.OK.Render(ui.GlyphHosts)
	case watch.KindLeft:
		return ui.Muted.Render(ui.GlyphHosts)
	}
	return " "
}
