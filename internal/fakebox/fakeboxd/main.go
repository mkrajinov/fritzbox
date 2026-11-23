// Command fakeboxd runs the fake FRITZ!Box from internal/fakebox, for trying fritzbox without a
// router (TUI screenshots, the Grafana setup):
//
//	go run ./internal/fakebox/fakeboxd [-name home] [-env file]
//
// It prints a config file and the password to use and serves until interrupted. With -env it
// also writes the export line to a file, for scripts such as docs/demo.tape.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/mkrajinov/fritzbox/internal/fakebox"
)

func main() {
	name := flag.String("name", "fake", "router profile name")
	envFile := flag.String("env", "", "also write the export line to this file")
	flag.Parse()
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
	data := fmt.Sprintf("default: %s\nrouters:\n  %s:\n    host: %s\n    port: %d\n    web_port: %d\n    user: %s\n", *name, *name, host, port, port, fakebox.User)
	if err := os.WriteFile(cfg, []byte(data), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	export := fmt.Sprintf("export FBCTL_CONFIG=%s FRITZ_PASSWORD=%s", cfg, fakebox.Password)
	if *envFile != "" {
		if err := os.WriteFile(*envFile, []byte(export+"\n"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer os.Remove(*envFile)
	}
	fmt.Printf("fake FRITZ!Box on %s:%d\n\n  %s\n  fritzbox status\n\nctrl+c stops it.\n", host, port, export)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}
