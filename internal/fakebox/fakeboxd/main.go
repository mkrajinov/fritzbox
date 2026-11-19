// Command fakeboxd runs the fake FRITZ!Box from internal/fakebox, for trying fritzbox without a
// router (TUI screenshots, the Grafana setup):
//
//	go run ./internal/fakebox/fakeboxd
//
// It prints a config file and the password to use and serves until interrupted.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/mkrajinov/fritzbox/internal/fakebox"
)

func main() {
	fb := fakebox.New()
	defer fb.Close()
	host, port := fb.HostPort()
	dir, err := os.MkdirTemp("", "fakebox")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	cfg := filepath.Join(dir, "config.yaml")
	data := fmt.Sprintf("default: fake\nrouters:\n  fake:\n    host: %s\n    port: %d\n    web_port: %d\n    user: %s\n", host, port, port, fakebox.User)
	if err := os.WriteFile(cfg, []byte(data), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("fake FRITZ!Box on %s:%d\n\n  export FBCTL_CONFIG=%s FRITZ_PASSWORD=%s\n  fritzbox status\n\nctrl+c stops it.\n", host, port, cfg, fakebox.Password)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}
