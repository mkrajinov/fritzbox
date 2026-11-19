package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/mkrajinov/fritzbox/internal/service"
)

func fakeServices(t *testing.T, goos string) *[][]string {
	var calls [][]string
	home := t.TempDir()
	old := newServiceManager
	newServiceManager = func() (*service.Manager, error) {
		return &service.Manager{GOOS: goos, Home: home, UID: 501, Exe: "/usr/local/bin/fritzbox", Run: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{name}, args...))
			return []byte("state = running\npid = 42\n"), nil
		}}, nil
	}
	t.Cleanup(func() { newServiceManager = old })
	return &calls
}

func TestCLIServiceInstall(t *testing.T) {
	_, run := setupCLI(t)
	calls := fakeServices(t, "darwin")
	t.Setenv("FB_NTFY_TOKEN", "tk_secret")

	out, err := run("service", "install", "watch", "--", "--track", "anna-phone")
	if err != nil || !strings.Contains(out, "installed watch") || !strings.Contains(out, "--track anna-phone") ||
		!strings.Contains(out, "environment copied (file readable only by you): FB_NTFY_TOKEN") {
		t.Fatalf("install: %v\n%s", err, out)
	}
	// The test config is not the default path, so it is passed on.
	if !strings.Contains(out, "--config ") {
		t.Errorf("--config not passed:\n%s", out)
	}
	if len(*calls) != 2 || (*calls)[1][1] != "bootstrap" {
		t.Errorf("launchctl calls: %v", *calls)
	}
	if out, err = run("service", "list"); err != nil || strings.TrimSpace(out) != "watch" {
		t.Errorf("list: %v %q", err, out)
	}
	if out, err = run("service", "status", "watch"); err != nil || !strings.Contains(out, "state = running") {
		t.Errorf("status: %v\n%s", err, out)
	}
	if out, err = run("service", "uninstall", "watch"); err != nil || !strings.Contains(out, "removed watch") {
		t.Errorf("uninstall: %v\n%s", err, out)
	}
	if _, err = run("service", "uninstall", "watch"); err == nil {
		t.Error("uninstalling twice should fail")
	}

	// Flags without -- and unknown jobs are refused.
	for _, bad := range [][]string{{"service", "install", "watch", "extra"}, {"service", "install", "nope"}, {"service", "install", "watch", "--every", "1h"}} {
		if out, err := run(bad...); err == nil {
			t.Errorf("%v should fail:\n%s", bad, out)
		}
	}
}

func TestCLIServiceBackupNeedsPassword(t *testing.T) {
	_, run := setupCLI(t)
	fakeServices(t, "linux")
	if out, err := run("service", "install", "backup"); err == nil || !strings.Contains(err.Error(), "backup password set") {
		t.Fatalf("backup without password: %v\n%s", err, out)
	}
	t.Setenv("FB_BACKUP_PASSWORD", "secret")
	if _, err := run("backup", "password", "set"); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("FB_BACKUP_PASSWORD")
	out, err := run("-r", "home", "service", "install", "backup", "--every", "12h", "--dry-run", "--", "--keep", "7")
	if err != nil || !strings.Contains(out, "fritzbox-backup-home.timer") || !strings.Contains(out, "OnUnitActiveSec=43200s") ||
		!strings.Contains(out, "--router home backup run --keep 7") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
}
