package fritz

import (
	"context"
	"encoding/xml"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/mkrajinov/fritzbox/internal/tr064"
)

// WLANClient is a device associated with one of the box's own radios. Devices on
// mesh repeaters are not in this list; the box only reports its own associations.
type WLANClient struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip,omitempty"`
	Name     string `json:"name,omitempty"` // from the host list
	WLAN     int    `json:"wlan"`           // WLANConfiguration index
	Band     string `json:"band,omitempty"`
	SSID     string `json:"ssid,omitempty"`
	Guest    bool   `json:"guest"`
	Signal   int    `json:"signal"`     // X_AVM-DE_SignalStrength, 0–100 (AVM: percent; unverified)
	Speed    int    `json:"speed_mbit"` // current TX rate, Mbit/s
	SpeedRX  int    `json:"speed_rx_mbit,omitempty"`
	SpeedMax int    `json:"speed_max_mbit,omitempty"`
	Auth     bool   `json:"authenticated"`
}

// SignalLabel turns the signal value into words, using the same thresholds as bars.
func SignalLabel(s int) string {
	switch {
	case s >= 70:
		return "excellent"
	case s >= 50:
		return "good"
	case s >= 30:
		return "fair"
	}
	return "weak"
}

// ParseWLANDeviceList parses the XML behind X_AVM-DE_GetWLANDeviceListPath. The element
// names are matched loosely (by suffix) because AVM documents them only in prose.
func ParseWLANDeviceList(data []byte) ([]WLANClient, error) {
	var l struct {
		Items []struct {
			Fields []struct {
				XMLName xml.Name
				Value   string `xml:",chardata"`
			} `xml:",any"`
		} `xml:"Item"`
	}
	if err := xml.Unmarshal(data, &l); err != nil {
		return nil, err
	}
	out := make([]WLANClient, 0, len(l.Items))
	for _, it := range l.Items {
		f := map[string]string{}
		for _, x := range it.Fields {
			f[x.XMLName.Local] = strings.TrimSpace(x.Value)
		}
		c := clientFromFields(func(name string) string {
			for k, v := range f {
				if k == name || strings.HasSuffix(k, "_"+name) || strings.HasSuffix(k, "-"+name) || strings.HasSuffix(k, "Device"+name) {
					return v
				}
			}
			return ""
		})
		if c.MAC != "" {
			out = append(out, c)
		}
	}
	return out, nil
}

// clientFromFields maps field names (without the New/AssociatedDevice/X_AVM-DE_ prefixes) to a client.
func clientFromFields(get func(string) string) WLANClient {
	auth := get("AuthState")
	return WLANClient{
		MAC: strings.ToUpper(get("MACAddress")), IP: get("IPAddress"),
		Signal: atoi(get("SignalStrength")), Speed: atoi(get("Speed")), SpeedRX: atoi(get("SpeedRX")),
		SpeedMax: atoi(get("SpeedMax")), Auth: auth == "" || bool01(auth),
	}
}

// WLANClients lists the devices on the box's own WLAN radios with signal and link rate,
// sorted by WLAN, then strongest signal first. Names come from the host list.
func (b *Box) WLANClients(ctx context.Context) ([]WLANClient, error) {
	var names map[string]string
	var wg sync.WaitGroup
	wg.Go(func() {
		if hosts, err := b.Hosts(ctx); err == nil {
			names = make(map[string]string, 2*len(hosts))
			for _, h := range hosts {
				names[strings.ToUpper(h.MAC)] = h.Name
				if h.IP != "" {
					names["ip:"+h.IP] = h.Name
				}
			}
		}
	})
	out, err := b.WLANAssociations(ctx)
	wg.Wait()
	for i := range out {
		c := &out[i]
		if c.Name = names[c.MAC]; c.Name == "" && c.IP != "" {
			c.Name = names["ip:"+c.IP]
		}
	}
	return out, err
}

// WLANAssociations is WLANClients without the names (for callers that have the host list).
// It needs one GetInfo per WLAN plus the device lists, so it is cheap enough to poll.
func (b *Box) WLANAssociations(ctx context.Context) ([]WLANClient, error) {
	idx, err := b.wlanIndexes(ctx)
	if err != nil {
		return nil, err
	}
	lists := make([][]WLANClient, len(idx))
	errs := make([]error, len(idx))
	var wg sync.WaitGroup
	for n, i := range idx {
		wg.Go(func() {
			r, err := b.call(ctx, "WLANConfiguration"+strconv.Itoa(i), "GetInfo", nil)
			if err != nil {
				errs[n] = err
				return
			}
			if !bool01(r["NewEnable"]) {
				return
			}
			band := bandOf(r["NewX_AVM-DE_FrequencyBand"], atoi(r["NewChannel"]))
			guest := len(idx) > 1 && i == idx[len(idx)-1]
			lists[n], errs[n] = b.wlanClients(ctx, i)
			for k := range lists[n] {
				c := &lists[n][k]
				c.WLAN, c.Band, c.SSID, c.Guest = i, band, r["NewSSID"], guest
			}
		})
	}
	wg.Wait()
	var out []WLANClient
	for _, l := range lists {
		out = append(out, l...)
	}
	slices.SortStableFunc(out, func(a, b WLANClient) int {
		if a.WLAN != b.WLAN {
			return a.WLAN - b.WLAN
		}
		return b.Signal - a.Signal
	})
	if out == nil {
		if err := errors.Join(errs...); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (b *Box) wlanClients(ctx context.Context, index int) ([]WLANClient, error) {
	svc := "WLANConfiguration" + strconv.Itoa(index)
	r, err := b.call(ctx, svc, "X_AVM-DE_GetWLANDeviceListPath", nil)
	if err == nil {
		data, err := b.TR.Get(ctx, r["NewX_AVM-DE_WLANDeviceListPath"])
		if err != nil {
			return nil, err
		}
		return ParseWLANDeviceList(data)
	}
	if !tr064.IsCode(err, tr064.CodeInvalidAction) {
		return nil, err
	}
	// Older firmware: one call per associated device.
	n, err := b.call(ctx, svc, "GetTotalAssociations", nil)
	if err != nil {
		return nil, err
	}
	var out []WLANClient
	for i := range atoi(n["NewTotalAssociations"]) {
		d, err := b.call(ctx, svc, "GetGenericAssociatedDeviceInfo", tr064.Args{"NewAssociatedDeviceIndex": strconv.Itoa(i)})
		if err != nil {
			return out, err
		}
		c := clientFromFields(func(name string) string {
			for _, k := range []string{"NewAssociatedDevice" + name, "NewX_AVM-DE_" + name} {
				if v, ok := d[k]; ok {
					return v
				}
			}
			return ""
		})
		if c.MAC != "" {
			out = append(out, c)
		}
	}
	return out, nil
}

// WiFiHostsNotOnBox returns active Wi-Fi hosts that are not associated with the box's
// own radios — usually devices on a mesh repeater, for which the box reports no signal.
func WiFiHostsNotOnBox(hosts []Host, clients []WLANClient) []Host {
	on := map[string]bool{}
	for _, c := range clients {
		on[c.MAC] = true
	}
	var out []Host
	for _, h := range hosts {
		if h.Active && strings.HasPrefix(h.Interface, "802.11") && !on[strings.ToUpper(h.MAC)] {
			out = append(out, h)
		}
	}
	return out
}
