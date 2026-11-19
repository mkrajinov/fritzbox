// Package tui is the interactive Bubble Tea front end.
package tui

import (
	"context"

	"github.com/mkrajinov/fritzbox/internal/aha"
	"github.com/mkrajinov/fritzbox/internal/fritz"
)

// Backend is what the TUI needs from a router. *fritz.Box implements it; tests use a fake.
type Backend interface {
	Status(ctx context.Context) (fritz.Status, error)
	Rates(ctx context.Context) (fritz.Rates, error)
	Hosts(ctx context.Context) ([]fritz.Host, error)
	WLANAssociations(ctx context.Context) ([]fritz.WLANClient, error)
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
	WANAccess(ctx context.Context, ip string) (access string, disallowed bool, err error)
	RenameHost(ctx context.Context, mac, name string) error
	SetWLANPassword(ctx context.Context, index int, password string) error
	Phonebooks(ctx context.Context) ([]fritz.Phonebook, error)
	Contacts(ctx context.Context, id int) (fritz.Phonebook, []fritz.Contact, error)
	AddContact(ctx context.Context, book int, c fritz.Contact) (int, error)
	UpdateContact(ctx context.Context, book, id int, e fritz.ContactEdit) (fritz.Contact, error)
	DeleteContact(ctx context.Context, book, id int) error
	BlockNumber(ctx context.Context, number, name string) (id int, added bool, err error)

	SetSecondFactorHandler(f func(fritz.SecondFactorPrompt))

	HostInfo(ctx context.Context, ip string) (fritz.HostInfo, error)
	SetPriority(ctx context.Context, ip string, on bool) error
	SetAutoWOL(ctx context.Context, mac string, on bool) error
	FilterProfiles(ctx context.Context) ([]fritz.FilterProfile, error)
	SetFilterProfile(ctx context.Context, ip, profileID string) error
	AddTicket(ctx context.Context, ip string) (used, maxMin int, err error)

	StartWPS(ctx context.Context, index int, stop bool) (string, error)
	SetWLANGlobal(ctx context.Context, on bool) error
	SetSSID(ctx context.Context, index int, ssid string) error
	Channels(ctx context.Context, index int) (fritz.ChannelInfo, error)
	SetChannel(ctx context.Context, index, channel int) error
	SetHidden(ctx context.Context, index int, hidden bool) error

	TAMs(ctx context.Context) ([]fritz.TAM, error)
	SetTAM(ctx context.Context, index int, on bool) error
	Messages(ctx context.Context) ([]fritz.Message, error)
	MarkMessage(ctx context.Context, tam, index int, read bool) error
	DeleteMessage(ctx context.Context, tam, index int) error
	Deflections(ctx context.Context) ([]fritz.Deflection, error)
	SetDeflection(ctx context.Context, id int, on bool) error
	Dial(ctx context.Context, number string) error
	Hangup(ctx context.Context) error

	Templates(ctx context.Context) ([]aha.Template, error)
	ApplyTemplate(ctx context.Context, id string) error

	UpdateInfo(ctx context.Context) (fritz.UpdateInfo, error)
	CheckUpdate(ctx context.Context) error
	DoUpdate(ctx context.Context) (string, error)
	SetAutoUpdate(ctx context.Context, mode string) error
	Backup(ctx context.Context, password string) ([]byte, error)
	DHCP(ctx context.Context) (fritz.DHCP, error)
	SetDHCPRange(ctx context.Context, lo, hi string) error
	Remote(ctx context.Context) (fritz.Remote, error)
	SetRemoteHTTPS(ctx context.Context, on bool) (string, error)
	SetLetsEncrypt(ctx context.Context, on bool) error
	SetMyFritz(ctx context.Context, on bool) error
}

// Connector opens a backend for a router profile name and returns the host for the call monitor.
type Connector func(ctx context.Context, name string) (Backend, string, error)

var _ Backend = (*fritz.Box)(nil)
