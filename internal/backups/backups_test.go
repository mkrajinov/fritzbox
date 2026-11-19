package backups

import (
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const exportA = `**** FRITZ!Box 5590 Fiber CONFIGURATION EXPORT
Password=$$$$AAAABBBB
FirmwareVersion=284.08.25
**** CFGFILE:ar7.cfg
/*
 * /var/flash/ar7.cfg
 * Thu Nov 19 10:00:00 2026
 */
ar7cfg {
        mode = dsldmode_router;
        dhcpserver {
                start = 192.168.178.20;
                end = 192.168.178.200;
        }
        passwd = "$$$$SECRET1";
}
**** END OF FILE ****
**** CFGFILE:wlan.cfg
wlancfg {
        ssid = "home";
        channel = 0;
}
**** END OF FILE ****
**** END OF EXPORT 1A2B3C4D ****
`

func TestSameIgnoresVolatileLines(t *testing.T) {
	b := strings.NewReplacer("$$$$AAAABBBB", "$$$$ZZZZ", "Thu Nov 19 10:00:00", "Fri Nov 20 03:00:00",
		"$$$$SECRET1", "$$$$OTHERIV", "1A2B3C4D", "99999999").Replace(exportA)
	if !Same([]byte(exportA), []byte(b)) {
		t.Error("exports that differ only in volatile lines should be the same")
	}
	if Same([]byte(exportA), []byte(strings.Replace(exportA, "channel = 0", "channel = 36", 1))) {
		t.Error("a changed setting must be detected")
	}
}

func TestDiff(t *testing.T) {
	b := strings.Replace(exportA, "end = 192.168.178.200;", "end = 192.168.178.150;", 1)
	b = strings.Replace(b, `ssid = "home";`, `ssid = "home";`+"\n        hidden = yes;", 1)
	b = strings.Replace(b, "Thu Nov 19 10:00:00", "Sat Nov 21 10:00:00", 1)
	hunks := Diff([]byte(exportA), []byte(b), 1)
	if len(hunks) != 2 {
		t.Fatalf("hunks: %+v", hunks)
	}
	h := hunks[0]
	if h.Section != "ar7.cfg" || h.OldLine != 12 || !slices.Equal(h.Ops, []Op{
		{' ', "                start = 192.168.178.20;"},
		{'-', "                end = 192.168.178.200;"},
		{'+', "                end = 192.168.178.150;"},
		{' ', "        }"},
	}) {
		t.Errorf("hunk 1: %+v", h)
	}
	if hunks[1].Section != "wlan.cfg" || !slices.Contains(hunks[1].Ops, Op{'+', "        hidden = yes;"}) {
		t.Errorf("hunk 2: %+v", hunks[1])
	}
	if d := Diff([]byte(exportA), []byte(exportA), 3); len(d) != 0 {
		t.Errorf("identical exports: %+v", d)
	}
}

// TestDiffRandom checks that applying the diff ops reproduces both inputs.
func TestDiffRandom(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	words := []string{"a", "b", "c", "d", "e"}
	gen := func() []string {
		out := make([]string, r.Intn(30))
		for i := range out {
			out[i] = words[r.Intn(len(words))]
		}
		return out
	}
	for n := 0; n < 300; n++ {
		a, b := gen(), gen()
		script := myers(a, b)
		var gotA, gotB []string
		i, j := 0, 0
		for _, op := range script {
			switch op {
			case ' ':
				if a[i] != b[j] {
					t.Fatalf("context mismatch %q %q", a[i], b[j])
				}
				gotA, gotB = append(gotA, a[i]), append(gotB, b[j])
				i, j = i+1, j+1
			case '-':
				gotA = append(gotA, a[i])
				i++
			case '+':
				gotB = append(gotB, b[j])
				j++
			}
		}
		if !slices.Equal(gotA, a) || !slices.Equal(gotB, b) {
			t.Fatalf("script does not reproduce inputs:\n%v\n%v\n%s", a, b, script)
		}
	}
}

func TestSaveListPrune(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	t0 := time.Date(2026, 11, 19, 3, 0, 0, 0, time.Local)
	for i := 0; i < 4; i++ {
		data := strings.Replace(exportA, "channel = 0", "channel = "+string(rune('1'+i)), 1)
		path, unchanged, err := Save(dir, "home", []byte(data), t0.Add(time.Duration(i)*24*time.Hour), true)
		if err != nil || unchanged {
			t.Fatalf("save %d: %s %v %v", i, path, unchanged, err)
		}
	}
	// Same configuration as the newest one: skipped.
	same := strings.Replace(exportA, "channel = 0", "channel = 4", 1)
	same = strings.Replace(same, "1A2B3C4D", "FFFF0000", 1)
	if _, unchanged, err := Save(dir, "home", []byte(same), t0.Add(5*24*time.Hour), true); err != nil || !unchanged {
		t.Fatalf("unchanged export should be skipped: %v %v", unchanged, err)
	}
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep me"), 0o600)
	os.WriteFile(filepath.Join(dir, "office-20260101-000000.export"), []byte("other router"), 0o600)

	list, err := List(dir, "home")
	if err != nil || len(list) != 4 || !list[0].Time.Equal(t0) {
		t.Fatalf("list: %+v %v", list, err)
	}
	if st, _ := os.Stat(list[0].Path); st.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", st.Mode().Perm())
	}
	removed, err := Prune(dir, "home", 2)
	if err != nil || len(removed) != 2 || removed[0] != list[0].Path {
		t.Fatalf("prune: %v %v", removed, err)
	}
	for _, keep := range []string{"notes.txt", "office-20260101-000000.export", filepath.Base(list[3].Path)} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("prune removed %s", keep)
		}
	}
	if _, err := Prune(dir, "home", 0); err == nil {
		t.Error("keep 0 must be refused")
	}

	list, _ = List(dir, "home")
	for ref, want := range map[string]int{"1": 0, "-1": 1, "20261122": 1} {
		e, err := Resolve(list, ref)
		if err != nil || e.Path != list[want].Path {
			t.Errorf("resolve %q: %+v %v", ref, e, err)
		}
	}
	for _, bad := range []string{"5", "2025", "-"} {
		if _, err := Resolve(list, bad); err == nil {
			t.Errorf("resolve %q should fail", bad)
		}
	}
}
