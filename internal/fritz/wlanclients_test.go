package fritz_test

import (
	"context"
	"testing"

	"github.com/mkrajinov/fritzbox/internal/fakebox"
	"github.com/mkrajinov/fritzbox/internal/fritz"
	"github.com/mkrajinov/fritzbox/internal/tr064"
)

func TestWLANClients(t *testing.T) {
	b, _ := connect(t, fakebox.User, fakebox.Password)
	cs, err := b.WLANClients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("got %d clients: %+v", len(cs), cs)
	}
	unknown, laptop := cs[0], cs[1]
	if unknown.MAC != "AA:BB:CC:00:00:09" || unknown.Band != "2.4" || unknown.Signal != 22 || unknown.Name != "" || !unknown.Auth {
		t.Errorf("2.4 GHz client = %+v", unknown)
	}
	if laptop.Name != "laptop" || laptop.Band != "5" || laptop.Signal != 74 || laptop.Speed != 866 || laptop.SpeedRX != 780 || laptop.SpeedMax != 1201 {
		t.Errorf("5 GHz client = %+v", laptop)
	}
	if fritz.SignalLabel(laptop.Signal) != "excellent" || fritz.SignalLabel(unknown.Signal) != "weak" {
		t.Error("signal labels")
	}
}

func TestParseWLANDeviceListFallbackNames(t *testing.T) {
	cs, err := fritz.ParseWLANDeviceList([]byte(`<List><Item><MACAddress>aa:00:00:00:00:01</MACAddress><SignalStrength>55</SignalStrength><Speed>300</Speed></Item><Item><Foo>x</Foo></Item></List>`))
	if err != nil || len(cs) != 1 || cs[0].MAC != "AA:00:00:00:00:01" || cs[0].Signal != 55 || cs[0].Speed != 300 {
		t.Fatalf("%v %+v", err, cs)
	}
}

func TestWiFiHostsNotOnBox(t *testing.T) {
	hosts := []fritz.Host{
		{Name: "laptop", MAC: "aa:bb:cc:00:00:01", Active: true, Interface: "802.11"},
		{Name: "tv", MAC: "aa:bb:cc:00:00:07", Active: true, Interface: "802.11"},
		{Name: "nas", MAC: "aa:bb:cc:00:00:02", Active: true, Interface: "Ethernet"},
		{Name: "off", MAC: "aa:bb:cc:00:00:08", Active: false, Interface: "802.11"},
	}
	got := fritz.WiFiHostsNotOnBox(hosts, []fritz.WLANClient{{MAC: "AA:BB:CC:00:00:01"}})
	if len(got) != 1 || got[0].Name != "tv" {
		t.Fatalf("%+v", got)
	}
}

func TestWLANClientsFallback(t *testing.T) {
	b, fb := connect(t, fakebox.User, fakebox.Password)
	fb.Faults["wlanconfig2#X_AVM-DE_GetWLANDeviceListPath"] = tr064.CodeInvalidAction
	fb.Responses["wlanconfig2#GetTotalAssociations"] = map[string]string{"NewTotalAssociations": "1"}
	fb.Responses["wlanconfig2#GetGenericAssociatedDeviceInfo"] = map[string]string{
		"NewAssociatedDeviceMACAddress": "AA:BB:CC:00:00:01", "NewAssociatedDeviceIPAddress": "192.168.178.20",
		"NewAssociatedDeviceAuthState": "1", "NewX_AVM-DE_Speed": "600", "NewX_AVM-DE_SignalStrength": "51",
	}
	cs, err := b.WLANClients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[1].Name != "laptop" || cs[1].Signal != 51 || cs[1].Speed != 600 || cs[1].Band != "5" {
		t.Fatalf("%+v", cs)
	}
}
