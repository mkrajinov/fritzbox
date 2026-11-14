// Package watch turns periodic router snapshots into notification events.
//
// The core (State) is pure: feed it snapshots, get events back. Each data source
// (hosts, WAN status, call list) seeds itself on its first successful poll and only
// emits events afterwards, so a failed poll never looks like "all devices gone" or
// "internet down". Known device MACs are persisted so restarts don't re-alert.
package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/mkrajinovic/fbctl/internal/callmon"
	"github.com/mkrajinovic/fbctl/internal/fritz"
)

// Kind is the type of an event.
type Kind string

const (
	KindNewDevice    Kind = "new-device"
	KindWANDown      Kind = "wan-down"
	KindWANUp        Kind = "wan-up"
	KindMissedCall   Kind = "missed-call"
	KindIncomingCall Kind = "incoming-call"
)

// Event is one notification.
type Event struct {
	Time    time.Time   `json:"time"`
	Kind    Kind        `json:"kind"`
	Message string      `json:"message"`
	Host    *fritz.Host `json:"host,omitempty"`
	Call    *fritz.Call `json:"call,omitempty"`
}

// Snapshot is the result of one poll. Each source has its own OK flag: a source
// whose poll failed is ignored (neither seeds nor emits).
type Snapshot struct {
	Time time.Time

	HostsOK bool
	Hosts   []fritz.Host

	StatusOK  bool
	HasWAN    bool
	Connected bool

	CallsOK bool
	Calls   []fritz.Call
}

