package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mkrajinovic/fbctl/internal/config"
	"github.com/mkrajinovic/fbctl/internal/fakebox"
	"github.com/mkrajinovic/fbctl/internal/fritz"
	"github.com/mkrajinovic/fbctl/internal/watch"
)

type recSink struct {
	mu  sync.Mutex
	got []watch.Event
}

func (r *recSink) Name() string       { return "rec" }
func (r *recSink) WantsInitial() bool { return true }
func (r *recSink) Send(_ context.Context, e watch.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, e)
	return nil
}

func (r *recSink) wait(t *testing.T, n int) []watch.Event {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		r.mu.Lock()
		if len(r.got) >= n {
			out := slices.Clone(r.got)
			r.mu.Unlock()
			return out
		}
		r.mu.Unlock()
	}
	t.Fatalf("sink got %d events, want %d", len(r.got), n)
	return nil
}

func describe(evs []watch.Event) []string {
	var out []string
	for _, e := range evs {
		s := string(e.Kind) + ":" + e.Host.Name
		if e.Initial {
			s += ":initial"
		}
		out = append(out, s)
	}
	return out
}

func TestWatcherPresenceFlow(t *testing.T) {
	fb := fakebox.New()
	t.Cleanup(fb.Close)
	host, port := fb.HostPort()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, err := fritz.Connect(ctx, "home", &config.Router{Host: host, Port: port, WebPort: port, User: fakebox.User}, fakebox.Password, fritz.ConnectOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "home.json")
	state, _ := watch.NewState(path)
	state.AwayAfter = time.Nanosecond
	var out bytes.Buffer
	sink := &recSink{}
	w := &watcher{a: &app{out: &out, err: &out, timeout: 5 * time.Second}, b: b, state: state, notifier: watch.NopNotifier,
		lastErr: map[string]string{}, track: watch.Tracker{"anna-phone", "bob-phone"}, history: watch.HistoryPath(path)}
	w.sinks = watch.NewDispatcher(ctx, []watch.Sink{sink}, w.sinkReport)

	anna := [3]string{"AA:00:00:00:00:01", "anna-phone", "1"}
	tv := [3]string{"AA:00:00:00:00:02", "tv", "1"}
	fb.SetHostList(fakebox.HostListXML(anna, tv))
	w.poll(ctx) // seeds; publishes the current state of tracked devices once
	got := sink.wait(t, 1)
	if !slices.Equal(describe(got), []string{"arrived:anna-phone:initial"}) || got[0].Router != "home" {
		t.Fatalf("start: %v", describe(got))
	}

	// Anna leaves, the TV goes off (untracked: history only), Bob's new phone joins.
	anna[2], tv[2] = "0", "0"
	bob := [3]string{"AA:00:00:00:00:03", "bob-phone", "1"}
	fb.SetHostList(fakebox.HostListXML(anna, tv, bob))
	w.poll(ctx)
	got = sink.wait(t, 4)
	want := []string{"arrived:anna-phone:initial", "new-device:bob-phone", "left:anna-phone", "arrived:bob-phone:initial"}
	if !slices.Equal(describe(got), want) {
		t.Fatalf("sink got %v, want %v", describe(got), want)
	}
	printed := out.String()
	if strings.Count(printed, "bob-phone") != 1 || !strings.Contains(printed, "anna-phone left") || strings.Contains(printed, "tv left") {
		t.Fatalf("printed:\n%s", printed)
	}
	hist, _ := watch.ReadHistory(watch.HistoryPath(path), "", 0)
	var kinds []string
	for _, h := range hist {
		kinds = append(kinds, string(h.Kind)+":"+h.Name)
	}
	slices.Sort(kinds)
	if !slices.Equal(kinds, []string{"arrived:bob-phone", "left:anna-phone", "left:tv"}) {
		t.Fatalf("history %v", kinds)
	}
}
