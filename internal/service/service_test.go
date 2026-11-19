package service

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type recorder struct{ calls [][]string }

func (r *recorder) run(name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return nil, nil
}

func manager(t *testing.T, goos string) (*Manager, *recorder) {
	r := &recorder{}
	return &Manager{GOOS: goos, Home: t.TempDir(), UID: 501, Exe: "/opt/homebrew/opt/fritzbox/bin/fritzbox", Run: r.run}, r
}

func TestStablePath(t *testing.T) {
	exists := func(p string) bool { return p == "/opt/homebrew/opt/fritzbox/bin/fritzbox" }
	got, err := stablePath("/opt/homebrew/Cellar/fritzbox/0.2.0/bin/fritzbox", exists)
	if err != nil || got != "/opt/homebrew/opt/fritzbox/bin/fritzbox" {
		t.Errorf("cellar: %q %v", got, err)
	}
	if got, _ := stablePath("/home/me/go/bin/fritzbox", exists); got != "/home/me/go/bin/fritzbox" {
		t.Errorf("plain path changed: %q", got)
	}
	if _, err := stablePath("/var/folders/x/go-build123/b001/exe/fritzbox", exists); err == nil {
		t.Error("go run binaries must be refused")
	}
}

func TestLaunchdInstall(t *testing.T) {
	m, r := manager(t, "darwin")
	s := Spec{Job: "watch", Router: "home", Extra: []string{"--ntfy", "https://ntfy.sh/a&b"}, Env: map[string]string{"FB_NTFY_TOKEN": "tk<1>"}}
	files, err := m.Install(s)
	if err != nil {
		t.Fatal(err)
	}
	plist := files[0]
	if plist.Path != filepath.Join(m.Home, "Library/LaunchAgents/io.github.mkrajinov.fritzbox.watch-home.plist") {
		t.Errorf("path %s", plist.Path)
	}
	for _, want := range []string{
		"<string>/opt/homebrew/opt/fritzbox/bin/fritzbox</string>\n\t\t<string>--router</string>\n\t\t<string>home</string>\n\t\t<string>watch</string>",
		"<string>https://ntfy.sh/a&amp;b</string>", "<key>KeepAlive</key>", "<key>FB_NTFY_TOKEN</key>\n\t\t<string>tk&lt;1&gt;</string>",
		"Library/Logs/fritzbox/watch-home.log",
	} {
		if !strings.Contains(plist.Content, want) {
			t.Errorf("plist missing %q:\n%s", want, plist.Content)
		}
	}
	if st, _ := os.Stat(plist.Path); st.Mode().Perm() != 0o600 {
		t.Errorf("plist mode %v (holds secrets)", st.Mode().Perm())
	}
	if !slices.Equal(r.calls[1], []string{"launchctl", "bootstrap", "gui/501", plist.Path}) {
		t.Errorf("launchctl calls: %v", r.calls)
	}
	names, _ := m.Installed()
	if !slices.Equal(names, []string{"watch-home"}) {
		t.Errorf("installed: %v", names)
	}
	if found, err := m.Uninstall(Spec{Job: "watch", Router: "home"}); !found || err != nil {
		t.Errorf("uninstall: %v %v", found, err)
	}
	if found, _ := m.Uninstall(Spec{Job: "watch", Router: "home"}); found {
		t.Error("second uninstall should find nothing")
	}
	if got := m.LogCommand(s, 20, true); !slices.Equal(got, []string{"tail", "-n", "20", "-F", m.LogPath(s)}) {
		t.Errorf("log command %v", got)
	}
}

func TestLaunchdPeriodic(t *testing.T) {
	m, _ := manager(t, "darwin")
	files, err := m.Files(Spec{Job: "backup", Every: 24 * time.Hour, Extra: []string{"--keep", "14"}})
	if err != nil {
		t.Fatal(err)
	}
	c := files[0].Content
	if !strings.Contains(c, "<key>StartInterval</key>\n\t<integer>86400</integer>") || strings.Contains(c, "KeepAlive") ||
		!strings.Contains(c, "<string>backup</string>\n\t\t<string>run</string>\n\t\t<string>--keep</string>\n\t\t<string>14</string>") {
		t.Errorf("backup plist:\n%s", c)
	}
}

func TestSystemdInstall(t *testing.T) {
	m, r := manager(t, "linux")
	m.Exe = "/home/linuxbrew/.linuxbrew/opt/fritzbox/bin/fritzbox"
	s := Spec{Job: "backup", Router: "my box", Every: 12 * time.Hour, Extra: []string{"--dir", "/srv/fritz backups"}, Env: map[string]string{"FB_BACKUP_PASSWORD": `pa"ss`}}
	files, err := m.Install(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("files: %+v", files)
	}
	unit, env, tmr := files[0], files[1], files[2]
	if unit.Path != filepath.Join(m.Home, ".config/systemd/user/fritzbox-backup-my_box.service") {
		t.Errorf("unit path %s", unit.Path)
	}
	for _, want := range []string{"Type=oneshot", "EnvironmentFile=" + env.Path,
		`ExecStart=/home/linuxbrew/.linuxbrew/opt/fritzbox/bin/fritzbox --router "my box" backup run --dir "/srv/fritz backups"`} {
		if !strings.Contains(unit.Content, want) {
			t.Errorf("unit missing %q:\n%s", want, unit.Content)
		}
	}
	if strings.Contains(unit.Content, "pa") {
		t.Error("secret must not be in the unit file")
	}
	if env.Content != `FB_BACKUP_PASSWORD="pa\"ss"`+"\n" {
		t.Errorf("env file: %q", env.Content)
	}
	if st, _ := os.Stat(env.Path); st.Mode().Perm() != 0o600 {
		t.Errorf("env file mode %v", st.Mode().Perm())
	}
	if !strings.Contains(tmr.Content, "OnUnitActiveSec=43200s") || !strings.Contains(tmr.Content, "Unit=fritzbox-backup-my_box.service") {
		t.Errorf("timer:\n%s", tmr.Content)
	}
	if !slices.ContainsFunc(r.calls, func(c []string) bool {
		return slices.Equal(c, []string{"systemctl", "--user", "enable", "--now", "fritzbox-backup-my_box.timer"})
	}) {
		t.Errorf("systemctl calls: %v", r.calls)
	}
	if found, err := m.Uninstall(Spec{Job: "backup", Router: "my box"}); !found || err != nil {
		t.Errorf("uninstall: %v %v", found, err)
	}
	for _, f := range files {
		if _, err := os.Stat(f.Path); err == nil {
			t.Errorf("%s not removed", f.Path)
		}
	}
}

func TestValidate(t *testing.T) {
	for _, s := range []Spec{{Job: "nope"}, {Job: "backup"}, {Job: "backup", Every: time.Second}, {Job: "watch", Every: time.Hour}} {
		if err := s.Validate(); err == nil {
			t.Errorf("%+v should be invalid", s)
		}
	}
	m, _ := manager(t, "windows")
	if _, err := m.Files(Spec{Job: "watch"}); err == nil {
		t.Error("windows should be unsupported")
	}
}
