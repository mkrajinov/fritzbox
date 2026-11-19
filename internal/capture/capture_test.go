package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCapturedFixtures parses every reviewed capture in testdata/<name>/ (as written by
// `fb capture`) with fb's parsers. Add a capture there after checking it holds nothing private.
func TestCapturedFixtures(t *testing.T) {
	dirs, _ := filepath.Glob(filepath.Join("testdata", "*", "README.txt"))
	if len(dirs) == 0 {
		t.Skip("no captures in testdata/")
	}
	for _, readme := range dirs {
		dir := filepath.Dir(readme)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			docs := map[string][]byte{}
			files, _ := filepath.Glob(filepath.Join(dir, "docs", "*"))
			for _, f := range files {
				data, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				docs[filepath.Base(f)] = data
			}
			for _, f := range Check(docs, dir) {
				t.Log(f)
				if !strings.HasSuffix(f, "err=<nil>") {
					t.Errorf("parser failed: %s", f)
				}
			}
		})
	}
}

func TestScrubber(t *testing.T) {
	s := NewScrubber(false)
	in := "sid=0123456789abcdef MAC aa:bb:cc:dd:ee:ff IP 84.1.2.3 LAN 192.168.178.20 mask 255.255.255.0 v6 2a02:810d:1:2::5/64 ll fe80::1 x@y.de box.myfritz.net"
	out := s.Text(in)
	for _, gone := range []string{"0123456789abcdef", "aa:bb", "84.1.2.3", "2a02:810d", "x@y.de", "box.myfritz.net"} {
		if strings.Contains(strings.ToLower(out), gone) {
			t.Errorf("%q not scrubbed: %s", gone, out)
		}
	}
	for _, kept := range []string{"192.168.178.20", "255.255.255.0", "/64"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q should be kept: %s", kept, out)
		}
	}
	if s.Text("AA:BB:CC:DD:EE:FF") != s.Text("aa:bb:cc:dd:ee:ff") {
		t.Error("MAC mapping must ignore case")
	}
	if a, b := s.Phone("0301234567"), s.Phone("030 123 4567"); a != b || len(a) != 10 || a[0] != '0' {
		t.Errorf("phone: %q %q", a, b)
	}
	if got := s.Phone("SIP: 030123"); !strings.HasPrefix(got, "SIP: 0") || strings.Contains(got, "123") {
		t.Errorf("sip: %q", got)
	}
	for _, keep := range []string{"**610", "", "unknown"} {
		if s.Phone(keep) != keep {
			t.Errorf("%q should be kept", keep)
		}
	}
	if v := s.Value("NewKeyPassphrase", "hunter2"); v != "REDACTED" {
		t.Errorf("secret: %q", v)
	}
	if v := s.Value("NewX_AVM-DE_UserList", `<List><Username last_user="1">mkrajinov</Username></List>`); strings.Contains(v, "mkrajinov") {
		t.Errorf("user list: %q", v)
	}
	if forbidden("GetInfo") || !forbidden("SetEnable") || !forbidden("GetSecurityKeys") || !forbidden("X_AVM-DE_DoUpdate") || forbidden("X_AVM-DE_DialGetConfig") {
		t.Error("forbidden() misclassifies actions")
	}
	for _, st := range Steps([]int{1, 2, 3}) {
		if forbidden(st.Action) {
			t.Errorf("step %s %s is not read-only", st.Service, st.Action)
		}
	}
}
