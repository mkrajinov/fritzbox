package watch

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mkrajinov/fritzbox/internal/callmon"
	"github.com/mkrajinov/fritzbox/internal/fritz"
)

func kinds(evs []Event) []Kind {
	var out []Kind
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

func mustUpdate(t *testing.T, s *State, snap Snapshot) []Event {
	t.Helper()
	evs, err := s.Update(snap)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	return evs
}

func hostsSnap(macs ...string) Snapshot {
	var hs []fritz.Host
	for _, m := range macs {
		hs = append(hs, fritz.Host{MAC: m, Name: "dev-" + m, IP: "192.168.178.2"})
	}
	return Snapshot{HostsOK: true, Hosts: hs}
}

func TestFirstSnapshotSeedsOnly(t *testing.T) {
	s, _ := NewState("")
	evs := mustUpdate(t, s, Snapshot{
		HostsOK: true, Hosts: []fritz.Host{{MAC: "aa:bb:cc:00:00:01"}},
		StatusOK: true, HasWAN: true, Connected: false,
		CallsOK: true, Calls: []fritz.Call{{ID: 5, Type: fritz.CallMissed}},
	})
	if len(evs) != 0 {
		t.Fatalf("first snapshot emitted %v", kinds(evs))
	}
	if !s.Known("AA:BB:CC:00:00:01") {
		t.Fatal("MAC not seeded (case-insensitive)")
	}
}

func TestNewDevice(t *testing.T) {
	s, _ := NewState("")
	mustUpdate(t, s, hostsSnap("AA:00:00:00:00:01"))
	evs := mustUpdate(t, s, hostsSnap("aa:00:00:00:00:01", "AA:00:00:00:00:02", ""))
	if !slices.Equal(kinds(evs), []Kind{KindNewDevice}) {
		t.Fatalf("got %v", kinds(evs))
	}
	if evs[0].Host == nil || evs[0].Host.MAC != "AA:00:00:00:00:02" || !strings.Contains(evs[0].Message, "AA:00:00:00:00:02") {
		t.Fatalf("bad event %+v", evs[0])
	}
	if evs := mustUpdate(t, s, hostsSnap("AA:00:00:00:00:02")); len(evs) != 0 {
		t.Fatalf("re-alerted: %v", kinds(evs))
	}
}

func TestFailedHostPollIgnored(t *testing.T) {
	s, _ := NewState("")
	// A failed first poll must not seed: the next good poll seeds silently.
	mustUpdate(t, s, Snapshot{HostsOK: false})
	if evs := mustUpdate(t, s, hostsSnap("AA:00:00:00:00:01")); len(evs) != 0 {
		t.Fatalf("got %v", kinds(evs))
	}
	mustUpdate(t, s, Snapshot{HostsOK: false})
	if evs := mustUpdate(t, s, hostsSnap("AA:00:00:00:00:02")); len(evs) != 1 {
		t.Fatalf("got %v", kinds(evs))
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known", "home.json")
	s, err := NewState(path)
	if err != nil {
		t.Fatal(err)
	}
	mustUpdate(t, s, hostsSnap("AA:00:00:00:00:01"))
	mustUpdate(t, s, hostsSnap("AA:00:00:00:00:01", "AA:00:00:00:00:02"))

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 0700", di.Mode().Perm())
	}

	// Restart: both MACs come from the file. Device 02 is offline and missing from the
	// box's list on the first poll; when it returns it must not alert.
	s2, err := NewState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.SortedMACs(); !slices.Equal(got, []string{"AA:00:00:00:00:01", "AA:00:00:00:00:02"}) {
		t.Fatalf("loaded %v", got)
	}
	mustUpdate(t, s2, hostsSnap("AA:00:00:00:00:01"))
	if evs := mustUpdate(t, s2, hostsSnap("AA:00:00:00:00:01", "AA:00:00:00:00:02")); len(evs) != 0 {
		t.Fatalf("persisted device re-alerted: %v", kinds(evs))
	}
	// A truly new device still alerts after restart.
	if evs := mustUpdate(t, s2, hostsSnap("AA:00:00:00:00:03")); len(evs) != 1 {
		t.Fatalf("got %v", kinds(evs))
	}
	// No temp files left behind.
	ents, _ := os.ReadDir(filepath.Dir(path))
	if len(ents) != 1 {
		t.Errorf("dir has %d entries, want 1", len(ents))
	}
}

func TestCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.json")
	os.WriteFile(path, []byte("{"), 0o600)
	if _, err := NewState(path); err == nil {
		t.Fatal("expected error")
	}
}

