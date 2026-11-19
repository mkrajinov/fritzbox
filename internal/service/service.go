// Package service installs fritzbox jobs (watch, exporter, scheduled backups) as user services:
// launchd agents on macOS, systemd user units on Linux.
package service

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Jobs that can be installed. Long-running jobs restart when they exit; backup runs periodically.
var Jobs = map[string]struct {
	Args     []string
	Periodic bool
	Short    string
}{
	"watch":    {[]string{"watch"}, false, "notifications, presence tracking and forwarding (fb watch)"},
	"exporter": {[]string{"exporter"}, false, "Prometheus metrics (fb exporter)"},
	"backup":   {[]string{"backup", "run"}, true, "configuration backups with rotation (fb backup run)"},
}

// SecretEnv are environment variables copied into the service when set at install time. They are
// written to a file only the user can read (the plist, or an EnvironmentFile on Linux).
var SecretEnv = []string{"FB_NTFY_URL", "FB_NTFY_TOKEN", "FB_WEBHOOK_URL", "FB_MQTT_URL", "FB_MQTT_PASSWORD", "FRITZ_PASSWORD", "FBCTL_CONFIG", "FB_BACKUP_PASSWORD"}

// Spec describes one service.
type Spec struct {
	Job    string        // watch, exporter or backup
	Router string        // optional profile; becomes --router and part of the service name
	Args   []string      // global flags placed before the job (e.g. --config)
	Extra  []string      // flags for the job itself
	Every  time.Duration // periodic jobs: interval
	Env    map[string]string
}

// Manager talks to the service manager. Fields are injectable for tests.
type Manager struct {
	GOOS       string
	Home       string
	ConfigHome string // $XDG_CONFIG_HOME; empty = ~/.config
	UID        int
	Exe        string // absolute path of the fritzbox binary to run
	Run        func(name string, args ...string) ([]byte, error)
}

// New returns a manager for this system.
func New() (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	exe, err := Executable()
	if err != nil {
		return nil, err
	}
	return &Manager{GOOS: runtime.GOOS, Home: home, ConfigHome: os.Getenv("XDG_CONFIG_HOME"), UID: os.Getuid(), Exe: exe, Run: func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}}, nil
}

var cellar = regexp.MustCompile(`^(.*)/Cellar/([^/]+)/[^/]+/(.*)$`)

// Executable returns the path services should run. Homebrew's versioned Cellar path is
// replaced by the stable opt link, so a service keeps working after `brew upgrade`; a binary
// built by `go run` is refused.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return stablePath(exe, func(p string) bool { _, err := os.Stat(p); return err == nil })
}

func stablePath(exe string, exists func(string) bool) (string, error) {
	if strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return "", errors.New("this is a temporary `go run` binary; install fritzbox (brew, go install or make build) and run the installed one")
	}
	if m := cellar.FindStringSubmatch(exe); m != nil {
		opt := filepath.Join(m[1], "opt", m[2], m[3])
		if exists(opt) {
			return opt, nil
		}
	}
	return exe, nil
}

// Name is the service's short name: the job, plus the router when one is given.
func (s Spec) Name() string {
	if s.Router == "" {
		return s.Job
	}
	return s.Job + "-" + sanitize(s.Router)
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}

const labelPrefix = "io.github.mkrajinov.fritzbox."

// Label is the launchd label, e.g. io.github.mkrajinov.fritzbox.watch-home.
func (s Spec) Label() string { return labelPrefix + s.Name() }

// Unit is the systemd unit name without suffix, e.g. fritzbox-watch-home.
func (s Spec) Unit() string { return "fritzbox-" + s.Name() }

// Command is the full command line of the service.
func (m *Manager) Command(s Spec) []string {
	cmd := []string{m.Exe}
	cmd = append(cmd, s.Args...)
	if s.Router != "" {
		cmd = append(cmd, "--router", s.Router)
	}
	cmd = append(cmd, Jobs[s.Job].Args...)
	return append(cmd, s.Extra...)
}

// Validate checks a spec.
func (s Spec) Validate() error {
	j, ok := Jobs[s.Job]
	if !ok {
		names := make([]string, 0, len(Jobs))
		for n := range Jobs {
			names = append(names, n)
		}
		sort.Strings(names)
		return fmt.Errorf("unknown job %q: use %s", s.Job, strings.Join(names, ", "))
	}
	if j.Periodic && s.Every < time.Minute {
		return errors.New("--every must be at least 1m")
	}
	if !j.Periodic && s.Every != 0 {
		return fmt.Errorf("%s runs continuously; --every is only for backup", s.Job)
	}
	return nil
}

// File is a file the installer writes.
type File struct {
	Path    string
	Content string
	Mode    os.FileMode
}

func (m *Manager) logDir() string { return filepath.Join(m.Home, "Library", "Logs", "fritzbox") }

