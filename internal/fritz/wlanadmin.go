package fritz

import (
	"context"
	"fmt"
	"strings"

	"github.com/mkrajinov/fritzbox/internal/tr064"
)

func wlanSvc(index int) string { return "WLANConfiguration" + itoa(index) }

// WPS states reported by X_AVM-DE_GetWPSInfo.
const (
	WPSActive  = "active"
	WPSSuccess = "success"
)

// StartWPS starts push-button WPS on a WLAN (about two minutes); stop cancels it.
func (b *Box) StartWPS(ctx context.Context, index int, stop bool) (string, error) {
	mode := "pbc"
	if stop {
		mode = "stop"
	}
	r, err := b.call(ctx, wlanSvc(index), "X_AVM-DE_SetWPSConfig", tr064.Args{"NewX_AVM-DE_WPSMode": mode})
	if err != nil {
		return "", err
	}
	return r["NewX_AVM-DE_WPSStatus"], nil
}

// WPSStatus reports the WPS state: off, inactive, active, success or err_*.
func (b *Box) WPSStatus(ctx context.Context, index int) (string, error) {
	r, err := b.call(ctx, wlanSvc(index), "X_AVM-DE_GetWPSInfo", nil)
	if err != nil {
		return "", err
	}
	return r["NewX_AVM-DE_WPSStatus"], nil
}

// SetWLANGlobal switches all radios on or off, like the WLAN button on the box.
func (b *Box) SetWLANGlobal(ctx context.Context, on bool) error {
	_, err := b.call(ctx, wlanSvc(1), "X_AVM-DE_SetWLANGlobalEnable", tr064.Args{"NewX_AVM-DE_WLANGlobalEnable": b01(on)})
	return err
}

// WLANGlobal reports whether the radios are on (the WLAN button state).
func (b *Box) WLANGlobal(ctx context.Context) (bool, error) {
	r, err := b.call(ctx, wlanSvc(1), "GetInfo", nil)
	if err != nil {
		return false, err
	}
	v, ok := r["NewX_AVM-DE_WLANGlobalEnable"]
	return !ok || bool01(v), nil
}

// SetSSID renames a WLAN, checked against the box's limits.
func (b *Box) SetSSID(ctx context.Context, index int, ssid string) error {
	info, err := b.call(ctx, wlanSvc(index), "GetInfo", nil)
	if err != nil {
		return err
	}
	lo, hi := atoi(info["NewMinCharsSSID"]), atoi(info["NewMaxCharsSSID"])
	if lo == 0 {
		lo = 1
	}
	if hi == 0 {
		hi = 32
	}
	if n := len(ssid); n < lo || n > hi {
		return fmt.Errorf("SSID must be %d–%d characters, got %d", lo, hi, n)
	}
	if allowed := info["NewAllowedCharsSSID"]; allowed != "" {
		for _, r := range ssid {
			if !strings.ContainsRune(allowed, r) {
				return fmt.Errorf("character %q is not allowed in an SSID", r)
			}
		}
	}
	_, err = b.call(ctx, wlanSvc(index), "SetSSID", tr064.Args{"NewSSID": ssid})
	return err
}

// ChannelInfo lists the channels a WLAN can use.
type ChannelInfo struct {
	Channel  int   `json:"channel"`
	Auto     bool  `json:"auto"`
	Possible []int `json:"possible"`
}

// Channels reads the current and possible channels.
func (b *Box) Channels(ctx context.Context, index int) (ChannelInfo, error) {
	r, err := b.call(ctx, wlanSvc(index), "GetChannelInfo", nil)
	if err != nil {
		return ChannelInfo{}, err
	}
	ci := ChannelInfo{Channel: atoi(r["NewChannel"]), Auto: bool01(r["NewX_AVM-DE_AutoChannelEnabled"])}
	for f := range strings.SplitSeq(r["NewPossibleChannels"], ",") {
		if c := atoi(f); c > 0 {
			ci.Possible = append(ci.Possible, c)
		}
	}
	return ci, nil
}

// SetChannel sets a fixed channel; 0 selects automatic channel choice.
func (b *Box) SetChannel(ctx context.Context, index, channel int) error {
	if channel != 0 {
		ci, err := b.Channels(ctx, index)
		if err == nil && len(ci.Possible) > 0 && !containsInt(ci.Possible, channel) {
			return fmt.Errorf("channel %d not possible here; choose from %v or auto", channel, ci.Possible)
		}
	}
	_, err := b.call(ctx, wlanSvc(index), "SetChannel", tr064.Args{"NewChannel": itoa(channel)})
	return err
}

// SetHidden hides or shows the SSID (beacon advertisement).
func (b *Box) SetHidden(ctx context.Context, index int, hidden bool) error {
	_, err := b.call(ctx, wlanSvc(index), "SetBeaconAdvertisement", tr064.Args{"NewBeaconAdvertisementEnabled": b01(!hidden)})
	return err
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
