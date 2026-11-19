package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mkrajinov/fritzbox/internal/config"
	"github.com/mkrajinov/fritzbox/internal/fakebox"
)

func TestCLIBackupRotation(t *testing.T) {
	fb, run := setupCLI(t)
	dir := filepath.Join(t.TempDir(), "b")

	// No password anywhere and no terminal: refuse with a hint.
	if out, err := run("backup", "run", "--dir", dir); err == nil || !strings.Contains(err.Error(), "fb backup password set") {
		t.Fatalf("missing password: %v\n%s", err, out)
	}
	t.Setenv("FB_BACKUP_PASSWORD", "secret")
	if out, err := run("backup", "password", "set"); err != nil || !strings.Contains(out, "stored in the keychain") {
		t.Fatalf("password set: %v\n%s", err, out)
	}
	if pw, _ := config.BackupPassword("home"); pw != "secret" {
		t.Fatalf("keychain: %q", pw)
	}
	os.Unsetenv("FB_BACKUP_PASSWORD") // from now on the keychain entry is used

	export := func(dhcpEnd string) string {
		return "**** FRITZ!Box 7590 AX CONFIGURATION EXPORT\nPassword=$$$$X\n**** CFGFILE:ar7.cfg\n * Thu Nov 19 03:00:00 2026\nar7cfg {\n  end = " + dhcpEnd + ";\n}\n**** END OF EXPORT 0000 ****\n"
	}
	fb.SetExport(export("192.168.178.200"))
	out, err := run("backup", "run", "--dir", dir, "--keep", "2")
	if err != nil || !strings.Contains(out, "saved "+dir) {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	if c := fb.CallsTo("deviceconfig", "X_AVM-DE_GetConfigFile"); !strings.Contains(c[0].Body, "secret") {
		t.Errorf("keychain password not used: %s", c[0].Body)
	}
	fb.SetExport(strings.Replace(export("192.168.178.200"), "Thu Nov 19", "Fri Nov 20", 1))
	if out, err = run("backup", "run", "--dir", dir); err != nil || !strings.Contains(out, "unchanged") {
		t.Fatalf("unchanged run: %v\n%s", err, out)
	}
	// Two more changed exports with --keep 2: the oldest is removed. Names have second
	// resolution, so wait for the clock between runs.
	for _, end := range []string{"192.168.178.150", "192.168.178.100"} {
		waitNextSecond()
		fb.SetExport(export(end))
		if out, err = run("backup", "run", "--dir", dir, "--keep", "2"); err != nil {
			t.Fatalf("run %s: %v\n%s", end, err, out)
		}
	}
	if !strings.Contains(out, "removed home-") {
		t.Errorf("prune not reported:\n%s", out)
	}
	if out, err = run("backup", "list", "--dir", dir); err != nil || strings.Count(out, ".export") != 2 {
		t.Fatalf("list: %v\n%s", err, out)
	}
	out, err = run("backup", "diff", "--dir", dir)
	if err != nil || !strings.Contains(out, "@@ ar7.cfg") || !strings.Contains(out, "-  end = 192.168.178.150;") || !strings.Contains(out, "+  end = 192.168.178.100;") {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	if out, err = run("backup", "diff", "--dir", dir, "1", "1"); err != nil || !strings.Contains(out, "no configuration changes") {
		t.Fatalf("diff same: %v\n%s", err, out)
	}
	if out, err = run("backup", "password", "clear"); err != nil || !strings.Contains(out, "removed") {
		t.Fatalf("clear: %v\n%s", err, out)
	}
	_ = fakebox.BackupData
}

func waitNextSecond() {
	now := time.Now()
	time.Sleep(now.Truncate(time.Second).Add(time.Second).Sub(now) + 10*time.Millisecond)
}