func (m *Manager) configHome() string {
	if m.ConfigHome != "" {
		return m.ConfigHome
	}
	return filepath.Join(m.Home, ".config")
}

func (m *Manager) systemdDir() string { return filepath.Join(m.configHome(), "systemd", "user") }

// LogPath is where a launchd job logs (macOS).
func (m *Manager) LogPath(s Spec) string { return filepath.Join(m.logDir(), s.Name()+".log") }

// Files returns the files Install writes for a spec.
func (m *Manager) Files(s Spec) ([]File, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	switch m.GOOS {
	case "darwin":
		return []File{{Path: filepath.Join(m.Home, "Library", "LaunchAgents", s.Label()+".plist"), Content: m.plist(s), Mode: 0o600}}, nil
	case "linux":
		dir := m.systemdDir()
		files := []File{{Path: filepath.Join(dir, s.Unit()+".service"), Content: m.unit(s), Mode: 0o644}}
		if len(s.Env) > 0 {
			files = append(files, File{Path: m.envFile(s), Content: envFileContent(s.Env), Mode: 0o600})
		}
		if Jobs[s.Job].Periodic {
			files = append(files, File{Path: filepath.Join(dir, s.Unit()+".timer"), Content: timer(s), Mode: 0o644})
		}
		return files, nil
	}
	return nil, fmt.Errorf("services are supported on macOS (launchd) and Linux (systemd), not %s", m.GOOS)
}

func (m *Manager) envFile(s Spec) string {
	return filepath.Join(m.configHome(), "fbctl", "service", s.Unit()+".env")
}

func (m *Manager) plist(s Spec) string {
	var b bytes.Buffer
	esc := html.EscapeString
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + esc(s.Label()) + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range m.Command(s) {
		b.WriteString("\t\t<string>" + esc(a) + "</string>\n")
	}
	b.WriteString("\t</array>\n\t<key>RunAtLoad</key>\n\t<true/>\n")
	if Jobs[s.Job].Periodic {
		fmt.Fprintf(&b, "\t<key>StartInterval</key>\n\t<integer>%d</integer>\n", int(s.Every.Seconds()))
	} else {
		b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n\t<key>ThrottleInterval</key>\n\t<integer>30</integer>\n")
	}
	if len(s.Env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, k := range sortedKeys(s.Env) {
			b.WriteString("\t\t<key>" + esc(k) + "</key>\n\t\t<string>" + esc(s.Env[k]) + "</string>\n")
		}
		b.WriteString("\t</dict>\n")
	}
	log := esc(m.LogPath(s))
	b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + log + "</string>\n")
	b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + log + "</string>\n")
	b.WriteString("\t<key>ProcessType</key>\n\t<string>Background</string>\n</dict>\n</plist>\n")
	return b.String()
}

func (m *Manager) unit(s Spec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=fritzbox %s\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\n", Jobs[s.Job].Short)
	if Jobs[s.Job].Periodic {
		b.WriteString("Type=oneshot\n")
	} else {
		b.WriteString("Restart=on-failure\nRestartSec=30\n")
	}
	if len(s.Env) > 0 {
		b.WriteString("EnvironmentFile=" + m.envFile(s) + "\n")
	}
	b.WriteString("ExecStart=" + systemdQuote(m.Command(s)) + "\n")
	if !Jobs[s.Job].Periodic {
		b.WriteString("\n[Install]\nWantedBy=default.target\n")
	}
	return b.String()
}

func timer(s Spec) string {
	return fmt.Sprintf("[Unit]\nDescription=fritzbox %s, every %s\n\n[Timer]\nOnBootSec=5min\nOnUnitActiveSec=%ds\nUnit=%s.service\n\n[Install]\nWantedBy=timers.target\n",
		Jobs[s.Job].Short, s.Every, int(s.Every.Seconds()), s.Unit())
}

