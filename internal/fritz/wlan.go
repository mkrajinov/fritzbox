package fritz

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/mkrajinov/fritzbox/internal/tr064"
)

// WLAN is one WLANConfiguration service.
type WLAN struct {
	Index    int    `json:"index"` // WLANConfiguration<Index>
	SSID     string `json:"ssid"`
	Enabled  bool   `json:"enabled"`
	Status   string `json:"status"`
	Channel  int    `json:"channel"`
	Band     string `json:"band"` // "2.4", "5", "6" GHz
	Standard string `json:"standard"`
	Beacon   string `json:"beacon_type"`
	Hidden   bool   `json:"hidden"`
	Guest    bool   `json:"guest"`
	Clients  int    `json:"clients"`
}

// Label is a short human name, e.g. "2.4 GHz" or "Guest".
func (w WLAN) Label() string {
	if w.Guest {
		return "Guest"
	}
	if w.Band != "" {
		return w.Band + " GHz"
	}
	return "WLAN " + strconv.Itoa(w.Index)
}

func (b *Box) wlanIndexes(ctx context.Context) ([]int, error) {
	d, err := b.TR.Description(ctx)
	if err != nil {
		return nil, err
	}
	var idx []int
	for i := 1; ; i++ {
		if _, ok := d.Services["WLANConfiguration"+strconv.Itoa(i)]; !ok {
			break
		}
		idx = append(idx, i)
	}
	return idx, nil
}

// WLANs lists all WLANs. As in fritzconnection, the highest-numbered service is the guest
// network when the box has more than one WLAN.
func (b *Box) WLANs(ctx context.Context) ([]WLAN, error) {
	idx, err := b.wlanIndexes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]WLAN, len(idx))
	errs := make([]error, len(idx))
	var wg sync.WaitGroup
	for n, i := range idx {
		wg.Go(func() {
			out[n], errs[n] = b.wlan(ctx, i, len(idx) > 1 && i == idx[len(idx)-1])
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

func (b *Box) wlan(ctx context.Context, i int, guest bool) (WLAN, error) {
	svc := "WLANConfiguration" + strconv.Itoa(i)
	r, err := b.call(ctx, svc, "GetInfo", nil)
	if err != nil {
		return WLAN{}, err
	}
	w := WLAN{
		Index: i, SSID: r["NewSSID"], Enabled: bool01(r["NewEnable"]), Status: r["NewStatus"],
		Channel: atoi(r["NewChannel"]), Standard: r["NewStandard"], Beacon: r["NewBeaconType"], Guest: guest,
	}
	w.Band = bandOf(r["NewX_AVM-DE_FrequencyBand"], w.Channel)
	if a, err := b.call(ctx, svc, "GetTotalAssociations", nil); err == nil {
		w.Clients = atoi(a["NewTotalAssociations"])
	}
	if bc, err := b.call(ctx, svc, "GetBeaconAdvertisement", nil); err == nil {
		w.Hidden = !bool01(bc["NewBeaconAdvertisementEnabled"])
	}
	return w, nil
}

// bandOf prefers the AVM band field ("2400", "5000", "6000") and falls back to the channel number.
func bandOf(field string, channel int) string {
	switch {
	case strings.HasPrefix(field, "24"):
		return "2.4"
	case strings.HasPrefix(field, "5"):
		return "5"
	case strings.HasPrefix(field, "6"):
		return "6"
	case channel > 0 && channel <= 14:
		return "2.4"
	case channel >= 32 && channel <= 177:
		return "5"
	}
	return ""
}

// ResolveWLAN maps "2.4", "5", "6", "guest" or an index to a WLAN.
func ResolveWLAN(ws []WLAN, sel string) (WLAN, error) {
	sel = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(sel)), "ghz"))
	if sel == "guest" {
		for _, w := range ws {
			if w.Guest {
				return w, nil
			}
		}
		return WLAN{}, fmt.Errorf("no guest WLAN found")
	}
	if sel == "2" || sel == "24" {
		sel = "2.4"
	}
	for _, w := range ws {
		if !w.Guest && w.Band == sel {
			return w, nil
		}
	}
	if n, err := strconv.Atoi(sel); err == nil {
		for _, w := range ws {
			if w.Index == n {
				return w, nil
			}
		}
	}
	return WLAN{}, fmt.Errorf("no WLAN matches %q (use 2.4|5|6|guest or an index)", sel)
}

// SetWLAN enables or disables a WLAN.
func (b *Box) SetWLAN(ctx context.Context, index int, enable bool) error {
	v := "0"
	if enable {
		v = "1"
	}
	_, err := b.call(ctx, "WLANConfiguration"+strconv.Itoa(index), "SetEnable", tr064.Args{"NewEnable": v})
	return err
}

// WLANPassword returns the passphrase of a WLAN.
func (b *Box) WLANPassword(ctx context.Context, index int) (string, error) {
	r, err := b.call(ctx, "WLANConfiguration"+strconv.Itoa(index), "GetSecurityKeys", nil)
	if err != nil {
		return "", err
	}
	return r["NewKeyPassphrase"], nil
}

// WiFiQRPayload builds the WIFI: URI understood by phone cameras.
func WiFiQRPayload(w WLAN, password string) string {
	sec := "WPA"
	switch w.Beacon {
	case "None", "OWE", "OWETrans":
		sec, password = "nopass", ""
	case "Basic":
		sec = "WEP"
	}
	esc := strings.NewReplacer(`\`, `\\`, `;`, `\;`, `,`, `\,`, `:`, `\:`, `"`, `\"`)
	s := fmt.Sprintf("WIFI:T:%s;S:%s;", sec, esc.Replace(w.SSID))
	if password != "" {
		s += "P:" + esc.Replace(password) + ";"
	}
	if w.Hidden {
		s += "H:true;"
	}
	return s + ";"
}
