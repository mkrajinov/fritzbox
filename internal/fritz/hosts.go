package fritz

import (
	"context"
	"encoding/xml"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/mkrajinovic/fbctl/internal/tr064"
)

// Host is a device known to the box.
type Host struct {
	Name      string `json:"name"`
	IP        string `json:"ip"`
	MAC       string `json:"mac"`
	Active    bool   `json:"active"`
	Interface string `json:"interface"` // Ethernet, 802.11, ...
	Port      string `json:"port,omitempty"`
	SpeedMbit int    `json:"speed_mbit,omitempty"`
	Guest     bool   `json:"guest"`
}

type hostList struct {
	Items []struct {
		IP        string `xml:"IPAddress"`
		MAC       string `xml:"MACAddress"`
		Active    string `xml:"Active"`
		Name      string `xml:"HostName"`
		Interface string `xml:"InterfaceType"`
		Port      string `xml:"X_AVM-DE_Port"`
		Speed     string `xml:"X_AVM-DE_Speed"`
		Guest     string `xml:"X_AVM-DE_Guest"`
	} `xml:"Item"`
}

// ParseHostList parses the XML behind X_AVM-DE_GetHostListPath.
func ParseHostList(data []byte) ([]Host, error) {
	var l hostList
	if err := xml.Unmarshal(data, &l); err != nil {
		return nil, err
	}
	out := make([]Host, 0, len(l.Items))
	for _, it := range l.Items {
		out = append(out, Host{
			Name: it.Name, IP: it.IP, MAC: it.MAC, Active: bool01(it.Active),
			Interface: it.Interface, Port: it.Port, SpeedMbit: atoi(it.Speed), Guest: bool01(it.Guest),
		})
	}
	return out, nil
}

// Hosts lists known hosts with one list download instead of one SOAP call per host.
// Falls back to GetGenericHostEntry on firmware without the list path.
func (b *Box) Hosts(ctx context.Context) ([]Host, error) {
	r, err := b.call(ctx, "Hosts1", "X_AVM-DE_GetHostListPath", nil)
	if err == nil {
		data, err := b.TR.Get(ctx, r["NewX_AVM-DE_HostListPath"])
		if err != nil {
			return nil, err
		}
		return ParseHostList(data)
	}
	if !tr064.IsCode(err, tr064.CodeInvalidAction) {
		return nil, err
	}
	n, err := b.call(ctx, "Hosts1", "GetHostNumberOfEntries", nil)
	if err != nil {
		return nil, err
	}
	count := atoi(n["NewHostNumberOfEntries"])
	out := make([]Host, 0, count)
	for i := range count {
		h, err := b.call(ctx, "Hosts1", "GetGenericHostEntry", tr064.Args{"NewIndex": fmt.Sprint(i)})
		if err != nil {
			return nil, err
		}
		out = append(out, Host{
			Name: h["NewHostName"], IP: h["NewIPAddress"], MAC: h["NewMACAddress"],
			Active: bool01(h["NewActive"]), Interface: h["NewInterfaceType"],
		})
	}
	return out, nil
}

// SortHosts sorts by "ip" (numeric) or "name".
func SortHosts(h []Host, by string) {
	sort.SliceStable(h, func(i, j int) bool {
		if by == "name" {
			return strings.ToLower(h[i].Name) < strings.ToLower(h[j].Name)
		}
		return ipLess(h[i].IP, h[j].IP)
	})
}

func ipLess(a, b string) bool {
	ia, ib := net.ParseIP(a), net.ParseIP(b)
	switch {
	case ia == nil && ib == nil:
		return a < b
	case ia == nil:
		return false
	case ib == nil:
		return true
	}
	return string(ia.To16()) < string(ib.To16())
}

// FindHost matches a MAC, IP or (case-insensitive) host name.
func FindHost(hosts []Host, q string) (Host, error) {
	var matches []Host
	for _, h := range hosts {
		if strings.EqualFold(h.MAC, q) || h.IP == q || strings.EqualFold(h.Name, q) {
			matches = append(matches, h)
		}
	}
	switch len(matches) {
	case 0:
		return Host{}, fmt.Errorf("no host matches %q", q)
	case 1:
		return matches[0], nil
	}
	return Host{}, fmt.Errorf("%q matches %d hosts; use the MAC address", q, len(matches))
}

// WakeOnLAN sends a magic packet via the box to the given MAC.
func (b *Box) WakeOnLAN(ctx context.Context, mac string) error {
	_, err := b.call(ctx, "Hosts1", "X_AVM-DE_WakeOnLANByMACAddress", tr064.Args{"NewMACAddress": mac})
	return err
}

// Internet access per host (X_AVM-DE_HostFilter, documented by AVM; requires "App rights").

// WANAccess reports whether a host can reach the internet. access is "granted", "denied" or
// "error"; disallowed is the explicit block flag (a filter profile can deny access without it).
func (b *Box) WANAccess(ctx context.Context, ip string) (access string, disallowed bool, err error) {
	r, err := b.call(ctx, "X_AVM-DE_HostFilter1", "GetWANAccessByIP", tr064.Args{"NewIPv4Address": ip})
	if err != nil {
		return "", false, err
	}
	return r["NewWANAccess"], bool01(r["NewDisallow"]), nil
}

// SetInternetBlocked blocks or unblocks internet access for a host. The box applies it
// asynchronously. FRITZ! devices and PLC adapters cannot be blocked (UPnP error 880).
func (b *Box) SetInternetBlocked(ctx context.Context, ip string, blocked bool) error {
	if ip == "" {
		return fmt.Errorf("host has no IPv4 address")
	}
	v := "0"
	if blocked {
		v = "1"
	}
	_, err := b.call(ctx, "X_AVM-DE_HostFilter1", "DisallowWANAccessByIP", tr064.Args{"NewIPv4Address": ip, "NewDisallow": v})
	return err
}