func TestWANTransitions(t *testing.T) {
	s, _ := NewState("")
	st := func(ok, conn bool) Snapshot { return Snapshot{StatusOK: ok, HasWAN: true, Connected: conn} }
	steps := []struct {
		snap Snapshot
		want []Kind
	}{
		{st(true, true), nil},   // seed
		{st(true, true), nil},   // no change
		{st(false, false), nil}, // failed poll is not "down"
		{st(true, false), []Kind{KindWANDown}},
		{st(true, false), nil},
		{st(true, true), []Kind{KindWANUp}},
		{Snapshot{StatusOK: true, HasWAN: false}, nil}, // no WAN: ignored
	}
	for i, step := range steps {
		if got := kinds(mustUpdate(t, s, step.snap)); !slices.Equal(got, step.want) {
			t.Fatalf("step %d: got %v want %v", i, got, step.want)
		}
	}
}

func TestMissedCall(t *testing.T) {
	s, _ := NewState("")
	calls := []fritz.Call{{ID: 10, Type: fritz.CallMissed, Number: "0301"}, {ID: 11, Type: fritz.CallIn}}
	mustUpdate(t, s, Snapshot{CallsOK: true, Calls: calls})

	calls = append([]fritz.Call{
		{ID: 13, Type: fritz.CallMissed, Number: "0303", Name: "Bob"},
		{ID: 12, Type: fritz.CallOut},
	}, calls...)
	evs := mustUpdate(t, s, Snapshot{CallsOK: true, Calls: calls})
	if !slices.Equal(kinds(evs), []Kind{KindMissedCall}) || evs[0].Call.ID != 13 {
		t.Fatalf("got %+v", evs)
	}
	if evs[0].Message != "missed call from Bob (0303)" {
		t.Errorf("message %q", evs[0].Message)
	}
	// The cursor advances: the same missed call does not alert again.
	if evs := mustUpdate(t, s, Snapshot{CallsOK: true, Calls: calls}); len(evs) != 0 {
		t.Fatalf("re-alerted: %v", kinds(evs))
	}
	// Failed poll is ignored; two new missed calls arrive oldest first.
	mustUpdate(t, s, Snapshot{CallsOK: false})
	calls = append([]fritz.Call{{ID: 15, Type: fritz.CallMissed}, {ID: 14, Type: fritz.CallMissed}}, calls...)
	evs = mustUpdate(t, s, Snapshot{CallsOK: true, Calls: calls})
	if len(evs) != 2 || evs[0].Call.ID != 14 || evs[1].Call.ID != 15 {
		t.Fatalf("got %+v", evs)
	}
	if evs[0].Message != "missed call from unknown number" {
		t.Errorf("message %q", evs[0].Message)
	}
}

func TestRingEvent(t *testing.T) {
	ev, err := callmon.Parse("14.11.26 10:00:00;RING;0;01701234;5551;SIP0;", time.Local)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := RingEvent(ev)
	if !ok || e.Kind != KindIncomingCall || e.Message != "incoming call from 01701234 to 5551" {
		t.Fatalf("got %+v %v", e, ok)
	}
	if _, ok := RingEvent(callmon.Event{Kind: "CONNECT"}); ok {
		t.Fatal("CONNECT should not notify")
	}
}

func TestOsascriptArgs(t *testing.T) {
	args := OsascriptArgs("fb", `home "main"`, "new device: Bob's \"PC\" C:\\x\nline2\r")
	if len(args) != 2 || args[0] != "-e" {
		t.Fatalf("args %q", args)
	}
	want := `display notification "new device: Bob's \"PC\" C:\\x line2 " with title "fb" subtitle "home \"main\""`
	if args[1] != want {
		t.Fatalf("script\n got %s\nwant %s", args[1], want)
	}
	// Backslash before quote: escaping order matters.
	if got := AppleScriptString(`a\"b`); got != `"a\\\"b"` {
		t.Fatalf("got %s", got)
	}
}

func TestNotifySendArgs(t *testing.T) {
	got := NotifySendArgs("fb", "home", "-rf message")
	want := []string{"--app-name=fb", "--", "fb · home", "-rf message"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestKnownPath(t *testing.T) {
	cases := map[string]string{
		"home":     "home.json",
		"../etc/x": ".._etc_x.json",
		"..":       "_...json",
		"büro 2":   "b_ro_2.json",
	}
	for in, want := range cases {
		got := KnownPath("/d", in)
		if filepath.Dir(got) != "/d" || filepath.Base(got) != want {
			t.Errorf("KnownPath(%q) = %s, want /d/%s", in, got, want)
		}
	}
}
