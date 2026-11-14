package fritz_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mkrajinovic/fbctl/internal/fakebox"
	"github.com/mkrajinovic/fbctl/internal/fritz"
)

func TestFirmwareBackupDHCPRemote(t *testing.T) {
	b, fb := connect(t, fakebox.User, fakebox.Password)
	ctx := context.Background()
	u, err := b.UpdateInfo(ctx)
	if err != nil || u.AutoMode != "important" || u.Current != "154.08.02" {
		t.Fatalf("update info %+v %v", u, err)
	}
	if st, err := b.DoUpdate(ctx); err != nil || st != "Started" {
		t.Fatalf("do update %q %v", st, err)
	}
	if err := b.SetAutoUpdate(ctx, "sometimes"); err == nil {
		t.Fatal("bad auto mode accepted")
	}
	// The box names itself fritz.box in the URL; the client must still use the configured host.
	data, err := b.Backup(ctx, "pw")
	if err != nil || string(data) != fakebox.BackupData {
		t.Fatalf("backup %q %v", data, err)
	}
	if c := fb.CallsTo("deviceconfig", "X_AVM-DE_GetConfigFile"); !strings.Contains(c[0].Body, "<NewX_AVM-DE_Password>pw</NewX_AVM-DE_Password>") {
		t.Fatalf("password not sent: %s", c[0].Body)
	}
	if _, err := b.Backup(ctx, ""); err == nil {
		t.Fatal("backup without password")
	}
	d, err := b.DHCP(ctx)
	if err != nil || d.Min != "192.168.178.20" {
		t.Fatalf("dhcp %+v %v", d, err)
	}
	for _, bad := range [][2]string{{"10.0.0.2", "10.0.0.9"}, {"192.168.178.50", "192.168.178.10"}, {"192.168.178.1", "192.168.178.9"}, {"x", "y"}} {
		if err := b.SetDHCPRange(ctx, bad[0], bad[1]); err == nil {
			t.Errorf("range %v accepted", bad)
		}
	}
	if err := b.SetDHCPRange(ctx, "192.168.178.100", "192.168.178.199"); err != nil {
		t.Fatal(err)
	}
	r, err := b.Remote(ctx)
	if err != nil || !r.MyFritzEnabled || r.MyFritzName != "abc123.myfritz.net" || !r.LetsEncrypt {
		t.Fatalf("remote %+v %v", r, err)
	}
	if port, err := b.SetRemoteHTTPS(ctx, true); err != nil || port != "44321" {
		t.Fatalf("https %s %v", port, err)
	}
	if err := b.SetMyFritz(ctx, false); err != nil {
		t.Fatal(err)
	}
	if c := fb.CallsTo("x_myfritz", "SetMyFRITZ"); !strings.Contains(c[0].Body, "<NewEmail>me@example.org</NewEmail>") || !strings.Contains(c[0].Body, "<NewEnabled>0</NewEnabled>") {
		t.Fatalf("myfritz: %s", c[0].Body)
	}
}

func TestWLANAdmin(t *testing.T) {
	b, fb := connect(t, fakebox.User, fakebox.Password)
	ctx := context.Background()
	if st, err := b.StartWPS(ctx, 1, false); err != nil || st != "active" {
		t.Fatalf("wps %q %v", st, err)
	}
	if c := fb.CallsTo("wlanconfig1", "X_AVM-DE_SetWPSConfig"); !strings.Contains(c[0].Body, "pbc") {
		t.Fatal(c)
	}
	if err := b.SetSSID(ctx, 3, ""); err == nil {
		t.Fatal("empty SSID accepted")
	}
	if err := b.SetSSID(ctx, 3, "Visitors"); err != nil {
		t.Fatal(err)
	}
	if err := b.SetChannel(ctx, 3, 36); err == nil {
		t.Fatal("5 GHz channel accepted on 2.4 GHz")
	}
	if err := b.SetChannel(ctx, 3, 11); err != nil {
		t.Fatal(err)
	}
	if err := b.SetHidden(ctx, 3, true); err != nil {
		t.Fatal(err)
	}
	if c := fb.CallsTo("wlanconfig3", "SetBeaconAdvertisement"); !strings.Contains(c[0].Body, "<NewBeaconAdvertisementEnabled>0<") {
		t.Fatal(c)
	}
	if err := b.SetWLANGlobal(ctx, false); err != nil {
		t.Fatal(err)
	}
}

