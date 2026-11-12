package fritz

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/mkrajinovic/fbctl/internal/tr064"
)

// DeviceInfo is DeviceInfo1.GetInfo.
type DeviceInfo struct {
	Model    string        `json:"model"`
	Firmware string        `json:"firmware"`
	Serial   string        `json:"serial"`
	Uptime   time.Duration `json:"uptime_ns"`
}

// Status is a snapshot of the WAN side.
type Status struct {
	DeviceInfo
	HasWAN          bool          `json:"has_wan"`
	AccessType      string        `json:"access_type"` // DSL, Ethernet, X_AVM-DE_Fiber, ...
	LinkUp          bool          `json:"link_up"`
	ConnectionType  string        `json:"connection_service"` // WANPPPConnection1 or WANIPConnection1
	Connected       bool          `json:"connected"`
	ConnStatus      string        `json:"connection_status"`
	LastError       string        `json:"last_error,omitempty"`
	ConnUptime      time.Duration `json:"connection_uptime_ns"`
	ExternalIPv4    string        `json:"external_ipv4,omitempty"`
	ExternalIPv6    string        `json:"external_ipv6,omitempty"`
	MaxUpBits       int64         `json:"max_up_bits"`
	MaxDownBits     int64         `json:"max_down_bits"`
	TotalSent       int64         `json:"total_sent_bytes"`
	TotalReceived   int64         `json:"total_received_bytes"`
	UpdateAvailable string        `json:"update_available,omitempty"`
	Rates           Rates         `json:"rates"`
}

// Rates is the live throughput from X_AVM-DE_GetOnlineMonitor.
// History slices are newest-first, one sample per 5 s, as delivered by the box.
type Rates struct {
	Up, Down         int64   // current, bytes/s
	UpHist, DownHist []int64 // newest first
	MaxUp, MaxDown   int64   // bytes/s
}

// Info returns model/firmware/uptime.
func (b *Box) Info(ctx context.Context) (DeviceInfo, error) {
	r, err := b.call(ctx, "DeviceInfo1", "GetInfo", nil)
	if err != nil {
		return DeviceInfo{}, err
	}
	return DeviceInfo{
		Model:    r["NewModelName"],
		Firmware: r["NewSoftwareVersion"],
		Serial:   r["NewSerialNumber"],
		Uptime:   time.Duration(atoi64(r["NewUpTime"])) * time.Second,
	}, nil
}

// ConnectionService returns "WANPPPConnection1" or "WANIPConnection1" (cached),
// or "" when the box has no WAN (e.g. repeater or mesh satellite).
func (b *Box) ConnectionService(ctx context.Context) (string, error) {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	if b.connKnown {
		return b.connSvc, nil
	}
	if _, err := b.TR.Description(ctx); err != nil {
		return "", err
	}
	svc := ""
	if b.TR.HasService(ctx, "Layer3Forwarding1") {
		r, err := b.call(ctx, "Layer3Forwarding1", "GetDefaultConnectionService", nil)
		if err != nil {
			return "", err // not cached: a transient error must not stick
		}
		// "1.WANPPPConnection.1" → "WANPPPConnection1"
		p := strings.Split(r["NewDefaultConnectionService"], ".")
		switch {
		case len(p) >= 3:
			svc = p[1] + p[2]
		case b.TR.HasService(ctx, "WANPPPConnection1"):
			svc = "WANPPPConnection1"
		default:
			svc = "WANIPConnection1"
		}
	}
	b.connSvc, b.connKnown = svc, true
	return svc, nil
}

