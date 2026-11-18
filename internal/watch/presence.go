package watch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mkrajinovic/fbctl/internal/fritz"
)

// DefaultAwayAfter is how long a device must stay inactive before it counts as gone.
const DefaultAwayAfter = 10 * time.Minute

// saveEvery is the heartbeat for writing the state file when nothing changed.
const saveEvery = 2 * time.Minute

// updatePresence applies the host list to the presence state of every known device.
// Devices missing from the list count as inactive. It returns arrive/leave events
// (the caller drops them on the seeding poll) and whether anything changed.
func (s *State) updatePresence(now time.Time, hosts []fritz.Host) ([]Event, bool) {
	active := map[string]fritz.Host{}
	for _, h := range hosts {
		mac := normMAC(h.MAC)
		if mac == "" {
			continue
		}
		if h.Active {
			active[mac] = h
		} else if _, ok := active[mac]; !ok {
			if d, ok := s.known[mac]; ok && h.Name != "" && h.Name != d.Name {
				d.Name = h.Name // keep renames
				s.known[mac] = d
			}
		}
	}
	var evs []Event
	changed := false
	for _, mac := range slices.Sorted(maps.Keys(s.known)) {
		d := s.known[mac]
		if h, ok := active[mac]; ok {
			if h.Name != "" {
				d.Name = h.Name
			}
			d.LastSeen = now
			if !d.Home {
				d.Home, d.Since, changed = true, now, true
				h := h
				evs = append(evs, Event{Time: now, Kind: KindArrived, Since: now, Host: &h, Message: nameOr(d.Name, mac) + " is home"})
			}
		} else if d.Home {
			if d.LastSeen.IsZero() {
				d.LastSeen = now // state from an older file: start the timer now
			}
			if now.Sub(d.LastSeen) >= s.AwayAfter {
				d.Home, d.Since, changed = false, d.LastSeen, true
				h := fritz.Host{MAC: mac, Name: d.Name}
				evs = append(evs, Event{Time: now, Kind: KindLeft, Since: d.LastSeen, Host: &h,
					Message: fmt.Sprintf("%s left (last seen %s)", nameOr(d.Name, mac), d.LastSeen.Format("15:04"))})
			}
		}
		s.known[mac] = d
	}
	return evs, changed
}

func nameOr(name, mac string) string {
	if name == "" {
		return mac
	}
	return name
}

// Presence is one device's presence as stored by `fb watch`.
type Presence struct {
	MAC string `json:"mac"`
	KnownDevice
}

// PresenceFile is the stored state of one router.
type PresenceFile struct {
	Updated time.Time     `json:"updated"` // last successful host poll by fb watch
	Every   time.Duration `json:"-"`       // poll interval of that fb watch
	Devices []Presence    `json:"devices"`
}

// LoadPresence reads the state written by `fb watch`. A missing file returns fs.ErrNotExist.
func LoadPresence(path string) (PresenceFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PresenceFile{}, err
	}
	var f knownFile
	if err := json.Unmarshal(data, &f); err != nil {
		return PresenceFile{}, fmt.Errorf("%s: %w", path, err)
	}
	out := PresenceFile{Updated: f.Updated, Every: time.Duration(f.Every) * time.Second}
	for mac, d := range f.Devices {
		out.Devices = append(out.Devices, Presence{MAC: normMAC(mac), KnownDevice: d})
	}
	slices.SortFunc(out.Devices, func(a, b Presence) int { return strings.Compare(a.MAC, b.MAC) })
	return out, nil
}

// Stale reports whether the data is too old to describe the present: fb watch is not running.
func (p PresenceFile) Stale(now time.Time) bool {
	return now.Sub(p.Updated) > 2*max(saveEvery, p.Every)+time.Minute
}

// Find matches a MAC or (case-insensitive) name.
func (p PresenceFile) Find(q string) (Presence, error) {
	var hits []Presence
	for _, d := range p.Devices {
		if strings.EqualFold(d.MAC, q) || strings.EqualFold(d.Name, q) {
			hits = append(hits, d)
		}
	}
	switch len(hits) {
	case 0:
		return Presence{}, fmt.Errorf("no device %q in the presence data", q)
	case 1:
		return hits[0], nil
	}
	return Presence{}, fmt.Errorf("%q matches %d devices; use the MAC address", q, len(hits))
}

// HistoryEntry is one arrival or departure.
type HistoryEntry struct {
	Time time.Time `json:"time"` // when it happened (for "left": last seen)
	MAC  string    `json:"mac"`
	Name string    `json:"name,omitempty"`
	Kind Kind      `json:"kind"`
}

// HistoryPath is the per-router arrivals/departures log next to the known-devices file.
func HistoryPath(knownPath string) string {
	return strings.TrimSuffix(knownPath, ".json") + ".history.jsonl"
}

const (
	historyMax  = 1 << 20 // trim the log above 1 MiB …
	historyKeep = 4000    // … to the newest entries
)

// AppendHistory appends the presence events to the log (0600) and trims it when it grows too big.
func AppendHistory(path string, evs []Event) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range evs {
		if (e.Kind != KindArrived && e.Kind != KindLeft) || e.Host == nil || e.Initial {
			continue
		}
		if err := enc.Encode(HistoryEntry{Time: e.Since, MAC: normMAC(e.Host.MAC), Name: e.Host.Name, Kind: e.Kind}); err != nil {
			return err
		}
	}
	if buf.Len() == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(buf.Bytes())
	st, serr := f.Stat()
	if err := errors.Join(werr, serr, f.Close()); err != nil {
		return err
	}
	if st.Size() > historyMax {
		return trimHistory(path)
	}
	return nil
}

func trimHistory(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(lines) > historyKeep {
		lines = lines[len(lines)-historyKeep:]
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bytes.Join(lines, nil), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadHistory returns the entries for one MAC (all when mac is empty), newest first.
func ReadHistory(path, mac string, limit int) ([]HistoryEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []HistoryEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e HistoryEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue // a torn last line after a crash
		}
		if mac == "" || e.MAC == normMAC(mac) {
			out = append(out, e)
		}
	}
	slices.Reverse(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, sc.Err()
}

// Tracker selects the devices whose arrivals and departures are announced.
// Entries are names (case-insensitive) or MACs; "*" tracks everything.
type Tracker []string

// Match reports whether the event's device is tracked.
func (t Tracker) Match(h *fritz.Host) bool {
	if h == nil {
		return false
	}
	for _, q := range t {
		q = strings.TrimSpace(q)
		if q == "*" || strings.EqualFold(q, h.MAC) || (h.Name != "" && strings.EqualFold(q, h.Name)) {
			return true
		}
	}
	return false
}

// CurrentPresence returns synthetic "initial" events with the present state of tracked
// devices, for retained MQTT topics after a restart.
func (s *State) CurrentPresence(t Tracker, now time.Time) []Event {
	var evs []Event
	for _, mac := range slices.Sorted(maps.Keys(s.known)) {
		d := s.known[mac]
		h := fritz.Host{MAC: mac, Name: d.Name}
		if !t.Match(&h) {
			continue
		}
		k := KindLeft
		if d.Home {
			k = KindArrived
		}
		evs = append(evs, Event{Time: now, Kind: k, Since: d.Since, Host: &h, Initial: true, Message: nameOr(d.Name, mac)})
	}
	return evs
}
