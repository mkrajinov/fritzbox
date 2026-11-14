package fritz

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/mkrajinovic/fbctl/internal/tr064"
)

const userIf = "UserInterface1"

// UpdateInfo is the firmware update state (UserInterface:1).
type UpdateInfo struct {
	Current   string `json:"current"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	State     string `json:"state"` // Started, Stopped, Error, NoUpdate, UpdateAvailable, Unknown
	InfoURL   string `json:"info_url,omitempty"`
	AutoMode  string `json:"auto_mode,omitempty"` // all, important, notify
	LastOK    string `json:"last_update,omitempty"`
}

// UpdateInfo reads the firmware update state.
func (b *Box) UpdateInfo(ctx context.Context) (UpdateInfo, error) {
	r, err := b.call(ctx, userIf, "GetInfo", nil)
	if err != nil {
		return UpdateInfo{}, err
	}
	u := UpdateInfo{
		Available: bool01(r["NewUpgradeAvailable"]), Version: r["NewX_AVM-DE_Version"],
		State: r["NewX_AVM-DE_UpdateState"], InfoURL: r["NewX_AVM-DE_InfoURL"],
	}
	if x, err := b.call(ctx, userIf, "X_AVM-DE_GetInfo", nil); err == nil {
		u.AutoMode, u.Current, u.LastOK = x["NewX_AVM-DE_AutoUpdateMode"], x["NewX_AVM-DE_CurrentFwVersion"], x["NewX_AVM-DE_UpdateSuccessful"]
	}
	return u, nil
}

// CheckUpdate asks the box to look for new firmware now.
func (b *Box) CheckUpdate(ctx context.Context) error {
	_, err := b.call(ctx, userIf, "X_AVM-DE_CheckUpdate", nil)
	return err
}

// DoUpdate starts the firmware update. The box reboots when it is done.
func (b *Box) DoUpdate(ctx context.Context) (string, error) {
	r, err := b.call(ctx, userIf, "X_AVM-DE_DoUpdate", nil)
	if err != nil {
		return "", err
	}
	return r["NewX_AVM-DE_UpdateState"], nil
}

// AutoUpdateModes are the values X_AVM-DE_SetConfig accepts.
var AutoUpdateModes = []string{"notify", "important", "all"}

// SetAutoUpdate sets the automatic update mode.
func (b *Box) SetAutoUpdate(ctx context.Context, mode string) error {
	if !contains(AutoUpdateModes, mode) {
		return fmt.Errorf("auto update mode must be one of %s", strings.Join(AutoUpdateModes, ", "))
	}
	_, err := b.call(ctx, userIf, "X_AVM-DE_SetConfig", tr064.Args{"NewX_AVM-DE_AutoUpdateMode": mode})
	return err
}

// Backup exports the box configuration, encrypted with password (needed again to restore it in
// the web UI). The download URL the box returns is valid for a short time only.
func (b *Box) Backup(ctx context.Context, password string) ([]byte, error) {
	if password == "" {
		return nil, errors.New("a backup password is required; the box encrypts secrets in the export with it")
	}
	r, err := b.call(ctx, "DeviceConfig1", "X_AVM-DE_GetConfigFile", tr064.Args{"NewX_AVM-DE_Password": password})
	if err != nil {
		return nil, err
	}
	u := r["NewX_AVM-DE_ConfigFileUrl"]
	if u == "" {
		return nil, errors.New("box returned no download URL")
	}
	data, err := b.TR.Get(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("download backup: %w", err)
	}
	if !strings.Contains(string(data[:min(len(data), 512)]), "FRITZ") {
		return nil, errors.New("download did not return a FRITZ!Box export")
	}
	return data, nil
}

// DHCP is the LAN DHCP configuration (LANHostConfigManagement:1).
type DHCP struct {
	Enabled bool   `json:"enabled"`
	Min     string `json:"min"`
	Max     string `json:"max"`
	Router  string `json:"router"`
	Subnet  string `json:"subnet_mask"`
	DNS     string `json:"dns_servers"`
	Domain  string `json:"domain"`
}

const lanHostCfg = "LANHostConfigManagement1"

// DHCP reads the DHCP server settings.
func (b *Box) DHCP(ctx context.Context) (DHCP, error) {
	r, err := b.call(ctx, lanHostCfg, "GetInfo", nil)
	if err != nil {
		return DHCP{}, err
	}
	return DHCP{
		Enabled: bool01(r["NewDHCPServerEnable"]), Min: r["NewMinAddress"], Max: r["NewMaxAddress"],
		Router: r["NewIPRouters"], Subnet: r["NewSubnetMask"], DNS: r["NewDNSServers"], Domain: r["NewDomainName"],
	}, nil
}

// SetDHCPRange changes the DHCP address range. Both addresses must be in the box's subnet.
func (b *Box) SetDHCPRange(ctx context.Context, lo, hi string) error {
	l, h := net.ParseIP(lo).To4(), net.ParseIP(hi).To4()
	if l == nil || h == nil {
		return errors.New("DHCP range needs two IPv4 addresses")
	}
	if !ipLess(lo, hi) {
		return errors.New("the first address must be lower than the second")
	}
	cur, err := b.DHCP(ctx)
	if err == nil && cur.Router != "" && cur.Subnet != "" {
		mask := net.IPMask(net.ParseIP(cur.Subnet).To4())
		subnet := net.IPNet{IP: net.ParseIP(cur.Router).Mask(mask), Mask: mask}
		if !subnet.Contains(l) || !subnet.Contains(h) {
			return fmt.Errorf("range must be inside %s", subnet.String())
		}
		if r := net.ParseIP(cur.Router); r != nil && !ipLess(cur.Router, lo) && !ipLess(hi, cur.Router) {
			return fmt.Errorf("range must not include the box itself (%s)", cur.Router)
		}
	}
	_, err = b.call(ctx, lanHostCfg, "SetAddressRange", tr064.Args{"NewMinAddress": lo, "NewMaxAddress": hi})
	return err
}

// Remote access, DynDNS, MyFRITZ.

// Remote summarises internet-facing access settings.
type Remote struct {
	HTTPSEnabled bool   `json:"https_enabled"`
	HTTPSPort    string `json:"https_port,omitempty"`
	User         string `json:"user,omitempty"`
	LetsEncrypt  bool   `json:"letsencrypt"`
	LEState      string `json:"letsencrypt_state,omitempty"`

	DDNSEnabled  bool   `json:"ddns_enabled"`
	DDNSProvider string `json:"ddns_provider,omitempty"`
	DDNSDomain   string `json:"ddns_domain,omitempty"`
	DDNSStatus4  string `json:"ddns_status_ipv4,omitempty"`
	DDNSStatus6  string `json:"ddns_status_ipv6,omitempty"`

	MyFritzEnabled    bool   `json:"myfritz_enabled"`
	MyFritzRegistered bool   `json:"myfritz_registered"`
	MyFritzName       string `json:"myfritz_name,omitempty"`
	MyFritzState      string `json:"myfritz_state,omitempty"`
	MyFritzEmail      string `json:"myfritz_email,omitempty"`
}

const (
	remoteSvc  = "X_AVM-DE_RemoteAccess1"
	myFritzSvc = "X_AVM-DE_MyFritz1"
)

// Remote reads remote access, DynDNS and MyFRITZ state. Parts the box doesn't offer stay empty.
func (b *Box) Remote(ctx context.Context) (Remote, error) {
	var out Remote
	r, err := b.call(ctx, remoteSvc, "GetInfo", nil)
	if err != nil {
		return out, err
	}
	out.HTTPSEnabled, out.HTTPSPort, out.User = bool01(r["NewEnabled"]), r["NewPort"], r["NewUsername"]
	out.LetsEncrypt, out.LEState = bool01(r["NewLetsEncryptEnabled"]), r["NewLetsEncryptState"]
	if d, err := b.call(ctx, remoteSvc, "GetDDNSInfo", nil); err == nil {
		out.DDNSEnabled, out.DDNSProvider, out.DDNSDomain = bool01(d["NewEnabled"]), d["NewProviderName"], d["NewDomain"]
		out.DDNSStatus4, out.DDNSStatus6 = d["NewStatusIPv4"], d["NewStatusIPv6"]
	}
	if m, err := b.call(ctx, myFritzSvc, "GetInfo", nil); err == nil {
		out.MyFritzEnabled, out.MyFritzRegistered = bool01(m["NewEnabled"]), bool01(m["NewDeviceRegistered"])
		out.MyFritzName, out.MyFritzState, out.MyFritzEmail = m["NewDynDNSName"], m["NewState"], m["NewEmail"]
	}
	return out, nil
}

// SetRemoteHTTPS turns internet access to the web UI over HTTPS on or off and returns the port.
func (b *Box) SetRemoteHTTPS(ctx context.Context, on bool) (string, error) {
	r, err := b.call(ctx, remoteSvc, "SetEnable", tr064.Args{"NewEnabled": b01(on)})
	if err != nil {
		return "", err
	}
	return r["NewPort"], nil
}

// SetLetsEncrypt turns the Let's Encrypt certificate on or off.
func (b *Box) SetLetsEncrypt(ctx context.Context, on bool) error {
	_, err := b.call(ctx, remoteSvc, "SetLetsEncryptEnable", tr064.Args{"NewLetsEncryptEnabled": b01(on)})
	return err
}

// SetMyFritz turns MyFRITZ on or off, keeping the registered email.
func (b *Box) SetMyFritz(ctx context.Context, on bool) error {
	m, err := b.call(ctx, myFritzSvc, "GetInfo", nil)
	if err != nil {
		return err
	}
	_, err = b.call(ctx, myFritzSvc, "SetMyFRITZ", tr064.Args{"NewEnabled": b01(on), "NewEmail": m["NewEmail"]})
	return err
}

func b01(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// itoa is shorthand used by the TR-064 wrappers.
func itoa(i int) string { return strconv.Itoa(i) }
