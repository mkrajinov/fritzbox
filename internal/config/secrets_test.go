package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestSecretFileFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FBCTL_CONFIG", filepath.Join(dir, "config.yaml"))
	t.Setenv("FRITZ_PASSWORD", "")
	keyring.MockInitWithError(errors.New(`exec: "dbus-launch": executable file not found in $PATH`))

	file, err := StorePassword("home", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if file != filepath.Join(dir, "secrets", "home") {
		t.Errorf("file %q", file)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(file)); st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", st.Mode().Perm())
	}
	if pw, err := Password("home", &Router{}); pw != "s3cret" || err != nil {
		t.Errorf("password %q %v", pw, err)
	}

	if file, err := StoreBackupPassword("my/box", "b"); err != nil || filepath.Base(file) != "backup:my%2Fbox" {
		t.Errorf("backup file %q %v", file, err)
	}
	if pw, _ := BackupPassword("my/box"); pw != "b" {
		t.Errorf("backup password %q", pw)
	}
	if err := DeleteBackupPassword("my/box"); err != nil {
		t.Fatal(err)
	}
	if pw, err := BackupPassword("my/box"); pw != "" || err != nil {
		t.Errorf("after delete: %q %v", pw, err)
	}
	if err := DeletePassword("home"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("password file not removed")
	}

	// With a working keychain the secret goes there, and a stale file copy is removed.
	keyring.MockInit()
	if _, err := StorePassword("home", "old"); err != nil {
		t.Fatal(err)
	}
	keyring.MockInitWithError(errors.New("no keychain"))
	_, _ = StorePassword("other", "x")
	keyring.MockInit()
	if file, err := StorePassword("other", "y"); file != "" || err != nil {
		t.Errorf("keychain store: %q %v", file, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets", "other")); !os.IsNotExist(err) {
		t.Error("stale file copy kept")
	}
	if pw, _ := Password("other", &Router{}); pw != "y" {
		t.Errorf("keychain read %q", pw)
	}
}
