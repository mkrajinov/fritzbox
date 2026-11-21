package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

// Secrets go to the OS keychain (macOS Keychain, Secret Service on Linux). Headless Linux
// machines often have no Secret Service (no D-Bus session, no gnome-keyring), so when the
// keychain can't be used the secret is written to a file only the user can read instead,
// like gh and docker do. Reads try the keychain first, then the file.

// SecretsDir is where secrets live when there is no keychain: next to the config file.
func SecretsDir() string { return filepath.Join(filepath.Dir(DefaultPath()), "secrets") }

func secretFile(account string) string {
	return filepath.Join(SecretsDir(), url.PathEscape(account))
}

// storeSecret saves a secret. It returns the file path when the keychain was not available
// and the secret went to a file, or "" when it is in the keychain.
func storeSecret(account, secret string) (string, error) {
	kerr := keyring.Set(keyringService, account, secret)
	if kerr == nil {
		_ = os.Remove(secretFile(account)) // an older file copy would be stale
		return "", nil
	}
	p := secretFile(account)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", fmt.Errorf("keychain: %v; file: %w", kerr, err)
	}
	if err := os.Chmod(filepath.Dir(p), 0o700); err != nil {
		return "", fmt.Errorf("keychain: %v; file: %w", kerr, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("keychain: %v; file: %w", kerr, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(secret); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return "", err
	}
	return p, nil
}

// readSecret returns the secret, or "" and nil when none is stored.
func readSecret(account string) (string, error) {
	if pw, err := keyring.Get(keyringService, account); err == nil {
		return pw, nil
	}
	data, err := os.ReadFile(secretFile(account))
	if err == nil {
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", nil // not in the keychain (or no keychain) and no file: nothing stored
	}
	return "", err
}

// deleteSecret removes the secret from the keychain and the file (missing is not an error).
func deleteSecret(account string) error {
	_ = keyring.Delete(keyringService, account) // not found, or no keychain at all
	if err := os.Remove(secretFile(account)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
