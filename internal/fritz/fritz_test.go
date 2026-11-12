package fritz_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mkrajinovic/fbctl/internal/config"
	"github.com/mkrajinovic/fbctl/internal/fakebox"
	"github.com/mkrajinovic/fbctl/internal/fritz"
	"github.com/mkrajinovic/fbctl/internal/tr064"
)

func connect(t *testing.T, user, pw string) (*fritz.Box, *fakebox.Box) {
	t.Helper()
	fb := fakebox.New()
	t.Cleanup(fb.Close)
	host, port := fb.HostPort()
	r := &config.Router{Host: host, Port: port, WebPort: port, User: user}
	b, err := fritz.Connect(context.Background(), "test", r, pw, fritz.ConnectOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return b, fb
}

func TestStatus(t *testing.T) {
	b, fb := connect(t, fakebox.User, fakebox.Password)
	st, err := b.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Model != "FRITZ!Box 7590 AX" || st.Firmware != "8.02" || st.Uptime != 93784*time.Second {
		t.Errorf("device info: %+v", st.DeviceInfo)
	}
	if !st.Connected || !st.LinkUp || st.ConnectionType != "WANPPPConnection1" || st.ExternalIPv4 != "203.0.113.7" {
		t.Errorf("wan: %+v", st)
	}
	if st.Rates.Up != 1000 || st.Rates.Down != 50000 || len(st.Rates.DownHist) != 3 {
		t.Errorf("rates: %+v", st.Rates)
	}
	if st.TotalReceived != 987654321 || st.MaxDownBits != 250000000 {
		t.Errorf("totals: %+v", st)
	}
	// Digest challenge should be cached: at most one 401 round trip for the whole status burst
	// is expected per concurrent first request, and no request may fail auth with credentials.
	if fb.AuthFails != 0 {
		t.Errorf("digest auth failed %d times", fb.AuthFails)
	}
}

func TestWrongPassword(t *testing.T) {
	b, _ := connect(t, fakebox.User, "nope")
	_, err := b.Info(context.Background())
	if !errors.Is(err, tr064.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

func TestLastUserLookup(t *testing.T) {
	b, _ := connect(t, "", fakebox.Password)
	if b.User != "fbctl" {
		t.Fatalf("user = %q, want last_user fbctl", b.User)
	}
	if _, err := b.Info(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHostsAndWake(t *testing.T) {
	b, fb := connect(t, fakebox.User, fakebox.Password)
	ctx := context.Background()
	hosts, err := b.Hosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 3 || !hosts[0].Active || hosts[2].Guest != true || hosts[1].SpeedMbit != 1000 {
		t.Fatalf("hosts: %+v", hosts)
	}
	fritz.SortHosts(hosts, "ip")
	if hosts[0].Name != "nas" { // 192.168.178.3 < 192.168.178.20 numerically
		t.Errorf("ip sort: %v", hosts[0].Name)
	}
	h, err := fritz.FindHost(hosts, "LAPTOP")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WakeOnLAN(ctx, h.MAC); err != nil {
		t.Fatal(err)
	}
	calls := fb.CallsTo("hosts", "X_AVM-DE_WakeOnLANByMACAddress")
	if len(calls) != 1 || !strings.Contains(calls[0].Body, "<NewMACAddress>AA:BB:CC:00:00:01</NewMACAddress>") {
		t.Fatalf("wake call: %+v", calls)
	}
}

func TestWLAN(t *testing.T) {
	b, fb := connect(t, fakebox.User, fakebox.Password)
	ctx := context.Background()
	ws, err := b.WLANs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 3 || ws[0].Band != "2.4" || ws[1].Band != "5" || !ws[2].Guest || ws[1].Clients != 7 {
		t.Fatalf("wlans: %+v", ws)
	}
	g, err := fritz.ResolveWLAN(ws, "guest")
	if err != nil || g.Index != 3 {
		t.Fatalf("guest: %+v %v", g, err)
	}
	if w, _ := fritz.ResolveWLAN(ws, "5GHz"); w.Index != 2 {
		t.Errorf("5GHz → %d", w.Index)
	}
	if err := b.SetWLAN(ctx, g.Index, true); err != nil {
		t.Fatal(err)
	}
	if c := fb.CallsTo("wlanconfig3", "SetEnable"); len(c) != 1 || !strings.Contains(c[0].Body, "<NewEnable>1</NewEnable>") {
		t.Fatalf("SetEnable: %+v", c)
	}
	pw, err := b.WLANPassword(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := fritz.WiFiQRPayload(g, pw); got != `WIFI:T:WPA;S:Guest;P:guest\;pass;;` {
		t.Errorf("qr payload %q", got)
	}
}

func TestSecondFactorError(t *testing.T) {
	b, fb := connect(t, fakebox.User, fakebox.Password)
	fb.Faults["deviceconfig#Reboot"] = tr064.CodeSecondFactorNeeded
	err := b.Reboot(context.Background())
	if !tr064.NeedsSecondFactor(err) || !strings.Contains(err.Error(), "second-factor confirmation") {
		t.Fatalf("want second-factor error, got %v", err)
	}
}

func TestCallsAndPhonebook(t *testing.T) {
	b, _ := connect(t, fakebox.User, fakebox.Password)
	ctx := context.Background()
	calls, err := b.Calls(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("calls: %+v", calls)
	}
	if calls[0].TypeName != "missed" || calls[0].Number != "01701234567" || calls[0].Name != "Alice" {
		t.Errorf("missed call: %+v", calls[0])
	}
	if calls[1].Number != "0891234" || calls[1].Own != "030123456" || calls[1].Duration != 12*time.Minute {
		t.Errorf("out call: %+v", calls[1])
	}
	if calls[2].Duration != time.Hour+5*time.Minute || calls[2].Date.Day() != 10 {
		t.Errorf("in call: %+v", calls[2])
	}
	missed, _ := b.Calls(ctx, 0, fritz.CallMissed)
	if len(missed) != 1 {
		t.Errorf("missed filter: %d", len(missed))
	}
	pbs, err := b.Phonebooks(ctx)
	if err != nil || len(pbs) != 2 || pbs[0].Name != "Telefonbuch" {
		t.Fatalf("phonebooks: %+v %v", pbs, err)
	}
	_, contacts, err := b.Contacts(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 2 || len(contacts[0].Numbers) != 2 || contacts[0].Numbers[0].Type != "mobile" {
		t.Fatalf("contacts: %+v", contacts)
	}
}

func TestSmartHome(t *testing.T) {
	b, fb := connect(t, fakebox.User, fakebox.Password)
	ctx := context.Background()
	devs, err := b.SmartDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 || devs[0].Switch == nil || devs[0].PowerMeter.Power != 12500 || devs[1].Thermostat == nil {
		t.Fatalf("devices: %+v", devs)
	}
	d, err := fritz.ResolveSmartDevice(devs, "116570240192")
	if err != nil || d.Name != "Desk lamp" {
		t.Fatalf("resolve by AIN without spaces: %+v %v", d, err)
	}
	if err := b.SetSwitch(ctx, d.AIN, "off"); err != nil {
		t.Fatal(err)
	}
	if fb.Plug != "0" {
		t.Errorf("plug = %s", fb.Plug)
	}
	if err := b.SetThermostat(ctx, devs[1].AIN, "21.5"); err != nil {
		t.Fatal(err)
	}
	if fb.Target != 43 {
		t.Errorf("target = %d", fb.Target)
	}
	// One login, then the SID is reused.
	if n := len(fb.CallsTo("aha", "getdevicelistinfos")); n != 1 {
		t.Errorf("device list fetched %d times", n)
	}
}

func TestMesh(t *testing.T) {
	b, _ := connect(t, fakebox.User, fakebox.Password)
	nodes, err := b.Mesh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0].Role != "master" || nodes[1].Name != "fritz.repeater" {
		t.Fatalf("nodes: %+v", nodes)
	}
	router, rep := nodes[0], nodes[1]
	if len(router.Clients) != 2 || !router.Clients[0].IsMeshed || router.Clients[1].Name != "nas" || router.Clients[1].Type != "LAN" {
		t.Fatalf("router clients (repeater first, then nas): %+v", router.Clients)
	}
	if rep.Uplink == nil || rep.Parent != "fritz.box" || rep.Uplink.RxKbit != 80000 || rep.Uplink.Iface != "AP:5G:0" {
		t.Fatalf("repeater uplink: %+v parent=%s", rep.Uplink, rep.Parent)
	}
	// Disconnected visitor-phone must not show up; laptop link is stored reversed (node 1 = laptop).
	if len(rep.Clients) != 1 || rep.Clients[0].Name != "laptop" || rep.Clients[0].RxKbit != 40000 || rep.Clients[0].TxKbit != 3000 {
		t.Fatalf("repeater clients: %+v", rep.Clients)
	}
}

func TestDeviceLogRenameAndWLANPassword(t *testing.T) {
	b, fb := connect(t, fakebox.User, fakebox.Password)
	ctx := context.Background()
	evs, err := b.DeviceLog(ctx, "wlan")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Group != "net" || evs[0].Time.Hour() != 14 || !strings.Contains(evs[1].Msg, "laptop") {
		t.Fatalf("log: %+v", evs)
	}
	if c := fb.CallsTo("http", "devicelog"); len(c) != 1 || !strings.Contains(c[0].Body, "filter=wlan") {
		t.Fatalf("filter not passed: %+v", c)
	}
	if err := b.RenameHost(ctx, "AA:BB:CC:00:00:02", "nas & backup"); err != nil {
		t.Fatal(err)
	}
	if c := fb.CallsTo("hosts", "X_AVM-DE_SetHostNameByMACAddress"); len(c) != 1 || !strings.Contains(c[0].Body, "<NewHostName>nas &amp; backup</NewHostName>") {
		t.Fatalf("rename: %+v", c)
	}
	pw, err := fritz.GeneratePassword(20)
	if err != nil || len(pw) != 20 {
		t.Fatalf("generate: %q %v", pw, err)
	}
	if _, err := fritz.GeneratePassword(7); err == nil {
		t.Error("7-char password must be rejected")
	}
	if err := b.SetWLANPassword(ctx, 3, pw); err != nil {
		t.Fatal(err)
	}
	c := fb.CallsTo("wlanconfig3", "SetSecurityKeys")
	if len(c) != 1 || !strings.Contains(c[0].Body, "<NewKeyPassphrase>"+pw+"</NewKeyPassphrase>") || !strings.Contains(c[0].Body, "<NewWEPKey0></NewWEPKey0>") {
		t.Fatalf("SetSecurityKeys: %+v", c)
	}
}

func TestStatusAllReportsUnreachable(t *testing.T) {
	fb := fakebox.New()
	t.Cleanup(fb.Close)
	host, port := fb.HostPort()
	cfg, _ := config.Load(t.TempDir() + "/c.yaml")
	cfg.Routers["home"] = &config.Router{Host: host, Port: port, User: fakebox.User}
	cfg.Routers["dead"] = &config.Router{Host: "127.0.0.1", Port: 1, User: fakebox.User}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res := fritz.StatusAll(ctx, cfg, func(ctx context.Context, name string, r *config.Router) (*fritz.Box, error) {
		return fritz.Connect(ctx, name, r, fakebox.Password, fritz.ConnectOptions{Timeout: 2 * time.Second})
	})
	if len(res) != 2 || res[0].Router != "dead" || res[0].Error == "" || res[1].Error != "" || res[1].Status.Model == "" {
		t.Fatalf("%+v", res)
	}
}
