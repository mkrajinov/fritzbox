package watch

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Sink forwards events to another system (Home Assistant, ntfy, MQTT).
type Sink interface {
	Name() string
	Send(ctx context.Context, e Event) error
}

// initialSink is implemented by sinks that want the start-up state (retained MQTT topics).
type initialSink interface{ WantsInitial() bool }

// WantsInitial reports whether the sink should get Initial events.
func WantsInitial(s Sink) bool {
	i, ok := s.(initialSink)
	return ok && i.WantsInitial()
}

const sinkTimeout = 10 * time.Second

var sinkHTTP = &http.Client{Timeout: sinkTimeout}

// stripURL removes the URL from net/http errors: webhook URLs often carry secrets
// (a Home Assistant webhook id, an ntfy topic).
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s request failed: %w", ue.Op, ue.Err)
	}
	return err
}

func post(ctx context.Context, u, contentType string, body []byte, hdr map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, sinkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return stripURL(err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "fb-watch")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := sinkHTTP.Do(req)
	if err != nil {
		return stripURL(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return nil
}

func checkHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return stripURL(err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("must be an http(s) URL")
	}
	return nil
}

// Webhook POSTs every event as JSON (for example to a Home Assistant webhook trigger).
type Webhook struct{ URL string }

func NewWebhook(u string) (*Webhook, error) {
	if err := checkHTTPURL(u); err != nil {
		return nil, fmt.Errorf("webhook URL: %w", err)
	}
	return &Webhook{URL: u}, nil
}

func (w *Webhook) Name() string { return "webhook" }

func (w *Webhook) Send(ctx context.Context, e Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return post(ctx, w.URL, "application/json", body, nil)
}

// Ntfy publishes events as push notifications to an ntfy topic URL (https://ntfy.sh/<topic>
// or a self-hosted server). Token is sent as a bearer token when set.
type Ntfy struct{ URL, Token string }

func NewNtfy(u, token string) (*Ntfy, error) {
	if err := checkHTTPURL(u); err != nil {
		return nil, fmt.Errorf("ntfy URL: %w", err)
	}
	return &Ntfy{URL: u, Token: token}, nil
}

func (n *Ntfy) Name() string { return "ntfy" }

func (n *Ntfy) Send(ctx context.Context, e Event) error {
	hdr := map[string]string{"Title": "fb · " + e.Router, "Tags": ntfyTag(e.Kind)}
	if e.Kind == KindWANDown || e.Kind == KindNewDevice {
		hdr["Priority"] = "high"
	}
	if n.Token != "" {
		hdr["Authorization"] = "Bearer " + n.Token
	}
	return post(ctx, n.URL, "text/plain; charset=utf-8", []byte(e.Message), hdr)
}

func ntfyTag(k Kind) string {
	switch k {
	case KindNewDevice:
		return "new"
	case KindWANDown:
		return "warning"
	case KindWANUp:
		return "white_check_mark"
	case KindMissedCall, KindIncomingCall:
		return "telephone_receiver"
	case KindArrived:
		return "house"
	case KindLeft:
		return "wave"
	}
	return ""
}

// MQTT publishes with a minimal MQTT 3.1.1 client (QoS 0, one short connection per event):
//
//	<prefix>/<router>/event                 every event as JSON
//	<prefix>/<router>/presence/<device>     "home" / "not_home", retained
//	<prefix>/<router>/internet              "online" / "offline", retained
//
// The presence payloads match Home Assistant's MQTT device_tracker defaults.
type MQTT struct {
	Addr     string // host:port
	TLS      bool
	User     string
	Password string
	Prefix   string
	ClientID string
}

// NewMQTT parses mqtt://[user@]host[:1883] or mqtts://[user@]host[:8883]. The password
// must come from the environment, never the URL (command lines are visible to other users).
func NewMQTT(raw, password, prefix string) (*MQTT, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("MQTT URL: %w", stripURL(err))
	}
	m := &MQTT{Prefix: strings.Trim(prefix, "/"), Password: password, ClientID: fmt.Sprintf("fb-watch-%d", time.Now().UnixNano()%1e6)}
	switch u.Scheme {
	case "mqtt", "tcp":
	case "mqtts", "ssl", "tls":
		m.TLS = true
	default:
		return nil, fmt.Errorf("MQTT URL must start with mqtt:// or mqtts://")
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("MQTT URL has no host")
	}
	if _, set := u.User.Password(); set {
		return nil, fmt.Errorf("put the MQTT password in FB_MQTT_PASSWORD, not in the URL")
	}
	m.User = u.User.Username()
	port := u.Port()
	if port == "" {
		port = map[bool]string{false: "1883", true: "8883"}[m.TLS]
	}
	m.Addr = net.JoinHostPort(u.Hostname(), port)
	if m.Prefix == "" {
		m.Prefix = "fb"
	}
	return m, nil
}

func (m *MQTT) Name() string       { return "mqtt" }
func (m *MQTT) WantsInitial() bool { return true }

// MQTTMessage is one PUBLISH.
type MQTTMessage struct {
	Topic   string
	Payload []byte
	Retain  bool
}

