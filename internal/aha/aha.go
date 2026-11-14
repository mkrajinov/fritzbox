// Package aha implements the AVM AHA-HTTP-Interface (smart home) with SID login.
package aha

import (
	"context"
	"crypto/md5" //nolint:gosec // legacy FRITZ!OS login protocol mandates MD5
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"golang.org/x/crypto/pbkdf2"
)

const (
	loginPath  = "/login_sid.lua?version=2"
	switchPath = "/webservices/homeautoswitch.lua"
	emptySID   = "0000000000000000"
)

// ErrForbidden means the login failed or the user lacks the Smart Home right.
var ErrForbidden = errors.New("aha: forbidden (wrong credentials or missing 'Smart Home' permission)")

// Client is an AHA session. Safe for concurrent use.
type Client struct {
	base       string // http://fritz.box
	user, pass string
	http       *http.Client

	mu  sync.Mutex
	sid string
}

// New creates a client. base is scheme://host[:port] of the web UI (not the TR-064 port).
func New(base, user, pass string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{base: strings.TrimRight(base, "/"), user: user, pass: pass, http: hc}
}

type sessionInfo struct {
	SID       string `xml:"SID"`
	Challenge string `xml:"Challenge"`
	BlockTime int    `xml:"BlockTime"`
}

// ChallengeResponse computes the login response for a challenge.
// "2$<iter1>$<salt1>$<iter2>$<salt2>" → PBKDF2-SHA256 (FRITZ!OS ≥ 7.24), otherwise legacy MD5.
func ChallengeResponse(challenge, password string) (string, error) {
	if strings.HasPrefix(challenge, "2$") {
		p := strings.Split(challenge, "$")
		if len(p) != 5 {
			return "", fmt.Errorf("aha: malformed challenge %q", challenge)
		}
		it1, err1 := strconv.Atoi(p[1])
		salt1, err2 := hex.DecodeString(p[2])
		it2, err3 := strconv.Atoi(p[3])
		salt2, err4 := hex.DecodeString(p[4])
		if err := errors.Join(err1, err2, err3, err4); err != nil {
			return "", fmt.Errorf("aha: malformed challenge: %w", err)
		}
		static := pbkdf2.Key([]byte(password), salt1, it1, 32, sha256.New)
		dynamic := pbkdf2.Key(static, salt2, it2, 32, sha256.New)
		return p[4] + "$" + hex.EncodeToString(dynamic), nil
	}
	// Legacy: md5(UTF-16LE("<challenge>-<password>")).
	u := utf16.Encode([]rune(challenge + "-" + password))
	b := make([]byte, 0, len(u)*2)
	for _, r := range u {
		b = append(b, byte(r), byte(r>>8))
	}
	sum := md5.Sum(b) //nolint:gosec
	return challenge + "-" + hex.EncodeToString(sum[:]), nil
}

func (c *Client) login(ctx context.Context) (string, error) {
	var si sessionInfo
	if err := c.getXML(ctx, c.base+loginPath, &si); err != nil {
		return "", err
	}
	if si.BlockTime > 0 {
		return "", fmt.Errorf("aha: login blocked for %ds after failed attempts", si.BlockTime)
	}
	resp, err := ChallengeResponse(si.Challenge, c.pass)
	if err != nil {
		return "", err
	}
	form := url.Values{"username": {c.user}, "response": {resp}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+loginPath, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	var out sessionInfo
	if err := xml.NewDecoder(r.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("aha: login response: %w", err)
	}
	if out.SID == "" || out.SID == emptySID {
		return "", ErrForbidden
	}
	return out.SID, nil
}

func (c *Client) getXML(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	r, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return fmt.Errorf("aha: GET %s: http %d", strings.SplitN(u, "?", 2)[0], r.StatusCode)
	}
	return xml.NewDecoder(r.Body).Decode(v)
}

// Command runs a homeautoswitch command and returns the raw body.
// The session is reused; on 403 it logs in again once.
func (c *Client) Command(ctx context.Context, cmd, ain string, extra url.Values) ([]byte, error) {
	for attempt := range 2 {
		c.mu.Lock()
		if c.sid == "" || attempt == 1 {
			sid, err := c.login(ctx)
			if err != nil {
				c.mu.Unlock()
				return nil, err
			}
			c.sid = sid
		}
		sid := c.sid
		c.mu.Unlock()

		q := url.Values{"switchcmd": {cmd}, "sid": {sid}}
		if ain != "" {
			q.Set("ain", ain)
		}
		maps.Copy(q, extra)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+switchPath+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		r, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		r.Body.Close()
		if err != nil {
			return nil, err
		}
		switch r.StatusCode {
		case http.StatusOK:
			return body, nil
		case http.StatusForbidden:
			continue // stale SID → relogin
		default:
			return nil, fmt.Errorf("aha: %s: http %d: %s", cmd, r.StatusCode, strings.TrimSpace(string(body)))
		}
	}
	return nil, ErrForbidden
}

// Device types from getdevicelistinfos.

type Switch struct {
	State string `xml:"state" json:"state"` // "1", "0" or "" (unknown)
	Mode  string `xml:"mode" json:"mode"`   // auto|manuell
	Lock  string `xml:"lock" json:"lock"`
}

