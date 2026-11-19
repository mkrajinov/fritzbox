package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mkrajinov/fritzbox/internal/fakebox"
)

func TestCLICapture(t *testing.T) {
	fb, run := setupCLI(t)
	dir := filepath.Join(t.TempDir(), "cap")
	out, err := run("capture", "-o", dir, "--hide-device-names")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"captured", "host list: 3 hosts, err=<nil>", "call list: 3 calls", "phonebook0.xml: 2 entries, 2 with uniqueid",
		"answering machine 0: 2 messages, 2 with a recording path", "wlandevicelist2.xml: 1 clients, signal 74…74"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// Only reads: nothing that changes the box was called.
	for _, c := range fb.Calls {
		for _, w := range []string{"Set", "Delete", "Add", "Reboot", "Force", "GetSecurityKeys", "GetConfigFile", "Wake", "Dial"} {
			if strings.Contains(c.Action, w) && c.Action != "X_AVM-DE_DialGetConfig" {
				t.Errorf("capture called %s", c.Action)
			}
		}
	}
	// Nothing personal or secret from the fake box is in the output.
	var all strings.Builder
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			data, _ := os.ReadFile(p)
			all.Write(data)
			if info.Mode().Perm() != 0o600 {
				t.Errorf("%s mode %v", p, info.Mode().Perm())
			}
		}
		return nil
	})
	for _, secret := range []string{fakebox.Password, "deadbeef", "01701234567", "01519876543", "030123456", "Alice", "Bob", "alice@example.com",
		"AA:BB:CC:00:00:01", "laptop", "abc123.myfritz", "fbctl", "4930123456", "1112223", "fritzhome", "tok-9f8e7d6c", "ddnsuser",
		"me@example.org", "ABC123", "000.044.000"} {
		if i := strings.Index(all.String(), secret); i >= 0 {
			t.Logf("context: %q", all.String()[max(0, i-80):min(len(all.String()), i+40)])
			t.Errorf("capture contains %q", secret)
		}
	}
	if !strings.Contains(all.String(), "Contact 1") || !strings.Contains(all.String(), "02:00:00:00:") {
		t.Error("placeholders missing")
	}
	if _, err := run("capture", "-o", dir); err == nil {
		t.Error("an existing folder must not be overwritten")
	}
}
