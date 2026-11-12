package tr064

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/icholy/digest"
)

const (
	DefaultPort    = 49000
	DefaultTLSPort = 49443
	DescPath       = "/tr64desc.xml"
)

// Options configures a Client.
type Options struct {
	Host        string // hostname or IP, e.g. fritz.box
	Port        int    // 0 = default for the scheme
	TLS         bool
	Fingerprint string // hex SHA-256 of the box's leaf certificate (required with TLS)
	User        string
	Password    string
	Timeout     time.Duration
	Debug       io.Writer // when set, requests/responses are logged with secrets redacted
}

// Client talks TR-064 to one box. It is safe for concurrent use.
type Client struct {
	opts Options
	base string
	http *http.Client

	mu   sync.Mutex
	desc *Description
}

// New creates a client. The connection is lazy: nothing is sent until the first call.
func New(o Options) (*Client, error) {
	if o.Host == "" {
		return nil, errors.New("tr064: host is required")
	}
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	scheme, port := "http", o.Port
	if o.TLS {
		scheme = "https"
		if port == 0 {
			port = DefaultTLSPort
		}
	} else if port == 0 {
		port = DefaultPort
	}
	base := &http.Transport{
		Proxy:               nil, // the box is on the LAN; never go through a proxy
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     60 * time.Second,
	}
	if o.TLS {
		if o.Fingerprint == "" {
			return nil, errors.New("tr064: TLS enabled but no certificate fingerprint pinned; run `fb router trust`")
		}
		base.TLSClientConfig = PinnedTLSConfig(o.Fingerprint)
	}
	var rt http.RoundTripper = base
	if o.Password != "" {
		// digest.Transport caches the challenge, so only the first call pays the extra 401 round trip.
		rt = &digest.Transport{Username: o.User, Password: o.Password, Transport: base}
	}
	return &Client{
		opts: o,
		base: fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(o.Host, fmt.Sprint(port))),
		http: &http.Client{Transport: rt, Timeout: o.Timeout},
	}, nil
}

// BaseURL returns scheme://host:port.
func (c *Client) BaseURL() string { return c.base }

// PinnedTLSConfig accepts exactly the certificate with the given SHA-256 fingerprint.
// The FRITZ!Box uses a self-signed certificate, so chain validation is replaced by pinning.
func PinnedTLSConfig(fingerprint string) *tls.Config {
	want := normalizeFP(fingerprint)
	return &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // verification is done by VerifyPeerCertificate (pinning)
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("tls: no peer certificate")
			}
			got := Fingerprint(raw[0])
			if got != want {
				return fmt.Errorf("tls: certificate fingerprint mismatch: got %s, pinned %s (re-run `fb router trust` if the box certificate changed)", got, want)
			}
			return nil
		},
	}
}

// Fingerprint returns the hex SHA-256 of a DER certificate.
func Fingerprint(der []byte) string {
	s := sha256.Sum256(der)
	return hex.EncodeToString(s[:])
}

func normalizeFP(s string) string {
	return strings.ToLower(strings.NewReplacer(":", "", " ", "").Replace(s))
}

// FetchFingerprint connects once without verification and returns the leaf certificate fingerprint,
// for trust-on-first-use. The caller must show it to the user before storing it.
func FetchFingerprint(ctx context.Context, host string, port int) (string, error) {
	if port == 0 {
		port = DefaultTLSPort
	}
	d := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // only reads the cert for TOFU
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("no certificate presented")
	}
	return Fingerprint(certs[0].Raw), nil
}

// Description fetches and caches tr64desc.xml.
func (c *Client) Description(ctx context.Context) (*Description, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.desc != nil {
		return c.desc, nil
	}
	data, err := c.Get(ctx, DescPath)
	if err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return nil, errors.New("tr64desc.xml not found: enable 'Allow access for applications' (TR-064) under Home Network > Network > Network Settings")
		}
		return nil, err
	}
	d, err := ParseDescription(data)
	if err != nil {
		return nil, fmt.Errorf("parse tr64desc.xml: %w", err)
	}
	c.desc = d
	return d, nil
}

// Service looks up a service by (normalized) name.
func (c *Client) Service(ctx context.Context, name string) (Service, error) {
	d, err := c.Description(ctx)
	if err != nil {
		return Service{}, err
	}
	s, ok := d.Services[NormalizeServiceName(name)]
	if !ok {
		return Service{}, fmt.Errorf("%w: %s", ErrUnknownService, name)
	}
	return s, nil
}

// HasService reports whether the box announces the service.
func (c *Client) HasService(ctx context.Context, name string) bool {
	_, err := c.Service(ctx, name)
	return err == nil
}

// Actions fetches the SCPD of a service.
func (c *Client) Actions(ctx context.Context, service string) ([]Action, error) {
	s, err := c.Service(ctx, service)
	if err != nil {
		return nil, err
	}
	data, err := c.Get(ctx, s.SCPDURL)
	if err != nil {
		return nil, err
	}
	return ParseSCPD(data)
}

