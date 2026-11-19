package tui

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mkrajinov/fritzbox/internal/config"
	"github.com/mkrajinov/fritzbox/internal/fakebox"
	"github.com/mkrajinov/fritzbox/internal/fritz"
	"github.com/mkrajinov/fritzbox/internal/watch"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func plain(s string) string { return ansi.ReplaceAllString(s, "") }

// pump executes commands and feeds their messages back into the model, skipping
// long-running commands (ticks, cursor blink) that don't return quickly.
// TestMain disables timers, the second-factor listener and cursor blinking, so every command
// returns and pump can wait for each one instead of guessing with a timeout.
func TestMain(m *testing.M) {
	tick = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }
	listenSecondFactor = func(chan fritz.SecondFactorPrompt) tea.Cmd { return nil }
	cursorMode = cursor.CursorStatic
	os.Exit(m.Run())
}

func pump(t *testing.T, m tea.Model, cmd tea.Cmd) tea.Model {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0 && steps < 200; steps++ {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		ch := make(chan tea.Msg, 1)
		go func() { ch <- c() }()
		var msg tea.Msg
		select {
		case msg = <-ch:
		case <-time.After(10 * time.Second):
			t.Fatalf("a command did not return within 10s (a timer or listener not disabled in TestMain?)")
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		var next tea.Cmd
		m, next = m.Update(msg)
		queue = append(queue, next)
	}
	return m
}

func key(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(t *testing.T, m tea.Model, keys ...string) tea.Model {
	for _, k := range keys {
		var cmd tea.Cmd
		m, cmd = m.Update(key(k))
		m = pump(t, m, cmd)
	}
	return m
}

func setup(t *testing.T) (tea.Model, *fakebox.Box) { return setupState(t, t.TempDir()) }

func setupState(t *testing.T, stateDir string) (tea.Model, *fakebox.Box) {
	fb := fakebox.New()
	t.Cleanup(fb.Close)
	host, port := fb.HostPort()
	conn := func(ctx context.Context, name string) (Backend, string, error) {
		r := &config.Router{Host: host, Port: port, WebPort: port, User: fakebox.User}
		b, err := fritz.Connect(ctx, name, r, fakebox.Password, fritz.ConnectOptions{Timeout: 5 * time.Second})
		return b, host, err
	}
	var m tea.Model = New(Options{Routers: []string{"home", "office"}, Current: "home", Connect: conn, NoCallmon: true, StateDir: stateDir})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = pump(t, m, m.(Model).connect())
	return m, fb
}

func TestOverview(t *testing.T) {
	m, _ := setup(t)
	v := plain(m.View())
	for _, want := range []string{"◈ home", "◈ office", "◉ Overview", "FRITZ!Box 7590 AX", "FRITZ!OS 8.02", "Connected", "203.0.113.7", "online", "Live traffic"} {
		if !strings.Contains(v, want) {
			t.Errorf("overview missing %q\n%s", want, v)
		}
	}
}

func TestHostsFilterAndWake(t *testing.T) {
	m, fb := setup(t)
	m = press(t, m, "2")
	v := plain(m.View())
	if !strings.Contains(v, "2 active / 3 known") || !strings.Contains(v, "laptop") {
		t.Fatalf("hosts view:\n%s", v)
	}
	m = press(t, m, "/", "n", "a", "s", "enter")
	v = plain(m.View())
	if strings.Contains(v, "laptop") || !strings.Contains(v, "nas") {
		t.Fatalf("filter not applied:\n%s", v)
	}
	m = press(t, m, "w")
	if !strings.Contains(plain(m.View()), "Send Wake-on-LAN to nas") {
		t.Fatalf("no confirm modal:\n%s", plain(m.View()))
	}
	press(t, m, "y")
	if c := fb.CallsTo("hosts", "X_AVM-DE_WakeOnLANByMACAddress"); len(c) != 1 || !strings.Contains(c[0].Body, "AA:BB:CC:00:00:02") {
		t.Fatalf("wake not sent: %+v", c)
	}
}

func TestWLANToggleNeedsConfirmForMainNetwork(t *testing.T) {
	m, fb := setup(t)
	m = press(t, m, "4")
	if v := plain(m.View()); !strings.Contains(v, "2.4 GHz") || !strings.Contains(v, "Guest") {
		t.Fatalf("wlan view:\n%s", v)
	}
	// Disabling the main 2.4 GHz network asks first; "n" cancels.
	m = press(t, m, "space", "n")
	if n := len(fb.CallsTo("wlanconfig1", "SetEnable")); n != 0 {
		t.Fatalf("SetEnable sent without confirmation (%d)", n)
	}
	// Enabling the guest network goes straight through.
	m = press(t, m, "j", "j", "space")
	if n := len(fb.CallsTo("wlanconfig3", "SetEnable")); n != 1 {
		t.Fatalf("guest SetEnable calls = %d", n)
	}
	if !strings.Contains(plain(m.View()), "Guest (Guest) enabled") {
		t.Errorf("no flash:\n%s", plain(m.View()))
	}
}

func TestSmartHomeControls(t *testing.T) {
	m, fb := setup(t)
	m = press(t, m, "8")
	v := plain(m.View())
	if !strings.Contains(v, "Desk lamp") || !strings.Contains(v, "12.5 W") || !strings.Contains(v, "21.0°C") {
		t.Fatalf("home view:\n%s", v)
	}
	m = press(t, m, "space")
	if fb.Plug != "0" {
		t.Errorf("plug not toggled: %s", fb.Plug)
	}
	press(t, m, "j", "+")
	if fb.Target != 43 {
		t.Errorf("thermostat target = %d, want 43", fb.Target)
	}
}

func TestRouterSwitchDropsStaleData(t *testing.T) {
	m, _ := setup(t)
	before := m.(Model).gen
	m, cmd := m.Update(key("]"))
	mm := m.(Model)
	if mm.gen != before+1 || mm.status != nil || mm.routerName() != "office" {
		t.Fatalf("switch: gen=%d status=%v router=%s", mm.gen, mm.status, mm.routerName())
	}
	// A late status reply from the old router must be ignored.
	m, _ = m.Update(statusMsg{gen: before, st: fritz.Status{DeviceInfo: fritz.DeviceInfo{Model: "stale"}}})
	if m.(Model).status != nil {
		t.Fatal("stale status applied")
	}
	m = pump(t, m, cmd)
	if m.(Model).status == nil {
		t.Fatal("new router status not loaded")
	}
}

func TestRatePollSkipsWhileInFlight(t *testing.T) {
	m, fb := setup(t)
	polls := func() int { return len(fb.CallsTo("wancommonifconfig1", "X_AVM-DE_GetOnlineMonitor")) }
	mm := m.(Model)

	before := polls()
	mm.rateBusy = true
	next, cmd := mm.Update(rateTick{gen: mm.gen})
	pump(t, next, cmd)
	if polls() != before {
		t.Fatalf("polled while a request was in flight")
	}

	mm.rateBusy = false
	next, cmd = mm.Update(rateTick{gen: mm.gen})
	if !next.(Model).rateBusy {
		t.Fatal("rateBusy not set when issuing a poll")
	}
	pump(t, next, cmd)
	if polls() != before+1 {
		t.Fatalf("expected exactly one poll, got %d", polls()-before)
	}
}

func TestHostsActiveSortDetail(t *testing.T) {
	m, _ := setup(t)
	m = press(t, m, "2", "a")
	v := plain(m.View())
	if strings.Contains(v, "visitor-phone") || !strings.Contains(v, "active only") {
		t.Fatalf("active-only not applied:\n%s", v)
	}
	m = press(t, m, "s") // ip → name: laptop before nas
	v = plain(m.View())
	if strings.Index(v, "laptop") > strings.Index(v, "nas") || !strings.Contains(v, "sort name") {
		t.Fatalf("name sort:\n%s", v)
	}
	m = press(t, m, "enter")
	v = plain(m.View())
	if !strings.Contains(v, "Details") || !strings.Contains(v, "866 Mbit/s") {
		t.Fatalf("details:\n%s", v)
	}
}

func TestMeshAndLogTabs(t *testing.T) {
	m, _ := setup(t)
	m = press(t, m, "3")
	v := plain(m.View())
	for _, want := range []string{"⋔ Mesh", "2 nodes", "fritz.repeater", "uplink WLAN to fritz.box", "laptop", "AP:2G:0"} {
		if !strings.Contains(v, want) {
			t.Errorf("mesh missing %q\n%s", want, v)
		}
	}
	m = press(t, m, "9")
	v = plain(m.View())
	if !strings.Contains(v, "Event log · all · 2 events") || !strings.Contains(v, "Internet connection established") {
		t.Fatalf("log:\n%s", v)
	}
}

func TestBlockInternetConfirms(t *testing.T) {
	m, fb := setup(t)
	m = press(t, m, "2", "j", "b") // ip sort: nas, laptop → laptop
	if !strings.Contains(plain(m.View()), "Block internet for laptop") {
		t.Fatalf("no confirm:\n%s", plain(m.View()))
	}
	m = press(t, m, "y")
	if !fb.Blocked["192.168.178.20"] {
		t.Fatal("laptop not blocked")
	}
	press(t, m, "u")
	if fb.Blocked["192.168.178.20"] {
		t.Fatal("laptop not unblocked")
	}
}

func TestWLANPasswordShowAndChange(t *testing.T) {
	m, fb := setup(t)
	m = press(t, m, "4", "j", "j", "p") // guest network
	if v := plain(m.View()); !strings.Contains(v, "guest;pass") {
		t.Fatalf("password not shown:\n%s", v)
	}
	m = press(t, m, "x", "P")
	v := plain(m.View())
	if !strings.Contains(v, "New password for Guest") || !strings.Contains(v, "generated for you") {
		t.Fatalf("no input modal:\n%s", v)
	}
	// Too short is rejected in place.
	m = press(t, m, "ctrl+u", "s", "h", "o", "r", "t", "enter")
	if v := plain(m.View()); !strings.Contains(v, "8–63 characters, got 5") {
		t.Fatalf("no validation:\n%s", v)
	}
	m = press(t, m, "ctrl+u")
	m = press(t, m, strings.Split("correct-horse", "")...)
	m = press(t, m, "enter")
	if v := plain(m.View()); !strings.Contains(v, "Change the password of Guest") {
		t.Fatalf("no confirm:\n%s", v)
	}
	m = press(t, m, "y")
	c := fb.CallsTo("wlanconfig3", "SetSecurityKeys")
	if len(c) != 1 || !strings.Contains(c[0].Body, "<NewKeyPassphrase>correct-horse</NewKeyPassphrase>") {
		t.Fatalf("SetSecurityKeys: %+v", c)
	}
	if v := plain(m.View()); !strings.Contains(v, "new password") || !strings.Contains(v, "password changed") {
		t.Fatalf("no result QR:\n%s", v)
	}
}

func TestPasswordChangeEscCancels(t *testing.T) {
	m, fb := setup(t)
	m = press(t, m, "4", "j", "j", "P", "esc")
	if len(fb.CallsTo("wlanconfig3", "SetSecurityKeys")) != 0 || !strings.Contains(plain(m.View()), "cancelled") {
		t.Fatalf("esc did not cancel:\n%s", plain(m.View()))
	}
}

func TestHostRenameAndAccessDetail(t *testing.T) {
	m, fb := setup(t)
	m = press(t, m, "2", "enter") // nas
	if v := plain(m.View()); !strings.Contains(v, "internet access") || !strings.Contains(v, "allowed") {
		t.Fatalf("no access line:\n%s", v)
	}
	m = press(t, m, "b", "y")
	if v := plain(m.View()); !strings.Contains(v, "blocked") {
		t.Fatalf("access not refreshed:\n%s", v)
	}
	m = press(t, m, "n", "ctrl+u")
	m = press(t, m, strings.Split("storage", "")...)
	press(t, m, "enter")
	c := fb.CallsTo("hosts", "X_AVM-DE_SetHostNameByMACAddress")
	if len(c) != 1 || !strings.Contains(c[0].Body, "<NewHostName>storage</NewHostName>") || !strings.Contains(c[0].Body, "AA:BB:CC:00:00:02") {
		t.Fatalf("rename: %+v", c)
	}
}

func TestCallsFilterContactsAndLogGroup(t *testing.T) {
	m, fb := setup(t)
	m = press(t, m, "5", "f")
	if v := plain(m.View()); !strings.Contains(v, "last 30 days · missed") {
		t.Fatalf("calls filter:\n%s", v)
	}
	m = press(t, m, "d")
	if v := plain(m.View()); !strings.Contains(v, "last 90 days") {
		t.Fatalf("calls days:\n%s", v)
	}
	m = press(t, m, "6")
	if v := plain(m.View()); !strings.Contains(v, "Alice") || !strings.Contains(v, "Bob") {
		t.Fatalf("contacts:\n%s", v)
	}
	m = press(t, m, "/", "b", "o", "b", "enter")
	if v := plain(m.View()); strings.Contains(v, "Alice") || !strings.Contains(v, "Bob") {
		t.Fatalf("contacts search:\n%s", v)
	}
	m = press(t, m, "9", "g")
	if v := plain(m.View()); !strings.Contains(v, "Event log · sys") {
		t.Fatalf("log group:\n%s", v)
	}
	_ = fb
}

func TestAllRouters(t *testing.T) {
	m, _ := setup(t)
	m = press(t, m, "A")
	v := plain(m.View())
	if !strings.Contains(v, "All routers") || strings.Count(v, "FRITZ!Box 7590 AX") < 2 || !strings.Contains(v, "office") {
		t.Fatalf("all routers:\n%s", v)
	}
}

func TestSecondFactorModal(t *testing.T) {
	m, fb := setup(t)
	fb.NeedsToken["deviceconfig#Reboot"] = true
	fb.ConfirmAfter = 3
	mm := m.(Model)
	// Run the reboot action; the prompt arrives on sfCh while the action waits for confirmation.
	run := mm.action("reboot triggered", tabOverview, func(c context.Context, b Backend) error { return b.Reboot(c) })
	done := make(chan tea.Msg, 1)
	go func() { done <- run() }()
	p := <-mm.sfCh
	next, _ := m.Update(sfMsg{p})
	if v := plain(next.View()); !strings.Contains(v, "Confirm on the box") || !strings.Contains(v, "dial *1234") {
		t.Fatalf("no 2FA modal:\n%s", v)
	}
	res := <-done
	next, _ = next.Update(res)
	if next.(Model).sf != nil || !strings.Contains(plain(next.View()), "reboot triggered") {
		t.Fatalf("modal not closed:\n%s", plain(next.View()))
	}
	if c := fb.CallsTo("deviceconfig", "Reboot"); len(c) != 2 {
		t.Fatalf("reboot calls %d", len(c))
	}
}

func TestPhoneAndSystemTabs(t *testing.T) {
	m, fb := setup(t)
	m = press(t, m, "7")
	v := plain(m.View())
	for _, want := range []string{"Answering machine", "0301112223", "Alice", "all calls → 01701234567 immediately", "2 messages · 1 new"} {
		if !strings.Contains(v, want) {
			t.Errorf("phone missing %q\n%s", want, v)
		}
	}
	m = press(t, m, "j", "j", "space") // rows: tam, msg(0301112223), msg(Alice) → mark Alice heard
	if c := fb.CallsTo("x_tam", "MarkMessage"); len(c) != 1 || !strings.Contains(c[0].Body, "<NewMessageIndex>0<") || !strings.Contains(c[0].Body, "<NewMarkedAsRead>1<") {
		t.Fatalf("mark: %+v", c)
	}
	m = press(t, m, "j", "space")
	if len(fb.CallsTo("x_contact", "SetDeflectionEnable")) != 1 {
		t.Fatal("forwarding not toggled")
	}
	m = press(t, m, "k", "c")
	if !strings.Contains(plain(m.View()), "Call Alice?") {
		t.Fatalf("no dial confirm:\n%s", plain(m.View()))
	}
	m = press(t, m, "y")
	if c := fb.CallsTo("x_voip", "X_AVM-DE_DialNumber"); len(c) != 1 || !strings.Contains(c[0].Body, "01701234567") {
		t.Fatalf("dial %+v", c)
	}

	m = press(t, m, "0")
	v = plain(m.View())
	for _, want := range []string{"Firmware", "auto update: important", "192.168.178.20 – 192.168.178.200", "abc123.myfritz.net", "Backup"} {
		if !strings.Contains(v, want) {
			t.Errorf("system missing %q\n%s", want, v)
		}
	}
	m = press(t, m, "a")
	if c := fb.CallsTo("userif", "X_AVM-DE_SetConfig"); len(c) != 1 || !strings.Contains(c[0].Body, ">all<") {
		t.Fatalf("auto mode %+v", c)
	}
	m = press(t, m, "H")
	if !strings.Contains(plain(m.View()), "reachable from the internet") {
		t.Fatalf("https enable not confirmed:\n%s", plain(m.View()))
	}
	m = press(t, m, "n")
	if len(fb.CallsTo("x_remote", "SetEnable")) != 0 {
		t.Fatal("https enabled without confirmation")
	}
	m = press(t, m, "e", "ctrl+u")
	m = press(t, m, strings.Split("192.168.178.100 192.168.178.150", "")...)
	m = press(t, m, "enter", "y")
	if c := fb.CallsTo("lanhostcfg", "SetAddressRange"); len(c) != 1 || !strings.Contains(c[0].Body, "192.168.178.150") {
		t.Fatalf("dhcp %+v", c)
	}
}

func TestHostAdminKeysAndTemplates(t *testing.T) {
	m, fb := setup(t)
	m = press(t, m, "2", "enter") // nas, details load host info
	v := plain(m.View())
	if !strings.Contains(v, "Standard") || !strings.Contains(v, "35 of 120 min") {
		t.Fatalf("host info:\n%s", v)
	}
	m = press(t, m, "f")
	if !strings.Contains(plain(m.View()), "from profile Standard to Gast") {
		t.Fatalf("profile confirm:\n%s", plain(m.View()))
	}
	m = press(t, m, "y", "t", "p")
	if len(fb.CallsTo("x_hostfilter", "AddHostEntryToFilterProfile")) != 1 || len(fb.CallsTo("x_hostfilter", "AddTicketTimeToHostEntryByIP")) != 1 {
		t.Fatal("profile/ticket not sent")
	}
	if c := fb.CallsTo("hosts", "X_AVM-DE_SetPrioritizationByIP"); len(c) != 1 || !strings.Contains(c[0].Body, ">1<") {
		t.Fatalf("priority %+v", c)
	}
	m = press(t, m, "4", "j", "j", "e", "ctrl+u", "V", "i", "p", "enter", "y")
	if c := fb.CallsTo("wlanconfig3", "SetSSID"); len(c) != 1 || !strings.Contains(c[0].Body, ">Vip<") {
		t.Fatalf("ssid %+v", c)
	}
	m = press(t, m, "n")
	if !strings.Contains(plain(m.View()), "possible: 1 2 3") {
		t.Fatalf("channel input:\n%s", plain(m.View()))
	}
	m = press(t, m, "ctrl+u", "1", "1", "enter")
	if len(fb.CallsTo("wlanconfig3", "SetChannel")) != 1 {
		t.Fatal("channel not set")
	}
	m = press(t, m, "8", "j", "j", "space", "y")
	if c := fb.CallsTo("aha", "applytemplate"); len(c) != 1 || !strings.Contains(c[0].Body, "tmp6F0093-39512EE15") {
		t.Fatalf("template %+v\n%s", c, plain(m.View()))
	}
}

func TestHostsSignalAndPresence(t *testing.T) {
	dir := t.TempDir()
	st, _ := watch.NewState(watch.KnownPath(dir, "home"))
	st.Update(watch.Snapshot{Time: time.Now().Add(-90 * time.Minute), HostsOK: true,
		Hosts: []fritz.Host{{MAC: "AA:BB:CC:00:00:01", Name: "laptop", Active: true}}})
	m, _ := setupState(t, dir)
	m = press(t, m, "2")
	v := plain(m.View())
	if !strings.Contains(v, "▮▮▮▮  74 5 GHz") {
		t.Fatalf("no signal column:\n%s", v)
	}
	m = press(t, m, "s", "enter") // sort by name: laptop first
	v = plain(m.View())
	for _, want := range []string{"excellent", "866 / 780 Mbit/s", "home since", "fb watch not running"} {
		if !strings.Contains(v, want) {
			t.Errorf("details missing %q\n%s", want, v)
		}
	}
}
