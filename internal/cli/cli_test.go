package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/mkrajinovic/fbctl/internal/config"
	"github.com/mkrajinovic/fbctl/internal/fakebox"
)

func setupCLI(t *testing.T) (*fakebox.Box, func(args ...string) (string, error)) {
	keyring.MockInit()
	fb := fakebox.New()
	t.Cleanup(fb.Close)
	host, port := fb.HostPort()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg, _ := config.Load(path)
	cfg.Routers["home"] = &config.Router{Host: host, Port: port, WebPort: port, User: fakebox.User}
	cfg.Default = "home"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := config.StorePassword("home", fakebox.Password); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		root := newRoot(&out, &out)
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(append([]string{"--config", path}, args...))
		err := root.ExecuteContext(context.Background())
		return out.String(), err
	}
	return fb, run
}

func TestCLICommands(t *testing.T) {
	fb, run := setupCLI(t)
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"status"}, []string{"FRITZ!Box 7590 AX", "203.0.113.7", "connected"}},
		{[]string{"hosts", "--active", "--sort", "name"}, []string{"laptop", "nas"}},
		{[]string{"wlan", "list"}, []string{"2.4 GHz", "5 GHz", "Guest"}},
		{[]string{"wlan", "on", "guest"}, []string{"Guest (Guest) enabled"}},
		{[]string{"wlan", "qr", "guest"}, []string{"Guest"}},
		{[]string{"calls", "--missed"}, []string{"01701234567", "Alice"}},
		{[]string{"phonebook", "list"}, []string{"Telefonbuch"}},
		{[]string{"phonebook", "show", "0", "--search", "bob"}, []string{"01519876543"}},
		{[]string{"home", "list"}, []string{"Desk lamp", "Living room", "21.0°C"}},
		{[]string{"home", "switch", "Desk lamp", "off"}, []string{"Desk lamp: off"}},
		{[]string{"home", "temp", "living room", "22"}, []string{"target 22"}},
		{[]string{"hosts", "wake", "nas"}, []string{"AA:BB:CC:00:00:02"}},
		{[]string{"reboot", "--yes"}, []string{"reboot triggered"}},
		{[]string{"raw", "services"}, []string{"WLANConfiguration3", "X_AVM-DE_OnTel1"}},
		{[]string{"raw", "call", "DeviceInfo", "GetInfo"}, []string{"NewModelName", "FRITZ!Box 7590 AX"}},
		{[]string{"router", "list"}, []string{"home"}},
		{[]string{"mesh"}, []string{"fritz.repeater", "laptop", "uplink WLAN to fritz.box"}},
		{[]string{"log", "-g", "wlan"}, []string{"Internet connection established"}},
		{[]string{"hosts", "rename", "nas", "backup-nas"}, []string{"renamed to backup-nas"}},
		{[]string{"wlan", "password", "guest"}, []string{"guest;pass"}},
		{[]string{"wlan", "password", "guest", "--set", "correct-horse-1", "--yes"}, []string{"password set to: correct-horse-1"}},
		{[]string{"hosts", "block", "laptop"}, []string{"internet blocked for laptop"}},
		{[]string{"hosts", "access", "laptop"}, []string{"internet denied"}},
		{[]string{"hosts", "unblock", "laptop"}, []string{"internet allowed"}},
		{[]string{"hosts", "access", "laptop"}, []string{"internet granted"}},
		{[]string{"status", "--all"}, []string{"home", "online", "FRITZ!Box 7590 AX"}},
	}
	for _, c := range cases {
		out, err := run(c.args...)
		if err != nil {
			t.Errorf("%v: %v\n%s", c.args, err, out)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%v: missing %q in\n%s", c.args, w, out)
			}
		}
	}
	if fb.Plug != "0" || fb.Target != 44 {
		t.Errorf("smart home state plug=%s target=%d", fb.Plug, fb.Target)
	}
	if len(fb.CallsTo("deviceconfig", "Reboot")) != 1 {
		t.Error("reboot not sent")
	}
}

func TestCLIJSON(t *testing.T) {
	_, run := setupCLI(t)
	out, err := run("--json", "hosts")
	if err != nil {
		t.Fatal(err)
	}
	var hosts []map[string]any
	if err := json.Unmarshal([]byte(out), &hosts); err != nil || len(hosts) != 3 {
		t.Fatalf("json: %v %d\n%s", err, len(hosts), out)
	}
}

