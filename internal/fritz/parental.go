package fritz

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"strings"

	"github.com/mkrajinovic/fbctl/internal/tr064"
)

const hostFilter = "X_AVM-DE_HostFilter1"

// HostInfo is everything the box knows about one host (Hosts:1 X_AVM-DE_GetSpecificHostEntryByIP
// plus X_AVM-DE_HostFilter:1 GetHostEntryByIP for online-time limits).
type HostInfo struct {
	IP          string `json:"ip"`
	MAC         string `json:"mac"`
	Name        string `json:"name"`
	Active      bool   `json:"active"`
	Model       string `json:"model,omitempty"`
	DeviceClass string `json:"device_class,omitempty"`
	Priority    bool   `json:"priority"`
	AutoWOL     bool   `json:"auto_wol"`
	WANAccess   string `json:"wan_access"` // granted, denied, error, unknown
	Disallowed  bool   `json:"blocked"`
	ProfileID   string `json:"profile_id,omitempty"`
	TimeUsed    int    `json:"time_used_min"` // minutes today, as reported by the box
	TimeMax     int    `json:"time_max_min"`
	TicketAvail bool   `json:"ticket_available"`
	Guest       bool   `json:"guest"`
	VPN         bool   `json:"vpn"`
}

// HostInfo collects the details for a host by IP.
func (b *Box) HostInfo(ctx context.Context, ip string) (HostInfo, error) {
	r, err := b.call(ctx, "Hosts1", "X_AVM-DE_GetSpecificHostEntryByIP", tr064.Args{"NewIPAddress": ip})
	if err != nil {
		return HostInfo{IP: ip}, err
	}
	h := HostInfo{
		IP: ip, MAC: r["NewMACAddress"], Name: r["NewHostName"], Active: bool01(r["NewActive"]),
		Model: r["NewX_AVM-DE_Model"], DeviceClass: firstNonEmpty(r["NewX_AVM-DE_DeviceClassUser"], r["NewX_AVM-DE_DeviceClass"]),
		Priority: bool01(r["NewX_AVM-DE_Priority"]), WANAccess: r["NewX_AVM-DE_WANAccess"], Disallowed: bool01(r["NewX_AVM-DE_Disallow"]),
		ProfileID: r["NewX_AVM-DE_FilterProfileID"], TicketAvail: bool01(r["NewX_AVM-DE_AddTicketAvailable"]),
		Guest: bool01(r["NewX_AVM-DE_Guest"]), VPN: bool01(r["NewX_AVM-DE_VPN"]),
	}
	if h.MAC != "" {
		if w, err := b.call(ctx, "Hosts1", "X_AVM-DE_GetAutoWakeOnLANByMACAddress", tr064.Args{"NewMACAddress": h.MAC}); err == nil {
			h.AutoWOL = bool01(w["NewAutoWOLEnabled"])
		}
	}
	if f, err := b.call(ctx, hostFilter, "GetHostEntryByIP", tr064.Args{"NewIPv4Address": ip}); err == nil {
		h.TimeUsed, h.TimeMax = atoi(f["NewTimeUsed"]), atoi(f["NewTimeMax"])
		if p := f["NewFilterProfileID"]; p != "" {
			h.ProfileID = p
		}
		if a := f["NewWANAccess"]; a != "" {
			h.WANAccess = a
		}
	}
	return h, nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// SetPriority marks a host as prioritised (gets bandwidth first).
func (b *Box) SetPriority(ctx context.Context, ip string, on bool) error {
	_, err := b.call(ctx, "Hosts1", "X_AVM-DE_SetPrioritizationByIP", tr064.Args{"NewIPAddress": ip, "NewX_AVM-DE_Priority": b01(on)})
	return err
}

// SetAutoWOL makes the box wake the host when it is accessed from the internet.
func (b *Box) SetAutoWOL(ctx context.Context, mac string, on bool) error {
	_, err := b.call(ctx, "Hosts1", "X_AVM-DE_SetAutoWakeOnLANByMACAddress", tr064.Args{"NewMACAddress": mac, "NewAutoWOLEnabled": b01(on)})
	return err
}

// FilterProfile is a parental-control access profile.
type FilterProfile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// FilterProfiles lists the access profiles.
func (b *Box) FilterProfiles(ctx context.Context) ([]FilterProfile, error) {
	r, err := b.call(ctx, hostFilter, "GetFilterProfiles", nil)
	if err != nil {
		return nil, err
	}
	return ParseFilterProfiles(r["NewFilterProfileList"])
}

// ParseFilterProfiles reads the profile list. AVM documents it as an XML list; the element names
// are matched loosely (any element with an id and a name child, or id/name attributes) so small
// format differences between firmware versions don't break it.
func ParseFilterProfiles(s string) ([]FilterProfile, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if !strings.HasPrefix(s, "<") {
		// Fallback: "id:name,id:name" or "id,id".
		var out []FilterProfile
		for f := range strings.SplitSeq(s, ",") {
			id, name, _ := strings.Cut(strings.TrimSpace(f), ":")
			if id != "" {
				out = append(out, FilterProfile{ID: id, Name: firstNonEmpty(name, id)})
			}
		}
		return out, nil
	}
	dec := xml.NewDecoder(bytes.NewReader([]byte(s)))
	type frame struct {
		p    FilterProfile
		leaf string
	}
	var stack []frame
	var out []FilterProfile
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			f := frame{}
			for _, a := range t.Attr {
				switch strings.ToLower(a.Name.Local) {
				case "id", "profileid", "filterprofileid":
					f.p.ID = a.Value
				case "name", "profilename":
					f.p.Name = a.Value
				}
			}
			f.leaf = strings.ToLower(t.Name.Local)
			stack = append(stack, f)
			text.Reset()
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			v := strings.TrimSpace(text.String())
			text.Reset()
			if len(stack) > 0 && v != "" {
				parent := &stack[len(stack)-1].p
				switch cur.leaf {
				case "id", "profileid", "filterprofileid":
					parent.ID = v
				case "name", "profilename":
					parent.Name = v
				}
			}
			if cur.p.ID != "" {
				if cur.p.Name == "" {
					cur.p.Name = cur.p.ID
				}
				out = append(out, cur.p)
			}
		}
	}
	return out, nil
}

// SetFilterProfile assigns a host to an access profile.
func (b *Box) SetFilterProfile(ctx context.Context, ip, profileID string) error {
	_, err := b.call(ctx, hostFilter, "AddHostEntryToFilterProfile", tr064.Args{"NewIPv4Address": ip, "NewFilterProfileID": profileID})
	return err
}

// AddTicket gives a host one more online-time ticket and returns the new used/max minutes.
func (b *Box) AddTicket(ctx context.Context, ip string) (used, maxMin int, err error) {
	r, err := b.call(ctx, hostFilter, "AddTicketTimeToHostEntryByIP", tr064.Args{"NewIPv4Address": ip})
	if err != nil {
		return 0, 0, err
	}
	return atoi(r["NewTimeUsed"]), atoi(r["NewTimeMax"]), nil
}
