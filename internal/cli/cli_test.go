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
