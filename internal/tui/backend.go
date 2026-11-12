// Package tui is the interactive Bubble Tea front end.
package tui

import (
	"context"

	"github.com/mkrajinovic/fbctl/internal/aha"
	"github.com/mkrajinovic/fbctl/internal/fritz"
)

// Backend is what the TUI needs from a router. *fritz.Box implements it; tests use a fake.
type Backend interface {
	Status(ctx context.Context) (fritz.Status, error)
	Rates(ctx context.Context) (fritz.Rates, error)
	Hosts(ctx context.Context) ([]fritz.Host, error)
	WakeOnLAN(ctx context.Context, mac string) error
	WLANs(ctx context.Context) ([]fritz.WLAN, error)
	SetWLAN(ctx context.Context, index int, enable bool) error
	WLANPassword(ctx context.Context, index int) (string, error)
	Calls(ctx context.Context, days int, types ...int) ([]fritz.Call, error)
	SmartDevices(ctx context.Context) ([]aha.Device, error)
	SetSwitch(ctx context.Context, ain, state string) error
	SetThermostatRaw(ctx context.Context, ain string, v int) error
	Reconnect(ctx context.Context) error
	Reboot(ctx context.Context) error
	Mesh(ctx context.Context) ([]fritz.MeshNode, error)
	DeviceLog(ctx context.Context, group string) ([]fritz.LogEvent, error)
	SetInternetBlocked(ctx context.Context, ip string, blocked bool) error
}

// Connector opens a backend for a router profile name and returns the host for the call monitor.
type Connector func(ctx context.Context, name string) (Backend, string, error)

var _ Backend = (*fritz.Box)(nil)