// Args are action input arguments.
type Args map[string]string

// Result holds action output arguments keyed by name (e.g. "NewSSID").
type Result map[string]string

// Call invokes a TR-064 action.
func (c *Client) Call(ctx context.Context, service, action string, args Args) (Result, error) {
	s, err := c.Service(ctx, service)
	if err != nil {
		return nil, err
	}
	body := buildEnvelope(s.Type, action, args)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+s.ControlURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", s.Type+"#"+action)
	c.debugf("→ POST %s %s#%s\n%s\n", s.ControlURL, s.Name(), action, redact(string(body)))

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	c.debugf("← %d\n%s\n", resp.StatusCode, redact(string(data)))

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		if f := parseFault(data); f != nil {
			f.Service, f.Action = s.Name(), action
			return nil, f
		}
		return nil, &StatusError{Code: resp.StatusCode, Body: stripTags(string(data))}
	}
	return parseResponse(data, action)
}

// StatusError is a non-SOAP HTTP error.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("http %d: %s", e.Code, e.Body)
}

// Get fetches a path (relative to the base URL) or an absolute URL returned by the box,
// such as host, call or phonebook list URLs.
func (c *Client) Get(ctx context.Context, pathOrURL string) ([]byte, error) {
	u := pathOrURL
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = c.base + u
	} else if c.opts.TLS {
		// The box hands out absolute URLs; keep them on the pinned TLS endpoint.
		if pu, err := url.Parse(u); err == nil && pu.Scheme == "http" {
			pu.Scheme = "https"
			pu.Host = strings.TrimPrefix(c.base, "https://")
			u = pu.String()
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.debugf("→ GET %s\n", redactURL(u))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	c.debugf("← %d (%d bytes)\n", resp.StatusCode, len(data))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Code: resp.StatusCode, Body: stripTags(string(data))}
	}
	return data, nil
}

func (c *Client) debugf(format string, a ...any) {
	if c.opts.Debug != nil {
		fmt.Fprintf(c.opts.Debug, format, a...)
	}
}

func buildEnvelope(serviceType, action string, args Args) []byte {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<s:Envelope s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/" xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>`)
	fmt.Fprintf(&b, `<u:%s xmlns:u="%s">`, action, html.EscapeString(serviceType))
	for _, k := range keys {
		fmt.Fprintf(&b, "<%s>%s</%s>", k, html.EscapeString(args[k]), k)
	}
	fmt.Fprintf(&b, `</u:%s></s:Body></s:Envelope>`, action)
	return b.Bytes()
}

// parseResponse collects the leaf children of <u:ActionResponse>.
func parseResponse(data []byte, action string) (Result, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	res := Result{}
	depth, respDepth := 0, -1
	var cur string
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse %sResponse: %w", action, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if respDepth < 0 && t.Name.Local == action+"Response" {
				respDepth = depth
			} else if respDepth > 0 && depth == respDepth+1 {
				cur = t.Name.Local
				text.Reset()
			}
		case xml.CharData:
			if cur != "" {
				text.Write(t)
			}
		case xml.EndElement:
			if respDepth > 0 && depth == respDepth+1 && cur != "" {
				res[cur] = text.String()
				cur = ""
			}
			if depth == respDepth {
				respDepth = -2 // done
			}
			depth--
		}
	}
	if respDepth == -1 {
		return nil, fmt.Errorf("response has no %sResponse element", action)
	}
	return res, nil
}

func parseFault(data []byte) *UPnPError {
	var env struct {
		Code int    `xml:"Body>Fault>detail>UPnPError>errorCode"`
		Desc string `xml:"Body>Fault>detail>UPnPError>errorDescription"`
	}
	if err := xml.Unmarshal(data, &env); err != nil || env.Code == 0 {
		return nil
	}
	return &UPnPError{Code: env.Code, Description: env.Desc}
}

var (
	tagRe       = regexp.MustCompile(`<[^>]*>`)
	spaceRe     = regexp.MustCompile(`\s+`)
	secretTagRe = regexp.MustCompile(`(?s)<(NewKeyPassphrase|NewPreSharedKey|NewWEPKey\d|NewX_AVM-DE_Password|NewPassword)>.*?</`)
	sidRe       = regexp.MustCompile(`sid=[0-9a-fA-F]+`)
)

func stripTags(s string) string {
	return strings.TrimSpace(spaceRe.ReplaceAllString(tagRe.ReplaceAllString(s, " "), " "))
}

func redact(s string) string {
	s = secretTagRe.ReplaceAllString(s, "<$1>******</")
	return sidRe.ReplaceAllString(s, "sid=******")
}

func redactURL(u string) string { return sidRe.ReplaceAllString(u, "sid=******") }