type PowerMeter struct {
	Power   int `xml:"power" json:"power_mw"`   // mW
	Energy  int `xml:"energy" json:"energy_wh"` // Wh
	Voltage int `xml:"voltage" json:"voltage_mv"`
}

type Temperature struct {
	Celsius int `xml:"celsius" json:"celsius_tenths"` // 0.1 °C
	Offset  int `xml:"offset" json:"offset_tenths"`
}

type Thermostat struct {
	Actual  int `xml:"tist" json:"tist"` // encoded, see DecodeTemp
	Target  int `xml:"tsoll" json:"tsoll"`
	Comfort int `xml:"komfort" json:"komfort"`
	Eco     int `xml:"absenk" json:"absenk"`
	Battery int `xml:"battery" json:"battery"`
	Window  int `xml:"windowopenactiv" json:"window_open"`
}

type Device struct {
	AIN         string       `xml:"identifier,attr" json:"ain"`
	ID          string       `xml:"id,attr" json:"id"`
	Product     string       `xml:"productname,attr" json:"product"`
	Firmware    string       `xml:"fwversion,attr" json:"firmware"`
	Bitmask     int          `xml:"functionbitmask,attr" json:"functionbitmask"`
	Present     int          `xml:"present" json:"present"`
	Name        string       `xml:"name" json:"name"`
	Switch      *Switch      `xml:"switch" json:"switch,omitempty"`
	PowerMeter  *PowerMeter  `xml:"powermeter" json:"powermeter,omitempty"`
	Temperature *Temperature `xml:"temperature" json:"temperature,omitempty"`
	Thermostat  *Thermostat  `xml:"hkr" json:"thermostat,omitempty"`
}

type deviceList struct {
	Devices []Device `xml:"device"`
}

// ParseDeviceList parses getdevicelistinfos output (groups are ignored).
func ParseDeviceList(data []byte) ([]Device, error) {
	var l deviceList
	if err := xml.Unmarshal(data, &l); err != nil {
		return nil, err
	}
	return l.Devices, nil
}

// Devices lists all smart home devices.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	b, err := c.Command(ctx, "getdevicelistinfos", "", nil)
	if err != nil {
		return nil, err
	}
	return ParseDeviceList(b)
}

// SetSwitch sets a plug: "on", "off" or "toggle".
func (c *Client) SetSwitch(ctx context.Context, ain, state string) error {
	cmd := map[string]string{"on": "setswitchon", "off": "setswitchoff", "toggle": "setswitchtoggle"}[state]
	if cmd == "" {
		return fmt.Errorf("invalid switch state %q (on|off|toggle)", state)
	}
	_, err := c.Command(ctx, cmd, ain, nil)
	return err
}

// SetTarget sets a thermostat target (already encoded, see EncodeTemp).
func (c *Client) SetTarget(ctx context.Context, ain string, encoded int) error {
	_, err := c.Command(ctx, "sethkrtsoll", ain, url.Values{"param": {strconv.Itoa(encoded)}})
	return err
}

// Thermostat value encoding: 16..56 = 8..28 °C in 0.5 steps, 253 = off, 254 = on (max).
const (
	TempOff = 253
	TempOn  = 254
)

// EncodeTemp converts "21.5", "off" or "on" into the AHA value.
func EncodeTemp(s string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off":
		return TempOff, nil
	case "on", "max":
		return TempOn, nil
	}
	f, err := strconv.ParseFloat(strings.Replace(strings.TrimSuffix(strings.TrimSpace(s), "°C"), ",", ".", 1), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid temperature %q", s)
	}
	if f < 8 || f > 28 {
		return 0, fmt.Errorf("temperature %.1f°C out of range 8–28°C (use off|on)", f)
	}
	v := int(f*2 + 0.5)
	return v, nil
}

// DecodeTemp renders an AHA temperature value.
func DecodeTemp(v int) string {
	switch {
	case v == TempOff:
		return "off"
	case v == TempOn:
		return "on"
	case v > 0 && v < TempOff: // tsoll is 16..56; tist (measured) may fall outside that range
		return strconv.FormatFloat(float64(v)/2, 'f', 1, 64) + "°C"
	default:
		return "–"
	}
}

// Template is a smart home template (a saved set of settings applied in one go,
// e.g. "Guest WLAN on" or "Heating: holiday").
type Template struct {
	ID      string `xml:"identifier,attr" json:"id"`
	Num     string `xml:"id,attr" json:"num"`
	Name    string `xml:"name" json:"name"`
	Devices []struct {
		AIN string `xml:"identifier,attr" json:"ain"`
	} `xml:"devices>device" json:"devices"`
}

// ParseTemplates parses gettemplatelistinfos output.
func ParseTemplates(data []byte) ([]Template, error) {
	var l struct {
		Templates []Template `xml:"template"`
	}
	if err := xml.Unmarshal(data, &l); err != nil {
		return nil, err
	}
	return l.Templates, nil
}

// Templates lists smart home templates.
func (c *Client) Templates(ctx context.Context) ([]Template, error) {
	b, err := c.Command(ctx, "gettemplatelistinfos", "", nil)
	if err != nil {
		return nil, err
	}
	return ParseTemplates(b)
}

// ApplyTemplate runs a template by its identifier.
func (c *Client) ApplyTemplate(ctx context.Context, id string) error {
	_, err := c.Command(ctx, "applytemplate", id, nil)
	return err
}
