// Package callmon reads live call events from the FRITZ!Box call monitor (TCP 1012).
// Enable it once from a connected phone with #96*5* (disable: #96*4*).
package callmon

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const Port = 1012

// Event is one call monitor line.
//
//	date;RING;id;caller;callee;line;
//	date;CALL;id;extension;caller;callee;line;
//	date;CONNECT;id;extension;number;
//	date;DISCONNECT;id;durationSeconds;
type Event struct {
	Time      time.Time     `json:"time"`
	Kind      string        `json:"kind"` // RING, CALL, CONNECT, DISCONNECT
	ConnID    int           `json:"conn_id"`
	Extension string        `json:"extension,omitempty"`
	Caller    string        `json:"caller,omitempty"`
	Callee    string        `json:"callee,omitempty"`
	Line      string        `json:"line,omitempty"`
	Duration  time.Duration `json:"duration,omitempty"`
	Raw       string        `json:"raw"`
}

// Parse parses one line. loc is used for the box's local timestamps.
func Parse(line string, loc *time.Location) (Event, error) {
	line = strings.TrimRight(line, "\r\n")
	f := strings.Split(line, ";")
	if len(f) < 4 {
		return Event{}, fmt.Errorf("callmon: short line %q", line)
	}
	t, err := time.ParseInLocation("02.01.06 15:04:05", f[0], loc)
	if err != nil {
		return Event{}, fmt.Errorf("callmon: bad time %q", f[0])
	}
	id, _ := strconv.Atoi(f[2])
	e := Event{Time: t, Kind: f[1], ConnID: id, Raw: line}
	get := func(i int) string {
		if i < len(f) {
			return f[i]
		}
		return ""
	}
	switch e.Kind {
	case "RING":
		e.Caller, e.Callee, e.Line = get(3), get(4), get(5)
	case "CALL":
		e.Extension, e.Caller, e.Callee, e.Line = get(3), get(4), get(5), get(6)
	case "CONNECT":
		e.Extension, e.Caller = get(3), get(4)
	case "DISCONNECT":
		s, _ := strconv.Atoi(get(3))
		e.Duration = time.Duration(s) * time.Second
	default:
		return Event{}, fmt.Errorf("callmon: unknown event %q", e.Kind)
	}
	return e, nil
}

// Watch connects to host:1012 and streams events until ctx is done.
// It reconnects with exponential backoff (1s → 60s). Errors are sent on errs (non-blocking).
func Watch(ctx context.Context, host string, events chan<- Event, errs chan<- error) {
	watchAddr(ctx, net.JoinHostPort(host, strconv.Itoa(Port)), events, errs)
}

func watchAddr(ctx context.Context, addr string, events chan<- Event, errs chan<- error) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := readOnce(ctx, addr, events, func() { backoff = time.Second })
		if ctx.Err() != nil {
			return
		}
		select {
		case errs <- fmt.Errorf("callmon: %w (retry in %s; is the monitor enabled with #96*5*?)", err, backoff):
		default:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func readOnce(ctx context.Context, addr string, events chan<- Event, connected func()) error {
	d := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	connected()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		ev, err := Parse(sc.Text(), time.Local)
		if err != nil {
			continue
		}
		select {
		case events <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return fmt.Errorf("connection closed by box")
}
