package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIPhonebookEditing(t *testing.T) {
	_, run := setupCLI(t)
	steps := []struct {
		args []string
		want []string
	}{
		{[]string{"phonebook", "add", "Telefonbuch", "--name", "Jane Doe", "--number", "mobile:0170 555", "--number", "030111", "--vip"}, []string{"added", "Jane Doe", "0170555", "id 101"}},
		{[]string{"phonebook", "show", "0", "-s", "jane"}, []string{"101", "Jane Doe", "0170555", "mobile", "030111", "home"}},
		{[]string{"phonebook", "edit", "0", "jane", "--name", "Jane Smith", "--number", "work:0309"}, []string{"updated", "Jane Smith", "0309"}},
		{[]string{"phonebook", "delete", "0", "101", "--yes"}, []string{"deleted Jane Smith"}},
	}
	for _, s := range steps {
		out, err := run(s.args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", s.args, err, out)
		}
		for _, w := range s.want {
			if !strings.Contains(out, w) {
				t.Errorf("%v: missing %q in\n%s", s.args, w, out)
			}
		}
	}
	for _, bad := range [][]string{
		{"phonebook", "add", "0", "--name", "X"},                                       // no number
		{"phonebook", "add", "0", "--name", "X", "--number", "fax:1"},                  // bad type
		{"phonebook", "edit", "0", "bob"},                                              // nothing to change
		{"phonebook", "edit", "0", "nobody", "--name", "Y"},                            // no match
		{"phonebook", "add", "Google", "--name", "X", "--number", "1"},                 // online book
		{"phonebook", "add", "9", "--name", "X", "--number", "1"},                      // no book
		{"phonebook", "export", "0", "--format", "pdf"},                                // bad format
		{"phonebook", "import", "0", filepath.Join(t.TempDir(), "missing.vcf")},        // no file
		{"phonebook", "import", "0", filepath.Join(t.TempDir(), "contacts.txt"), "-n"}, // unknown type
	} {
		if out, err := run(bad...); err == nil {
			t.Errorf("%v should fail:\n%s", bad, out)
		}
	}
}

func TestCLIPhonebookImportExport(t *testing.T) {
	_, run := setupCLI(t)
	dir := t.TempDir()
	vcf := filepath.Join(dir, "in.vcf")
	os.WriteFile(vcf, []byte("BEGIN:VCARD\nVERSION:3.0\nFN:Alice\nTEL;TYPE=CELL:01701234567\nEND:VCARD\n"+
		"BEGIN:VCARD\nVERSION:3.0\nFN:Dave\nTEL;TYPE=HOME:0309876\nEND:VCARD\n"), 0o600)

	out, err := run("phonebook", "import", "0", vcf, "--dry-run")
	if err != nil || !strings.Contains(out, "would add") || !strings.Contains(out, "Dave") || strings.Contains(out, "would add Alice") ||
		!strings.Contains(out, "would add 1 contacts to Telefonbuch, 1 already there") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if out, err = run("phonebook", "import", "0", vcf); err != nil || !strings.Contains(out, "added 1 contacts") {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if out, err = run("phonebook", "import", "0", vcf); err != nil || !strings.Contains(out, "added 0 contacts to Telefonbuch, 2 already there") {
		t.Fatalf("re-import should skip: %v\n%s", err, out)
	}

	csvPath := filepath.Join(dir, "out.csv")
	if out, err = run("phonebook", "export", "0", "-o", csvPath); err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	data, _ := os.ReadFile(csvPath)
	if !strings.HasPrefix(string(data), "name,type,number,email,vip\n") || !strings.Contains(string(data), "Dave,home,0309876") ||
		!strings.Contains(string(data), "Alice,mobile,01701234567,alice@example.com") {
		t.Errorf("csv export:\n%s", data)
	}
	if _, err = run("phonebook", "export", "0", "-o", csvPath); err == nil {
		t.Error("export must not overwrite an existing file")
	}
	if out, err = run("phonebook", "export", "Telefonbuch"); err != nil || !strings.Contains(out, "BEGIN:VCARD") || !strings.Contains(out, "FN:Dave") {
		t.Errorf("vcard to stdout: %v\n%s", err, out)
	}
}

func TestCLICallBlocking(t *testing.T) {
	_, run := setupCLI(t)
	steps := []struct {
		args []string
		want string
	}{
		{[]string{"calls", "blocked"}, "no blocked numbers"},
		{[]string{"calls", "block", "0900 123456", "--name", "Spam"}, "0900123456 blocked (entry 101)"},
		{[]string{"calls", "block", "0900123456"}, "already blocked (entry 101)"},
		{[]string{"calls", "blocked"}, "Spam"},
		{[]string{"calls", "unblock", "0900-123456"}, "unblocked 0900123456 (Spam)"},
		{[]string{"calls", "block", "+49301"}, "+49301 blocked"},
		{[]string{"calls", "unblock", "--id", "102"}, "unblocked +49301"},
	}
	for _, s := range steps {
		out, err := run(s.args...)
		if err != nil || !strings.Contains(out, s.want) {
			t.Fatalf("%v: %v\n%s", s.args, err, out)
		}
	}
	for _, bad := range [][]string{{"calls", "block", "unknown"}, {"calls", "unblock", "123"}, {"calls", "unblock", "--id", "x"}} {
		if out, err := run(bad...); err == nil {
			t.Errorf("%v should fail:\n%s", bad, out)
		}
	}
}

func TestCLITamPlay(t *testing.T) {
	fb, run := setupCLI(t)
	dir := t.TempDir()
	out, err := run("tam", "play", "0:0", "-o", filepath.Join(dir, "msg"))
	if err != nil || !strings.Contains(out, "msg.wav") || !strings.Contains(out, "Alice") {
		t.Fatalf("save: %v\n%s", err, out)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "msg.wav")); !strings.HasPrefix(string(data), "RIFF") {
		t.Errorf("saved data: %q", data)
	}
	t.Setenv("FB_PLAYER", "true") // a player that succeeds without sound
	if out, err = run("tam", "play", "0:0"); err != nil || !strings.Contains(out, "playing Alice") {
		t.Fatalf("play: %v\n%s", err, out)
	}
	if m := fb.CallsTo("x_tam", "MarkMessage"); len(m) != 1 || !strings.Contains(m[0].Body, "<NewMarkedAsRead>1</NewMarkedAsRead>") {
		t.Errorf("new message should be marked heard after playing: %+v", m)
	}
	if _, err = run("tam", "play", "0:1"); err != nil {
		t.Fatal(err)
	}
	if m := fb.CallsTo("x_tam", "MarkMessage"); len(m) != 1 {
		t.Errorf("a heard message must not be marked again: %d", len(m))
	}
	t.Setenv("FB_PLAYER", "false")
	if _, err = run("tam", "play", "0:0", "--keep-new"); err == nil {
		t.Error("a failing player should be reported")
	}
}