// KnownDevice is a persisted MAC.
type KnownDevice struct {
	Name      string    `json:"name,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
}

type knownFile struct {
	Devices map[string]KnownDevice `json:"devices"`
}

// State tracks what has been seen so far.
type State struct {
	path  string
	known map[string]KnownDevice

	hostsSeeded  bool
	statusSeeded bool
	callsSeeded  bool

	connected bool
	maxCallID int
}

// NewState loads known MACs from path (a missing file is fine). An empty path disables persistence.
func NewState(path string) (*State, error) {
	s := &State{path: path, known: map[string]KnownDevice{}}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f knownFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for mac, d := range f.Devices {
		if m := normMAC(mac); m != "" {
			s.known[m] = d
		}
	}
	return s, nil
}

// Known reports whether a MAC has been seen before.
func (s *State) Known(mac string) bool {
	_, ok := s.known[normMAC(mac)]
	return ok
}

func normMAC(mac string) string { return strings.ToUpper(strings.TrimSpace(mac)) }

// Update applies a snapshot and returns the resulting events. The first successful
// poll of each source only seeds state. The error is a persistence failure; events
// are still valid when it is non-nil.
func (s *State) Update(snap Snapshot) ([]Event, error) {
	now := snap.Time
	if now.IsZero() {
		now = time.Now()
	}
	var evs []Event
	var saveErr error

	if snap.HostsOK {
		changed := false
		for _, h := range snap.Hosts {
			mac := normMAC(h.MAC)
			if mac == "" {
				continue
			}
			if _, ok := s.known[mac]; ok {
				continue
			}
			s.known[mac] = KnownDevice{Name: h.Name, FirstSeen: now}
			changed = true
			if s.hostsSeeded {
				evs = append(evs, Event{Time: now, Kind: KindNewDevice, Message: "new device: " + describeHost(h), Host: &h})
			}
		}
		s.hostsSeeded = true
		if changed {
			saveErr = s.save()
		}
	}

	if snap.StatusOK && snap.HasWAN {
		if s.statusSeeded && snap.Connected != s.connected {
			if snap.Connected {
				evs = append(evs, Event{Time: now, Kind: KindWANUp, Message: "internet connection is back"})
			} else {
				evs = append(evs, Event{Time: now, Kind: KindWANDown, Message: "internet connection is down"})
			}
		}
		s.connected, s.statusSeeded = snap.Connected, true
	}

	if snap.CallsOK {
		mx := s.maxCallID
		// Oldest first so notifications arrive in call order.
		calls := slices.Clone(snap.Calls)
		slices.SortFunc(calls, func(a, b fritz.Call) int { return a.ID - b.ID })
		for _, c := range calls {
			if s.callsSeeded && c.Type == fritz.CallMissed && c.ID > s.maxCallID {
				evs = append(evs, Event{Time: now, Kind: KindMissedCall, Message: "missed call from " + describeCaller(c.Name, c.Number), Call: &c})
			}
			mx = max(mx, c.ID)
		}
		s.maxCallID, s.callsSeeded = mx, true
	}
	return evs, saveErr
}

// RingEvent converts a call monitor RING into an incoming-call event.
func RingEvent(e callmon.Event) (Event, bool) {
	if e.Kind != "RING" {
		return Event{}, false
	}
	t := e.Time
	if t.IsZero() {
		t = time.Now()
	}
	msg := "incoming call from " + describeCaller("", e.Caller)
	if e.Callee != "" {
		msg += " to " + e.Callee
	}
	return Event{Time: t, Kind: KindIncomingCall, Message: msg}, true
}

func describeHost(h fritz.Host) string {
	var extra []string
	if h.IP != "" {
		extra = append(extra, h.IP)
	}
	extra = append(extra, normMAC(h.MAC))
	if h.Guest {
		extra = append(extra, "guest")
	}
	name := h.Name
	if name == "" {
		name = "unnamed"
	}
	return name + " (" + strings.Join(extra, ", ") + ")"
}

func describeCaller(name, number string) string {
	switch {
	case number == "" && name == "":
		return "unknown number"
	case name == "":
		return number
	case number == "":
		return name
	}
	return name + " (" + number + ")"
}

// save writes the known MACs atomically (temp file + rename), 0600 in a 0700 directory.
func (s *State) save() error {
	if s.path == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(knownFile{Devices: s.known}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".known-*.json") // CreateTemp uses 0600
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// KnownPath returns <dir>/<router>.json with the router name made safe for a file name.
func KnownPath(dir, router string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, router)
	if strings.Trim(safe, ".") == "" {
		safe = "_" + safe
	}
	return filepath.Join(dir, safe+".json")
}

// DefaultKnownPath is os.UserConfigDir()/fbctl/known/<router>.json.
func DefaultKnownPath(router string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return KnownPath(filepath.Join(dir, "fbctl", "known"), router), nil
}

// Notifier shows a desktop notification.
type Notifier interface {
	Notify(ctx context.Context, title, subtitle, message string) error
}

// NotifierFunc adapts a function to Notifier.
type NotifierFunc func(ctx context.Context, title, subtitle, message string) error

func (f NotifierFunc) Notify(ctx context.Context, title, subtitle, message string) error {
	return f(ctx, title, subtitle, message)
}

// NopNotifier does nothing.
var NopNotifier Notifier = NotifierFunc(func(context.Context, string, string, string) error { return nil })

const notifyTimeout = 5 * time.Second

// SystemNotifier returns the platform notifier: osascript on macOS, notify-send on Linux,
// otherwise a no-op. Commands are executed directly, never through a shell.
func SystemNotifier() Notifier {
	switch runtime.GOOS {
	case "darwin":
		return NotifierFunc(func(ctx context.Context, title, subtitle, message string) error {
			return run(ctx, "osascript", OsascriptArgs(title, subtitle, message))
		})
	case "linux":
		return NotifierFunc(func(ctx context.Context, title, subtitle, message string) error {
			return run(ctx, "notify-send", NotifySendArgs(title, subtitle, message))
		})
	}
	return NopNotifier
}

func run(ctx context.Context, name string, args []string) error {
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// OsascriptArgs builds the argv for osascript: the whole AppleScript is one -e element.
func OsascriptArgs(title, subtitle, message string) []string {
	script := "display notification " + AppleScriptString(message) +
		" with title " + AppleScriptString(title) +
		" subtitle " + AppleScriptString(subtitle)
	return []string{"-e", script}
}

// AppleScriptString quotes s as an AppleScript string literal. Backslashes are escaped
// before quotes; CR/LF and other control characters become spaces.
func AppleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return `"` + s + `"`
}

// NotifySendArgs builds the argv for notify-send. "--" ends option parsing so a
// message starting with "-" is not taken as a flag.
func NotifySendArgs(title, subtitle, message string) []string {
	summary := title
	if subtitle != "" {
		summary += " · " + subtitle
	}
	return []string{"--app-name=fb", "--", summary, message}
}

// SortedMACs returns the known MACs (for tests and debugging).
func (s *State) SortedMACs() []string { return slices.Sorted(maps.Keys(s.known)) }
