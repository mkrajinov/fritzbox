package callmon

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	loc := time.UTC
	cases := []struct {
		line string
		want Event
	}{
		{"12.11.26 14:03:21;RING;0;01701234567;030123456;SIP0;", Event{Kind: "RING", ConnID: 0, Caller: "01701234567", Callee: "030123456", Line: "SIP0"}},
		{"12.11.26 14:05:00;CALL;1;10;030123456;01701234567;SIP0;", Event{Kind: "CALL", ConnID: 1, Extension: "10", Caller: "030123456", Callee: "01701234567", Line: "SIP0"}},
		{"12.11.26 14:03:25;CONNECT;0;10;01701234567;", Event{Kind: "CONNECT", Extension: "10", Caller: "01701234567"}},
		{"12.11.26 14:04:25;DISCONNECT;0;60;", Event{Kind: "DISCONNECT", Duration: time.Minute}},
	}
	for _, c := range cases {
		got, err := Parse(c.line, loc)
		if err != nil {
			t.Fatalf("%s: %v", c.line, err)
		}
		if got.Time.IsZero() || got.Time.Year() != 2026 {
			t.Errorf("%s: bad time %v", c.line, got.Time)
		}
		got.Time, got.Raw = time.Time{}, ""
		if got != c.want {
			t.Errorf("Parse(%q)\n got %+v\nwant %+v", c.line, got, c.want)
		}
	}
	if _, err := Parse("garbage", loc); err == nil {
		t.Error("expected error for garbage")
	}
}

func TestWatchStreamsAndStops(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.Write([]byte("12.11.26 14:03:21;RING;0;0170;030;SIP0;\n"))
		time.Sleep(time.Second)
		c.Close()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan Event, 1)
	errs := make(chan error, 4)
	done := make(chan struct{})
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	go func() {
		defer close(done)
		watchAddr(ctx, net.JoinHostPort("127.0.0.1", port), events, errs)
	}()
	select {
	case e := <-events:
		if e.Kind != "RING" {
			t.Fatalf("got %+v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Watch did not stop on cancel")
	}
}