// systemdQuote quotes arguments for ExecStart (double quotes, backslash escapes, % doubled).
func systemdQuote(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		a = strings.ReplaceAll(a, "%", "%%")
		if a != "" && !strings.ContainsAny(a, " \t\"'\\;$") {
			out[i] = a
			continue
		}
		out[i] = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", "$$").Replace(a) + `"`
	}
	return strings.Join(out, " ")
}

func envFileContent(env map[string]string) string {
	var b strings.Builder
	for _, k := range sortedKeys(env) {
		b.WriteString(k + "=" + strconv.Quote(env[k]) + "\n")
	}
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Install writes the files and starts the service.
func (m *Manager) Install(s Spec) ([]File, error) {
	files, err := m.Files(s)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(f.Path, []byte(f.Content), f.Mode); err != nil {
			return nil, err
		}
		if err := os.Chmod(f.Path, f.Mode); err != nil { // WriteFile keeps the mode of an existing file
			return nil, err
		}
	}
	switch m.GOOS {
	case "darwin":
		if err := os.MkdirAll(m.logDir(), 0o700); err != nil {
			return nil, err
		}
		domain := "gui/" + strconv.Itoa(m.UID)
		_, _ = m.Run("launchctl", "bootout", domain+"/"+s.Label()) // replace a running older version
		if out, err := m.Run("launchctl", "bootstrap", domain, files[0].Path); err != nil {
			return files, fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		if out, err := m.Run("systemctl", "--user", "daemon-reload"); err != nil {
			return files, fmt.Errorf("systemctl daemon-reload: %v: %s", err, strings.TrimSpace(string(out)))
		}
		target := s.Unit() + ".service"
		if Jobs[s.Job].Periodic {
			target = s.Unit() + ".timer"
		}
		if out, err := m.Run("systemctl", "--user", "enable", "--now", target); err != nil {
			return files, fmt.Errorf("systemctl enable: %v: %s", err, strings.TrimSpace(string(out)))
		}
		if !Jobs[s.Job].Periodic {
			_, _ = m.Run("systemctl", "--user", "restart", target) // pick up a changed unit
		}
	}
	return files, nil
}

// Uninstall stops the service and removes its files. It reports false when nothing was installed.
func (m *Manager) Uninstall(s Spec) (bool, error) {
	if _, ok := Jobs[s.Job]; !ok {
		return false, s.Validate()
	}
	var paths []string
	switch m.GOOS {
	case "darwin":
		paths = []string{filepath.Join(m.Home, "Library", "LaunchAgents", s.Label()+".plist")}
	case "linux":
		dir := m.systemdDir()
		paths = []string{filepath.Join(dir, s.Unit()+".service"), filepath.Join(dir, s.Unit()+".timer"), m.envFile(s)}
	default:
		return false, fmt.Errorf("unsupported system %s", m.GOOS)
	}
	switch m.GOOS {
	case "darwin":
		_, _ = m.Run("launchctl", "bootout", "gui/"+strconv.Itoa(m.UID)+"/"+s.Label())
	case "linux":
		_, _ = m.Run("systemctl", "--user", "disable", "--now", s.Unit()+".timer", s.Unit()+".service")
	}
	found := false
	for _, p := range paths {
		err := os.Remove(p)
		if err == nil {
			found = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return found, err
		}
	}
	if m.GOOS == "linux" {
		_, _ = m.Run("systemctl", "--user", "daemon-reload")
	}
	return found, nil
}

// Installed lists the installed services by name (e.g. watch, backup-home).
func (m *Manager) Installed() ([]string, error) {
	var dir, prefix, suffix string
	switch m.GOOS {
	case "darwin":
		dir, prefix, suffix = filepath.Join(m.Home, "Library", "LaunchAgents"), labelPrefix, ".plist"
	case "linux":
		dir, prefix, suffix = m.systemdDir(), "fritzbox-", ".service"
	default:
		return nil, nil
	}
	des, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, de := range des {
		n := de.Name()
		if strings.HasPrefix(n, prefix) && strings.HasSuffix(n, suffix) {
			out = append(out, strings.TrimSuffix(strings.TrimPrefix(n, prefix), suffix))
		}
	}
	return out, nil
}

// Status returns the service manager's view of a service.
func (m *Manager) Status(s Spec) (string, error) {
	switch m.GOOS {
	case "darwin":
		out, err := m.Run("launchctl", "print", "gui/"+strconv.Itoa(m.UID)+"/"+s.Label())
		if err != nil {
			return "", fmt.Errorf("%s is not loaded (install it with `fb service install %s`)", s.Name(), s.Job)
		}
		var keep []string
		for _, l := range strings.Split(string(out), "\n") {
			t := strings.TrimSpace(l)
			for _, p := range []string{"state = ", "pid = ", "last exit code = ", "runs = ", "run interval = "} {
				if strings.HasPrefix(t, p) {
					keep = append(keep, t)
				}
			}
		}
		keep = append(keep, "log = "+m.LogPath(s))
		return strings.Join(keep, "\n"), nil
	case "linux":
		args := []string{"--user", "status", "--no-pager", "--lines=0", s.Unit() + ".service"}
		if Jobs[s.Job].Periodic {
			args = append(args, s.Unit()+".timer")
		}
		out, _ := m.Run("systemctl", args...) // exits non-zero for inactive units
		return strings.TrimSpace(string(out)), nil
	}
	return "", fmt.Errorf("unsupported system %s", m.GOOS)
}

// LogCommand returns the command that shows a service's log.
func (m *Manager) LogCommand(s Spec, lines int, follow bool) []string {
	n := strconv.Itoa(lines)
	if m.GOOS == "darwin" {
		cmd := []string{"tail", "-n", n}
		if follow {
			cmd = append(cmd, "-F")
		}
		return append(cmd, m.LogPath(s))
	}
	cmd := []string{"journalctl", "--user", "-u", s.Unit() + ".service", "-n", n, "--no-pager"}
	if follow {
		cmd = append(cmd, "-f")
	}
	return cmd
}