// Messages returns what an event publishes (exported for tests).
func (m *MQTT) Messages(e Event) []MQTTMessage {
	base := m.Prefix + "/" + TopicSafe(e.Router)
	var out []MQTTMessage
	if !e.Initial {
		body, _ := json.Marshal(e)
		out = append(out, MQTTMessage{base + "/event", body, false})
	}
	switch e.Kind {
	case KindArrived, KindLeft:
		if e.Host != nil {
			state := "not_home"
			if e.Kind == KindArrived {
				state = "home"
			}
			out = append(out, MQTTMessage{base + "/presence/" + TopicSafe(nameOr(e.Host.Name, e.Host.MAC)), []byte(state), true})
		}
	case KindWANDown:
		out = append(out, MQTTMessage{base + "/internet", []byte("offline"), true})
	case KindWANUp:
		out = append(out, MQTTMessage{base + "/internet", []byte("online"), true})
	}
	return out
}

// TopicSafe lower-cases a name and replaces everything but letters, digits, '-' and '_'.
func TopicSafe(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return '_'
	}, s)
	if s == "" {
		return "_"
	}
	return s
}

func (m *MQTT) Send(ctx context.Context, e Event) error {
	msgs := m.Messages(e)
	if len(msgs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, sinkTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", m.Addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if m.TLS {
		host, _, _ := net.SplitHostPort(m.Addr)
		tc := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = tc
	}
	if _, err := conn.Write(mqttConnect(m.ClientID, m.User, m.Password)); err != nil {
		return err
	}
	ack := make([]byte, 4)
	if _, err := io.ReadFull(conn, ack); err != nil {
		return fmt.Errorf("no CONNACK: %w", err)
	}
	if ack[0] != 0x20 || ack[1] != 2 {
		return fmt.Errorf("unexpected reply from broker")
	}
	if ack[3] != 0 {
		return fmt.Errorf("broker refused the connection (%s)", connackReason(ack[3]))
	}
	for _, msg := range msgs {
		if _, err := conn.Write(mqttPublish(msg.Topic, msg.Payload, msg.Retain)); err != nil {
			return err
		}
	}
	_, err = conn.Write([]byte{0xE0, 0}) // DISCONNECT
	return err
}

func connackReason(c byte) string {
	switch c {
	case 1:
		return "protocol version"
	case 2:
		return "client id rejected"
	case 3:
		return "server unavailable"
	case 4:
		return "bad user name or password"
	case 5:
		return "not authorised"
	}
	return fmt.Sprintf("code %d", c)
}

func mqttString(b *bytes.Buffer, s string) {
	_ = binary.Write(b, binary.BigEndian, uint16(len(s)))
	b.WriteString(s)
}

func mqttPacket(header byte, body []byte) []byte {
	out := []byte{header}
	n := len(body)
	for {
		c := byte(n % 128)
		n /= 128
		if n > 0 {
			c |= 0x80
		}
		out = append(out, c)
		if n == 0 {
			break
		}
	}
	return append(out, body...)
}

func mqttConnect(clientID, user, password string) []byte {
	var b bytes.Buffer
	mqttString(&b, "MQTT")
	b.WriteByte(4)      // protocol level 3.1.1
	flags := byte(0x02) // clean session
	if user != "" {
		flags |= 0x80
		if password != "" {
			flags |= 0x40
		}
	}
	b.WriteByte(flags)
	_ = binary.Write(&b, binary.BigEndian, uint16(30)) // keep-alive seconds
	mqttString(&b, clientID)
	if user != "" {
		mqttString(&b, user)
		if password != "" {
			mqttString(&b, password)
		}
	}
	return mqttPacket(0x10, b.Bytes())
}

func mqttPublish(topic string, payload []byte, retain bool) []byte {
	var b bytes.Buffer
	mqttString(&b, topic)
	b.Write(payload)
	h := byte(0x30) // PUBLISH, QoS 0
	if retain {
		h |= 0x01
	}
	return mqttPacket(h, b.Bytes())
}

// Dispatcher sends events to sinks from one goroutine, in order, so retained states
// never arrive out of order and a slow sink never blocks polling.
type Dispatcher struct {
	sinks  []Sink
	queue  chan Event
	report func(sink string, err error) // err == nil: the sink works (again)
	done   chan struct{}
}

// NewDispatcher starts the worker; it stops when ctx is done.
func NewDispatcher(ctx context.Context, sinks []Sink, report func(string, error)) *Dispatcher {
	d := &Dispatcher{sinks: sinks, queue: make(chan Event, 256), report: report, done: make(chan struct{})}
	go d.run(ctx)
	return d
}

// Send queues an event; it reports false when the queue is full and the event was dropped.
func (d *Dispatcher) Send(e Event) bool {
	if d == nil || len(d.sinks) == 0 {
		return true
	}
	select {
	case d.queue <- e:
		return true
	default:
		return false
	}
}

// Done is closed when the worker has stopped.
func (d *Dispatcher) Done() <-chan struct{} { return d.done }

func (d *Dispatcher) run(ctx context.Context) {
	defer close(d.done)
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-d.queue:
			for _, s := range d.sinks {
				if e.Initial && !WantsInitial(s) {
					continue
				}
				d.report(s.Name(), s.Send(ctx, e))
			}
		}
	}
}
