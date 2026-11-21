// Package config manages named router profiles and their credentials.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const keyringService = "fbctl"

// Router is one FRITZ!Box profile. Passwords are never stored in the file.
type Router struct {
	Host        string `yaml:"host"`
	User        string `yaml:"user,omitempty"`
	TLS         bool   `yaml:"tls,omitempty"`
	Port        int    `yaml:"port,omitempty"`
	Fingerprint string `yaml:"tls_fingerprint,omitempty"`
	// WebPort is the web UI / AHA port (default 80, or 443 with TLS).
	WebPort int `yaml:"web_port,omitempty"`
	// PasswordCmd is run via `sh -c` and its trimmed stdout is used as password
	// (e.g. `op read op://Private/fritzbox/password`).
	PasswordCmd string `yaml:"password_cmd,omitempty"`
}

// Config is the on-disk configuration.
type Config struct {
	Default string             `yaml:"default,omitempty"`
	Routers map[string]*Router `yaml:"routers"`

	path string
}

// DefaultPath is $FBCTL_CONFIG or ~/.config/fbctl/config.yaml.
func DefaultPath() string {
	if p := os.Getenv("FBCTL_CONFIG"); p != "" {
		return p
	}
	// XDG-style ~/.config on macOS too; it is what CLI users expect.
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "fbctl", "config.yaml")
}

// Load reads the config; a missing file yields an empty config.
func Load(path string) (*Config, error) {
	c := &Config{Routers: map[string]*Router{}, path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Routers == nil {
		c.Routers = map[string]*Router{}
	}
	return c, nil
}

// Save writes the config with 0600 permissions.
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Path returns the file location.
func (c *Config) Path() string { return c.path }

// Names returns router names sorted.
func (c *Config) Names() []string {
	n := make([]string, 0, len(c.Routers))
	for k := range c.Routers {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// Resolve picks a router by name, falling back to $FBCTL_ROUTER, the default, or the only one.
// If no profile exists but $FRITZ_ADDRESS is set, an ad-hoc profile from env vars is returned
// (fritzconnection-compatible: FRITZ_ADDRESS, FRITZ_USERNAME, FRITZ_PASSWORD).
func (c *Config) Resolve(name string) (string, *Router, error) {
	if name == "" {
		name = os.Getenv("FBCTL_ROUTER")
	}
	if name == "" {
		name = c.Default
	}
	if name == "" && len(c.Routers) == 1 {
		name = c.Names()[0]
	}
	if name == "" {
		if addr := os.Getenv("FRITZ_ADDRESS"); addr != "" {
			return "env", &Router{Host: addr, User: os.Getenv("FRITZ_USERNAME")}, nil
		}
		if len(c.Routers) == 0 {
			return "", nil, errors.New("no router configured: run `fb router add <name> --host fritz.box`")
		}
		return "", nil, fmt.Errorf("several routers configured (%s): pass --router or run `fb router use <name>`", strings.Join(c.Names(), ", "))
	}
	r, ok := c.Routers[name]
	if !ok {
		return "", nil, fmt.Errorf("unknown router %q (known: %s)", name, strings.Join(c.Names(), ", "))
	}
	return name, r, nil
}

// Password resolves the password: keychain (or its file fallback) → $FRITZ_PASSWORD → password_cmd.
func Password(name string, r *Router) (string, error) {
	if name != "env" {
		// Errors fall through to the other sources.
		if pw, err := readSecret(name); err == nil && pw != "" {
			return pw, nil
		}
	}
	if pw := os.Getenv("FRITZ_PASSWORD"); pw != "" {
		return pw, nil
	}
	if r.PasswordCmd != "" {
		out, err := exec.Command("sh", "-c", r.PasswordCmd).Output()
		if err != nil {
			return "", fmt.Errorf("password_cmd: %w", err)
		}
		return strings.TrimRight(string(out), "\r\n"), nil
	}
	return "", nil
}

// StorePassword saves the password in the OS keychain. Without a usable keychain (headless
// Linux) it goes to a file readable only by the user; its path is returned in that case.
func StorePassword(name, pw string) (string, error) { return storeSecret(name, pw) }

// DeletePassword removes the stored password (missing entries are not an error).
func DeletePassword(name string) error { return deleteSecret(name) }

// Backup passwords are stored under "backup:<router>", so scheduled backups can run
// without a prompt and without the password in a service file.
func backupAccount(router string) string { return "backup:" + router }

// StoreBackupPassword saves the password used to encrypt configuration exports of a router.
// Like StorePassword it returns the file path when there was no keychain.
func StoreBackupPassword(router, pw string) (string, error) {
	return storeSecret(backupAccount(router), pw)
}

// BackupPassword returns the stored backup password, or "" when there is none.
func BackupPassword(router string) (string, error) { return readSecret(backupAccount(router)) }

// DeleteBackupPassword removes the stored backup password (missing entries are not an error).
func DeleteBackupPassword(router string) error { return deleteSecret(backupAccount(router)) }
