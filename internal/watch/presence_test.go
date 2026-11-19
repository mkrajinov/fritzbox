package watch

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mkrajinov/fritzbox/internal/fritz"
)

var t0 = time.Date(2026, 11, 18, 18, 0, 0, 0, time.UTC)

func phone(active bool) Snapshot {
	return Snapshot{HostsOK: true, Hosts: []fritz.Host{{MAC: "aa:00:00:00:00:01", Name: "anna-phone", Active: active}}}
}

func at(s Snapshot, d time.Duration) Snapshot { s.Time = t0.Add(d); return s }

func TestPresenceArriveLeave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "home.json")
	s, _ := NewState(path)
	s.AwayAfter = 10 * time.Minute

	if evs := mustUpdate(t, s, at(phone(true), 0)); len(evs) != 0 {
		t.Fatalf("seed emitted %v", kinds(evs))
	}
	// Brief inactivity (phone asleep) is not a departure.
	if evs := mustUpdate(t, s, at(phone(false), 5*time.Minute)); len(evs) != 0 {
		t.Fatalf("flap emitted %v", kinds(evs))
	}
	mustUpdate(t, s, at(phone(true), 6*time.Minute))
	evs := mustUpdate(t, s, at(phone(false), 17*time.Minute))
	if !slices.Equal(kinds(evs), []Kind{KindLeft}) {
		t.Fatalf("got %v", kinds(evs))
	}
	if !evs[0].Since.Equal(t0.Add(6*time.Minute)) || !strings.Contains(evs[0].Message, "anna-phone left (last seen 18:06)") {
		t.Fatalf("left event %+v", evs[0])
	}
	// Missing from the list entirely counts as inactive too; no second "left".
	if evs := mustUpdate(t, s, at(Snapshot{HostsOK: true}, 30*time.Minute)); len(evs) != 0 {
		t.Fatalf("got %v", kinds(evs))
	}
	evs = mustUpdate(t, s, at(phone(true), 40*time.Minute))
	if !slices.Equal(kinds(evs), []Kind{KindArrived}) || evs[0].Message != "anna-phone is home" {
		t.Fatalf("got %+v", evs)
	}

	p, err := LoadPresence(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := p.Find("ANNA-PHONE")
	if err != nil || !d.Home || !d.Since.Equal(t0.Add(40*time.Minute)) || !p.Updated.Equal(t0.Add(40*time.Minute)) {
		t.Fatalf("stored %+v %v (updated %v)", d, err, p.Updated)
	}
	if !p.Stale(t0.Add(2*time.Hour)) || p.Stale(t0.Add(41*time.Minute)) {
		t.Error("staleness")
	}
}

func TestPresenceRestartIsSilent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "home.json")
	s, _ := NewState(path)
	mustUpdate(t, s, at(phone(true), 0))
	// fb watch stopped for a day; the phone is gone now. The new process must not
	// announce anything on its first poll, but the state is corrected.
	s2, _ := NewState(path)
	if evs := mustUpdate(t, s2, at(phone(false), 24*time.Hour)); len(evs) != 0 {
		t.Fatalf("restart emitted %v", kinds(evs))
	}
	p, _ := LoadPresence(path)
	if d, _ := p.Find("anna-phone"); d.Home || !d.Since.Equal(t0) {
		t.Fatalf("after restart %+v", d)
	}
}

func TestOldKnownFileLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "home.json")
	writeFile(t, path, `{"devices":{"AA:00:00:00:00:01":{"name":"x","first_seen":"2026-11-01T00:00:00Z"}}}`)
	s, err := NewState(path)
	if err != nil || !s.Known("aa:00:00:00:00:01") {
		t.Fatal(err)
	}
}

func TestHistory(t *testing.T) {
	path := HistoryPath(filepath.Join(t.TempDir(), "home.json"))
	if !strings.HasSuffix(path, "home.history.jsonl") {
		t.Fatal(path)
	}
	h := &fritz.Host{MAC: "aa:00:00:00:00:01", Name: "anna-phone"}
	evs := []Event{
		{Kind: KindArrived, Since: t0, Host: h},
		{Kind: KindNewDevice, Host: h},
		{Kind: KindLeft, Since: t0.Add(time.Hour), Host: h},
		{Kind: KindArrived, Since: t0, Host: &fritz.Host{MAC: "bb:00:00:00:00:02"}},
		{Kind: KindArrived, Since: t0, Host: h, Initial: true},
	}
	if err := AppendHistory(path, evs); err != nil {
		t.Fatal(err)
	}
	got, err := ReadHistory(path, "AA:00:00:00:00:01", 10)
	if err != nil || len(got) != 2 || got[0].Kind != KindLeft || got[1].Kind != KindArrived {
		t.Fatalf("%+v %v", got, err)
	}
	if all, _ := ReadHistory(path, "", 1); len(all) != 1 || all[0].MAC != "BB:00:00:00:00:02" {
		t.Fatalf("%+v", all)
	}
	if none, err := ReadHistory(path+".missing", "", 0); none != nil || err != nil {
		t.Fatal("missing file")
	}
}