// Status gathers device and WAN status. Independent calls run concurrently.
func (b *Box) Status(ctx context.Context) (Status, error) {
	var (
		st   Status
		mu   sync.Mutex
		wg   sync.WaitGroup
		errs []error
	)
	run := func(f func() error) {
		wg.Go(func() {
			if err := f(); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		})
	}
	// Prime the description once so goroutines don't race to fetch it.
	if _, err := b.TR.Description(ctx); err != nil {
		return st, err
	}
	run(func() error {
		info, err := b.Info(ctx)
		mu.Lock()
		st.DeviceInfo = info
		mu.Unlock()
		return err
	})
	run(func() error {
		r, err := b.call(ctx, "UserInterface1", "GetInfo", nil)
		if err != nil {
			return nil // optional
		}
		mu.Lock()
		st.UpdateAvailable = r["NewX_AVM-DE_Version"]
		mu.Unlock()
		return nil
	})
	svc, err := b.ConnectionService(ctx)
	if err != nil {
		return st, err
	}
	st.HasWAN = svc != ""
	st.ConnectionType = svc
	if st.HasWAN {
		run(func() error {
			r, err := b.call(ctx, "WANCommonInterfaceConfig1", "GetCommonLinkProperties", nil)
			if err != nil {
				return err
			}
			mu.Lock()
			st.AccessType = r["NewWANAccessType"]
			st.LinkUp = r["NewPhysicalLinkStatus"] == "Up"
			st.MaxUpBits = atoi64(r["NewLayer1UpstreamMaxBitRate"])
			st.MaxDownBits = atoi64(r["NewLayer1DownstreamMaxBitRate"])
			mu.Unlock()
			return nil
		})
		run(func() error {
			r, err := b.call(ctx, svc, "GetStatusInfo", nil)
			if err != nil {
				return err
			}
			mu.Lock()
			st.ConnStatus = r["NewConnectionStatus"]
			st.Connected = st.ConnStatus == "Connected"
			if e := r["NewLastConnectionError"]; e != "ERROR_NONE" {
				st.LastError = e
			}
			st.ConnUptime = time.Duration(atoi64(r["NewUptime"])) * time.Second
			mu.Unlock()
			return nil
		})
		run(func() error {
			r, err := b.call(ctx, svc, "GetExternalIPAddress", nil)
			if err != nil {
				return err
			}
			mu.Lock()
			st.ExternalIPv4 = r["NewExternalIPAddress"]
			mu.Unlock()
			return nil
		})
		run(func() error {
			// Best effort: not every box/firmware offers IPv6 on this service.
			r, err := b.call(ctx, svc, "X_AVM_DE_GetExternalIPv6Address", nil)
			if err == nil {
				mu.Lock()
				st.ExternalIPv6 = r["NewExternalIPv6Address"]
				mu.Unlock()
			}
			return nil
		})
		run(func() error {
			s, err1 := b.call(ctx, "WANCommonInterfaceConfig1", "GetTotalBytesSent", nil)
			r, err2 := b.call(ctx, "WANCommonInterfaceConfig1", "GetTotalBytesReceived", nil)
			if err := errors.Join(err1, err2); err != nil {
				return err
			}
			mu.Lock()
			st.TotalSent = atoi64(s["NewTotalBytesSent"])
			st.TotalReceived = atoi64(r["NewTotalBytesReceived"])
			mu.Unlock()
			return nil
		})
		run(func() error {
			rt, err := b.Rates(ctx)
			if err != nil {
				return nil // optional, older firmware
			}
			mu.Lock()
			st.Rates = rt
			mu.Unlock()
			return nil
		})
	}
	wg.Wait()
	return st, errors.Join(errs...)
}

// Rates returns live throughput. Cheap enough to poll every few seconds.
func (b *Box) Rates(ctx context.Context) (Rates, error) {
	r, err := b.call(ctx, "WANCommonInterfaceConfig1", "X_AVM-DE_GetOnlineMonitor", tr064.Args{"NewSyncGroupIndex": "0"})
	if err != nil {
		return Rates{}, err
	}
	up := parseList(r["Newus_current_bps"])
	down := parseList(r["Newds_current_bps"])
	rt := Rates{UpHist: up, DownHist: down, MaxUp: atoi64(r["Newmax_us"]), MaxDown: atoi64(r["Newmax_ds"])}
	if len(up) > 0 {
		rt.Up = up[0]
	}
	if len(down) > 0 {
		rt.Down = down[0]
	}
	return rt, nil
}

func parseList(s string) []int64 {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		out = append(out, atoi64(p))
	}
	return out
}

// Reconnect drops the WAN connection so the provider assigns a new IP.
func (b *Box) Reconnect(ctx context.Context) error {
	svc, err := b.ConnectionService(ctx)
	if err != nil {
		return err
	}
	if svc == "" {
		return errors.New("this box has no WAN connection")
	}
	_, err = b.call(ctx, svc, "ForceTermination", nil)
	return err
}

// Reboot restarts the box.
func (b *Box) Reboot(ctx context.Context) error {
	_, err := b.call(ctx, "DeviceConfig1", "Reboot", nil)
	return err
}
