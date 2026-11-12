// Package fritz is the domain layer shared by the CLI and the TUI. It hides
// TR-064 service/action names and the AHA HTTP interface behind typed methods.
package fritz

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mkrajinovic/fbctl/internal/aha"
	"github.com/mkrajinovic/fbctl/internal/config"
	"github.com/mkrajinovic/fbctl/internal/tr064"
)

// Box is a connected FRITZ!Box.
type Box struct {
	Name string
	Host string
	User string

	TR  *tr064.Client
	aha *aha.Client
	pw  string
	r   *config.Router

	ahaOnce sync.Once

	connMu    sync.Mutex
	connKnown bool
	connSvc   string
}

// ConnectOptions tunes Connect.
type ConnectOptions struct {
	Timeout time.Duration
	Debug   io.Writer
}

// Connect builds a Box from a router profile. It does not touch the network
// unless the profile has no user (then the box's last-used user is looked up, as AVM recommends).
func Connect(ctx context.Context, name string, r *config.Router, password string, o ConnectOptions) (*Box, error) {
	user := r.User
	if user == "" && password != "" {
		u, err := lastUser(ctx, r, password, o)
		if err != nil {
			return nil, fmt.Errorf("no user configured and lookup failed: %w", err)
		}
		user = u
	}
	c, err := tr064.New(tr064.Options{
		Host: r.Host, Port: r.Port, TLS: r.TLS, Fingerprint: r.Fingerprint,
		User: user, Password: password, Timeout: o.Timeout, Debug: o.Debug,
	})
	if err != nil {
		return nil, err
	}
	return &Box{Name: name, Host: r.Host, User: user, TR: c, pw: password, r: r}, nil
}

// lastUser asks LANConfigSecurity1 for the last logged-in user. AVM allows this call without
// credentials; if a box refuses, retry with the legacy "dslf-config" user like fritzconnection does.
func lastUser(ctx context.Context, r *config.Router, pw string, o ConnectOptions) (string, error) {
	var res tr064.Result
	for _, user := range []string{"", "dslf-config"} {
		opts := tr064.Options{Host: r.Host, Port: r.Port, TLS: r.TLS, Fingerprint: r.Fingerprint, Timeout: o.Timeout, Debug: o.Debug}
		if user != "" {
			opts.User, opts.Password = user, pw
		}
		c, err := tr064.New(opts)
		if err != nil {
			return "", err
		}
		res, err = c.Call(ctx, "LANConfigSecurity1", "X_AVM-DE_GetUserList", nil)
		if err == nil {
			break
		}
		if !errors.Is(err, tr064.ErrUnauthorized) || user != "" {
			return "", err
		}
	}
	var l struct {
		Users []struct {
			Name string `xml:",chardata"`
			Last string `xml:"last_user,attr"`
		} `xml:"Username"`
	}
	if err := xml.Unmarshal([]byte(res["NewX_AVM-DE_UserList"]), &l); err != nil {
		return "", err
	}
	for _, u := range l.Users {
		if u.Last == "1" {
			return u.Name, nil
		}
	}
	if len(l.Users) > 0 {
		return l.Users[0].Name, nil
	}
	return "", fmt.Errorf("box returned no users; pass --user")
}

func (b *Box) ahaClient() *aha.Client {
	b.ahaOnce.Do(func() {
		scheme, host := "http", b.Host
		tr := &http.Transport{Proxy: nil}
		if b.r.TLS {
			scheme = "https"
			tr.TLSClientConfig = tr064.PinnedTLSConfig(b.r.Fingerprint)
		}
		if b.r.WebPort != 0 {
			host = net.JoinHostPort(b.Host, strconv.Itoa(b.r.WebPort))
		}
		base := scheme + "://" + host
		b.aha = aha.New(base, b.User, b.pw, &http.Client{Transport: tr, Timeout: 15 * time.Second})
	})
	return b.aha
}

func (b *Box) call(ctx context.Context, svc, action string, args tr064.Args) (tr064.Result, error) {
	return b.TR.Call(ctx, svc, action, args)
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func bool01(s string) bool { return strings.TrimSpace(s) == "1" }