func TestTracker(t *testing.T) {
	tr := Tracker{"Anna-Phone", "bb:00:00:00:00:02"}
	if !tr.Match(&fritz.Host{Name: "anna-phone"}) || !tr.Match(&fritz.Host{MAC: "BB:00:00:00:00:02"}) || tr.Match(&fritz.Host{Name: "tv"}) || tr.Match(nil) {
		t.Fatal("match")
	}
	if !(Tracker{"*"}).Match(&fritz.Host{Name: "tv"}) || (Tracker{}).Match(&fritz.Host{Name: "tv"}) {
		t.Fatal("wildcard/empty")
	}
	s, _ := NewState("")
	mustUpdate(t, s, at(phone(true), 0))
	init := s.CurrentPresence(Tracker{"anna-phone"}, t0)
	if len(init) != 1 || init[0].Kind != KindArrived || !init[0].Initial {
		t.Fatalf("%+v", init)
	}
}

func TestWebhookAndNtfy(t *testing.T) {
	var mu sync.Mutex
	got := map[string]*http.Request{}
	bodies := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got[r.URL.Path], bodies[r.URL.Path] = r, string(b)
		mu.Unlock()
		if r.URL.Path == "/fail" {
			http.Error(w, "nope", http.StatusForbidden)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	e := Event{Router: "home", Kind: KindArrived, Message: "anna-phone is home", Host: &fritz.Host{Name: "anna-phone"}}

	wh, err := NewWebhook(srv.URL + "/api/webhook/secret-id")
	if err != nil {
		t.Fatal(err)
	}
	if err := wh.Send(ctx, e); err != nil {
		t.Fatal(err)
	}
	var back Event
	if json.Unmarshal([]byte(bodies["/api/webhook/secret-id"]), &back) != nil || back.Kind != KindArrived || back.Router != "home" {
		t.Fatalf("webhook body %q", bodies["/api/webhook/secret-id"])
	}

	n, _ := NewNtfy(srv.URL+"/fb-alerts", "tk_123")
	if err := n.Send(ctx, e); err != nil {
		t.Fatal(err)
	}
	r := got["/fb-alerts"]
	if bodies["/fb-alerts"] != "anna-phone is home" || r.Header.Get("Title") != "fb · home" || r.Header.Get("Tags") != "house" || r.Header.Get("Authorization") != "Bearer tk_123" {
		t.Fatalf("ntfy %q %v", bodies["/fb-alerts"], r.Header)
	}

	bad, _ := NewWebhook(srv.URL + "/fail")
	if err := bad.Send(ctx, e); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("status error: %v", err)
	}
	// Connection errors must not print the (secret) URL.
	gone, _ := NewWebhook("http://127.0.0.1:1/api/webhook/secret-id?token=abc")
	if err := gone.Send(ctx, e); err == nil || strings.Contains(err.Error(), "secret-id") || strings.Contains(err.Error(), "abc") {
		t.Fatalf("leaky error: %v", err)
	}
	if _, err := NewWebhook("ftp://x"); err == nil {
		t.Error("ftp accepted")
	}
}

func TestNewMQTT(t *testing.T) {
	m, err := NewMQTT("mqtt://ha@broker.lan", "pw", "")
	if err != nil || m.Addr != "broker.lan:1883" || m.User != "ha" || m.Password != "pw" || m.Prefix != "fb" || m.TLS {
		t.Fatalf("%+v %v", m, err)
	}
	if m, _ := NewMQTT("mqtts://broker.lan", "", "home/fb/"); m.Addr != "broker.lan:8883" || !m.TLS || m.Prefix != "home/fb" {
		t.Fatalf("%+v", m)
	}
	if _, err := NewMQTT("mqtt://ha:pw@broker.lan", "", ""); err == nil || !strings.Contains(err.Error(), "FB_MQTT_PASSWORD") {
		t.Fatalf("password in URL: %v", err)
	}
	if _, err := NewMQTT("http://broker.lan", "", ""); err == nil {
		t.Fatal("http accepted")
	}
}

func TestMQTTMessages(t *testing.T) {
	m := &MQTT{Prefix: "fb"}
	msgs := m.Messages(Event{Router: "Home Box", Kind: KindLeft, Host: &fritz.Host{Name: "Anna's Phone"}})
	if len(msgs) != 2 || msgs[0].Topic != "fb/home_box/event" || msgs[0].Retain ||
		msgs[1].Topic != "fb/home_box/presence/anna_s_phone" || string(msgs[1].Payload) != "not_home" || !msgs[1].Retain {
		t.Fatalf("%+v", msgs)
	}
	init := m.Messages(Event{Router: "home", Kind: KindArrived, Initial: true, Host: &fritz.Host{MAC: "AA:00"}})
	if len(init) != 1 || init[0].Topic != "fb/home/presence/aa_00" || string(init[0].Payload) != "home" {
		t.Fatalf("%+v", init)
	}
	wan := m.Messages(Event{Router: "home", Kind: KindWANDown})
	if wan[1].Topic != "fb/home/internet" || string(wan[1].Payload) != "offline" {
		t.Fatalf("%+v", wan)
	}
}