func TestRebootRefusesWithoutYesOnPipe(t *testing.T) {
	fb, run := setupCLI(t)
	r, w, _ := os.Pipe()
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	if _, err := run("reboot"); err == nil {
		t.Fatal("reboot without --yes on a non-tty must fail")
	}
	if n := len(fb.CallsTo("deviceconfig", "Reboot")); n != 0 {
		t.Fatalf("reboot sent %d times", n)
	}
}

func TestCLIAdminCommands(t *testing.T) {
	fb, run := setupCLI(t)
	t.Setenv("FB_BACKUP_PASSWORD", "secret")
	out := filepath.Join(t.TempDir(), "box.export")
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"update"}, []string{"154.08.02", "important"}},
		{[]string{"update", "auto", "all"}, []string{"auto update: all"}},
		{[]string{"backup", "-o", out}, []string{"saved", "box.export"}},
		{[]string{"dhcp"}, []string{"192.168.178.20 – 192.168.178.200"}},
		{[]string{"dhcp", "range", "192.168.178.100", "192.168.178.199", "-y"}, []string{"DHCP range"}},
		{[]string{"remote"}, []string{"abc123.myfritz.net", "valid"}},
		{[]string{"remote", "myfritz", "off"}, []string{"myfritz"}},
		{[]string{"tam"}, []string{"0:1", "0301112223", "Alice"}},
		{[]string{"tam", "mark", "0:0"}, nil},
		{[]string{"forward"}, []string{"all calls → 01701234567 immediately"}},
		{[]string{"forward", "on", "0"}, []string{"forwarding rule 0"}},
		{[]string{"dial", "alice"}, []string{"calling Alice (01701234567)"}},
		{[]string{"hangup"}, nil},
		{[]string{"profiles"}, []string{"filtprof5", "Kids"}},
		{[]string{"hosts", "info", "nas"}, []string{"35 of 120 min", "Standard"}},
		{[]string{"hosts", "profile", "nas", "kids"}, []string{"nas now uses profile Kids"}},
		{[]string{"hosts", "ticket", "nas"}, []string{"35 of 165 min"}},
		{[]string{"hosts", "priority", "nas", "on"}, []string{"priority"}},
		{[]string{"wlan", "set", "guest", "--ssid", "Visitors", "--channel", "11", "--hidden", "true", "-y"}, []string{"SSID: Visitors", "channel: 11", "hidden"}},
		{[]string{"wlan", "wps", "2.4", "--wait=false"}, []string{"press WPS on the device"}},
		{[]string{"wlan", "all", "on"}, []string{"all WLANs"}},
		{[]string{"home", "templates"}, []string{"Guest WLAN on", "tmp6F0093-39512EE15"}},
		{[]string{"home", "apply", "guest wlan on"}, []string{"applied Guest WLAN on"}},
	}
	for _, c := range cases {
		got, err := run(c.args...)
		if err != nil {
			t.Errorf("%v: %v\n%s", c.args, err, got)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%v: missing %q in\n%s", c.args, w, got)
			}
		}
	}
	data, err := os.ReadFile(out)
	if err != nil || string(data) != fakebox.BackupData {
		t.Fatalf("backup file %q %v", data, err)
	}
	if st, _ := os.Stat(out); st.Mode().Perm() != 0o600 {
		t.Errorf("backup mode %v", st.Mode().Perm())
	}
	if _, err := run("backup", "-o", out); err == nil {
		t.Error("backup overwrote an existing file without --force")
	}
	if c := fb.CallsTo("x_voip", "X_AVM-DE_DialNumber"); len(c) != 1 || !strings.Contains(c[0].Body, "01701234567") {
		t.Errorf("dial: %+v", c)
	}
}

func TestCLISecondFactor(t *testing.T) {
	fb, run := setupCLI(t)
	fb.NeedsToken["deviceconfig#Reboot"] = true
	fb.ConfirmAfter = 1
	got, err := run("reboot", "-y")
	if err != nil {
		t.Fatalf("%v\n%s", err, got)
	}
	if !strings.Contains(got, "press any button on the FRITZ!Box, or dial *1234") {
		t.Fatalf("no instructions:\n%s", got)
	}
}