func TestHostInfoAndParental(t *testing.T) {
	b, fb := connect(t, fakebox.User, fakebox.Password)
	ctx := context.Background()
	h, err := b.HostInfo(ctx, "192.168.178.3")
	if err != nil || h.TimeUsed != 35 || h.TimeMax != 120 || h.ProfileID != "filtprof1" || h.WANAccess != "granted" {
		t.Fatalf("host info %+v %v", h, err)
	}
	ps, err := b.FilterProfiles(ctx)
	if err != nil || len(ps) != 3 || ps[2] != (fritz.FilterProfile{ID: "filtprof5", Name: "Kids"}) {
		t.Fatalf("profiles %+v %v", ps, err)
	}
	if err := b.SetFilterProfile(ctx, "192.168.178.3", "filtprof5"); err != nil {
		t.Fatal(err)
	}
	if _, maxMin, err := b.AddTicket(ctx, "192.168.178.3"); err != nil || maxMin != 165 {
		t.Fatalf("ticket %d %v", maxMin, err)
	}
	if err := b.SetPriority(ctx, "192.168.178.3", true); err != nil {
		t.Fatal(err)
	}
	if err := b.SetAutoWOL(ctx, h.MAC, true); err != nil {
		t.Fatal(err)
	}
	if c := fb.CallsTo("hosts", "X_AVM-DE_SetPrioritizationByIP"); !strings.Contains(c[0].Body, "<NewX_AVM-DE_Priority>1<") {
		t.Fatal(c)
	}
}

func TestParseFilterProfilesVariants(t *testing.T) {
	for in, want := range map[string]int{
		`<List><Item><ID>filtprof1</ID><Name>Standard</Name></Item></List>`: 1,
		`<FilterProfiles><Profile id="filtprof1" name="Standard"/><Profile id="filtprof2" name="Kids"/></FilterProfiles>`: 2,
		"filtprof1:Standard,filtprof2:Kids": 2,
		"": 0,
	} {
		ps, err := fritz.ParseFilterProfiles(in)
		if err != nil || len(ps) != want || (want > 0 && ps[0].ID != "filtprof1") {
			t.Errorf("%q → %+v %v", in, ps, err)
		}
	}
}

func TestPhoneFeatures(t *testing.T) {
	b, fb := connect(t, fakebox.User, fakebox.Password)
	ctx := context.Background()
	tams, err := b.TAMs(ctx)
	if err != nil || len(tams) != 1 || tams[0].Name != "Answering machine" {
		t.Fatalf("tams %+v %v", tams, err)
	}
	ms, err := b.Messages(ctx)
	if err != nil || len(ms) != 2 || ms[0].Number != "0301112223" || !ms[1].New || ms[1].Name != "Alice" {
		t.Fatalf("messages %+v %v", ms, err)
	}
	if err := b.MarkMessage(ctx, 0, 1, true); err != nil {
		t.Fatal(err)
	}
	if c := fb.CallsTo("x_tam", "MarkMessage"); !strings.Contains(c[0].Body, "<NewMessageIndex>1</NewMessageIndex>") {
		t.Fatal(c)
	}
	ds, err := b.Deflections(ctx)
	if err != nil || len(ds) != 1 || ds[0].Describe() != "all calls → 01701234567 immediately" {
		t.Fatalf("deflections %+v %v", ds, err)
	}
	if err := b.SetDeflection(ctx, 0, true); err != nil {
		t.Fatal(err)
	}
	if err := b.Dial(ctx, "030 123"); err == nil {
		t.Fatal("space accepted in number")
	}
	if err := b.Dial(ctx, "+4930123"); err != nil {
		t.Fatal(err)
	}
	if err := b.Hangup(ctx); err != nil {
		t.Fatal(err)
	}
	ts, err := b.Templates(ctx)
	if err != nil || len(ts) != 2 || ts[0].Name != "Guest WLAN on" {
		t.Fatalf("templates %+v %v", ts, err)
	}
	if err := b.ApplyTemplate(ctx, ts[0].ID); err != nil {
		t.Fatal(err)
	}
}