// fakeBroker accepts one MQTT connection and records CONNECT and PUBLISH packets.
type fakeBroker struct {
	ln       net.Listener
	rc       byte
	mu       sync.Mutex
	user, pw string
	pubs     []MQTTMessage
	done     chan struct{}
}

func newFakeBroker(t *testing.T, rc byte) *fakeBroker {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeBroker{ln: ln, rc: rc, done: make(chan struct{})}
	t.Cleanup(func() { ln.Close() })
	go b.serve()
	return b
}

func readPacket(r *bufio.Reader) (byte, []byte, error) {
	h, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n, mult := 0, 1
	for {
		c, err := r.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		n += int(c&0x7f) * mult
		mult *= 128
		if c&0x80 == 0 {
			break
		}
	}
	body := make([]byte, n)
	_, err = io.ReadFull(r, body)
	return h, body, err
}

func str(b []byte) (string, []byte) {
	n := int(binary.BigEndian.Uint16(b))
	return string(b[2 : 2+n]), b[2+n:]
}

func (b *fakeBroker) serve() {
	defer close(b.done)
	c, err := b.ln.Accept()
	if err != nil {
		return
	}
	defer c.Close()
	r := bufio.NewReader(c)
	for {
		h, body, err := readPacket(r)
		if err != nil {
			return
		}
		switch h & 0xF0 {
		case 0x10:
			proto, rest := str(body)
			flags := rest[1]
			_, rest = str(rest[4:]) // client id
			b.mu.Lock()
			if proto == "MQTT" && flags&0x80 != 0 {
				b.user, rest = str(rest)
				if flags&0x40 != 0 {
					b.pw, _ = str(rest)
				}
			}
			b.mu.Unlock()
			c.Write([]byte{0x20, 2, 0, b.rc})
		case 0x30:
			topic, payload := str(body)
			b.mu.Lock()
			b.pubs = append(b.pubs, MQTTMessage{topic, payload, h&1 == 1})
			b.mu.Unlock()
		case 0xE0:
			return
		}
	}
}

func TestMQTTSend(t *testing.T) {
	b := newFakeBroker(t, 0)
	m, _ := NewMQTT("mqtt://ha@"+b.ln.Addr().String(), "pw", "fb")
	if err := m.Send(context.Background(), Event{Router: "home", Kind: KindArrived, Host: &fritz.Host{Name: "anna-phone"}}); err != nil {
		t.Fatal(err)
	}
	<-b.done
	if b.user != "ha" || b.pw != "pw" || len(b.pubs) != 2 || b.pubs[1].Topic != "fb/home/presence/anna-phone" || string(b.pubs[1].Payload) != "home" || !b.pubs[1].Retain {
		t.Fatalf("user %q pw %q pubs %+v", b.user, b.pw, b.pubs)
	}

	refused := newFakeBroker(t, 5)
	m2, _ := NewMQTT("mqtt://"+refused.ln.Addr().String(), "", "fb")
	if err := m2.Send(context.Background(), Event{Router: "home", Kind: KindWANUp}); err == nil || !strings.Contains(err.Error(), "not authorised") {
		t.Fatalf("refused: %v", err)
	}
}

type recSink struct {
	mu      sync.Mutex
	name    string
	initial bool
	got     []Kind
	fail    bool
}

func (r *recSink) Name() string       { return r.name }
func (r *recSink) WantsInitial() bool { return r.initial }
func (r *recSink) Send(_ context.Context, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, e.Kind)
	if r.fail {
		return errors.New("down")
	}
	return nil
}

func TestDispatcher(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a, m := &recSink{name: "webhook"}, &recSink{name: "mqtt", initial: true, fail: true}
	var mu sync.Mutex
	reports := map[string]int{}
	d := NewDispatcher(ctx, []Sink{a, m}, func(s string, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			reports[s]++
		}
	})
	d.Send(Event{Kind: KindArrived, Initial: true})
	d.Send(Event{Kind: KindLeft})
	d.Send(Event{Kind: KindArrived})
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.Lock()
		n := len(m.got)
		m.mu.Unlock()
		if n == 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-d.Done()
	if !slices.Equal(a.got, []Kind{KindLeft, KindArrived}) || !slices.Equal(m.got, []Kind{KindArrived, KindLeft, KindArrived}) || reports["mqtt"] != 3 {
		t.Fatalf("webhook %v mqtt %v reports %v", a.got, m.got, reports)
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
