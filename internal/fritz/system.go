package fritz

import (
	"context"
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mkrajinov/fritzbox/internal/config"
	"github.com/mkrajinov/fritzbox/internal/tr064"
)

// LogEvent is one entry of the box's event log.
type LogEvent struct {
	ID    int       `json:"id"`
	Group string    `json:"group"` // sys, net, fon, wlan, usb
	Time  time.Time `json:"time"`
	Msg   string    `json:"msg"`
}

type deviceLogXML struct {
	Events []struct {
		ID    string `xml:"id"`
		Group string `xml:"group"`
		Date  string `xml:"date"`
		Time  string `xml:"time"`
		Msg   string `xml:"msg"`
	} `xml:"Event"`
}

// ParseDeviceLog parses the XML behind DeviceInfo1.X_AVM-DE_GetDeviceLogPath.
func ParseDeviceLog(data []byte, loc *time.Location) ([]LogEvent, error) {
	var l deviceLogXML
	if err := xml.Unmarshal(data, &l); err != nil {
		return nil, err
	}
	out := make([]LogEvent, 0, len(l.Events))
	for _, e := range l.Events {
		t, _ := time.ParseInLocation("02.01.06 15:04:05", e.Date+" "+e.Time, loc)
		out = append(out, LogEvent{ID: atoi(e.ID), Group: e.Group, Time: t, Msg: strings.TrimSpace(e.Msg)})
	}
	return out, nil
}

// LogGroups are the filters the box understands.
var LogGroups = []string{"sys", "net", "fon", "wlan", "usb"}

// DeviceLog returns the event log, newest first. group "" means all.
func (b *Box) DeviceLog(ctx context.Context, group string) ([]LogEvent, error) {
	r, err := b.call(ctx, "DeviceInfo1", "X_AVM-DE_GetDeviceLogPath", nil)
	if err != nil {
		return nil, err
	}
	p := r["NewDeviceLogPath"]
	if group != "" {
		p += "&filter=" + url.QueryEscape(group)
	}
	data, err := b.TR.Get(ctx, p)
	if err != nil {
		return nil, err
	}
	return ParseDeviceLog(data, time.Local)
}

// RenameHost sets the name the box shows for a device.
func (b *Box) RenameHost(ctx context.Context, mac, name string) error {
	_, err := b.call(ctx, "Hosts1", "X_AVM-DE_SetHostNameByMACAddress", tr064.Args{"NewMACAddress": mac, "NewHostName": name})
	return err
}

// GeneratePassword returns a random WLAN passphrase from an unambiguous alphabet.
func GeneratePassword(n int) (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	if n < 8 || n > 63 {
		return "", fmt.Errorf("WLAN passwords must be 8–63 characters, got %d", n)
	}
	var sb strings.Builder
	for range n {
		i, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		sb.WriteByte(alphabet[i.Int64()])
	}
	return sb.String(), nil
}

// SetWLANPassword sets a new WPA passphrase. Like fritzconnection, it also sends a fresh
// pre-shared key built from the box's allowed PSK characters.
func (b *Box) SetWLANPassword(ctx context.Context, index int, password string) error {
	if l := len(password); l < 8 || l > 63 {
		return fmt.Errorf("WLAN passwords must be 8–63 characters, got %d", l)
	}
	svc := "WLANConfiguration" + strconv.Itoa(index)
	info, err := b.call(ctx, svc, "GetInfo", nil)
	if err != nil {
		return err
	}
	chars := info["NewAllowedCharsPSK"]
	n := atoi(info["NewMaxCharsPSK"])
	if chars == "" || n == 0 {
		chars, n = "0123456789ABCDEF", 64
	}
	var psk strings.Builder
	for range n {
		i, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return err
		}
		psk.WriteByte(chars[i.Int64()])
	}
	_, err = b.call(ctx, svc, "SetSecurityKeys", tr064.Args{
		"NewKeyPassphrase": password,
		"NewPreSharedKey":  strings.ToUpper(psk.String()),
		"NewWEPKey0":       "", "NewWEPKey1": "", "NewWEPKey2": "", "NewWEPKey3": "",
	})
	return err
}

// RouterStatus is the result of polling one profile in StatusAll.
type RouterStatus struct {
	Router string `json:"router"`
	Host   string `json:"host"`
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Opener connects to a profile by name.
type Opener func(ctx context.Context, name string, r *config.Router) (*Box, error)

// StatusAll polls every profile concurrently. Failures are reported per router, never fatal.
func StatusAll(ctx context.Context, cfg *config.Config, open Opener) []RouterStatus {
	names := cfg.Names()
	out := make([]RouterStatus, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		r := cfg.Routers[n]
		out[i] = RouterStatus{Router: n, Host: r.Host}
		wg.Go(func() {
			b, err := open(ctx, n, r)
			if err == nil {
				out[i].Status, err = b.Status(ctx)
				if err != nil && out[i].Status.Model != "" {
					err = nil // partial status is still useful
				}
			}
			if err != nil {
				out[i].Error = err.Error()
			}
		})
	}
	wg.Wait()
	return out
}
