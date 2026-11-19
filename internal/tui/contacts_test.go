package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func typeText(t *testing.T, m tea.Model, s string) tea.Model {
	t.Helper()
	keys := make([]string, 0, len(s))
	for _, r := range s {
		if r == ' ' {
			keys = append(keys, "space")
			continue
		}
		keys = append(keys, string(r))
	}
	return press(t, m, keys...)
}

func TestContactAddEditDelete(t *testing.T) {
	m, fb := setup(t)
	m = press(t, m, "6", "n")
	if v := plain(m.View()); !strings.Contains(v, "New contact in Telefonbuch · name") {
		t.Fatalf("no name prompt:\n%s", v)
	}
	m = typeText(t, m, "Dave")
	m = press(t, m, "enter")
	if v := plain(m.View()); !strings.Contains(v, "· numbers") {
		t.Fatalf("no numbers prompt:\n%s", v)
	}
	m = typeText(t, m, "mobile:0170 1, fax:2")
	m = press(t, m, "enter")
	if v := plain(m.View()); !strings.Contains(v, `type "fax"`) {
		t.Fatalf("bad type not reported:\n%s", v)
	}
	for range len(", fax:2") {
		m = press(t, m, "backspace")
	}
	if v := plain(m.View()); !strings.Contains(v, "mobile:0170 1") || strings.Contains(v, "fax:2") {
		t.Fatalf("backspace did not remove the bad number:\n%s", v)
	}
	m = press(t, m, "enter")
	if v := plain(m.View()); !strings.Contains(v, "added Dave") || !strings.Contains(v, "01701") {
		t.Fatalf("contact not added:\n%s", v)
	}

	// Edit the first contact (Alice): change the name, keep the numbers.
	m = press(t, m, "e")
	for range len("Alice") {
		m = press(t, m, "backspace")
	}
	m = typeText(t, m, "Alicia")
	m = press(t, m, "enter")
	if v := plain(m.View()); !strings.Contains(v, "mobile:01701234567, home:030111222") {
		t.Fatalf("numbers not prefilled:\n%s", v)
	}
	m = press(t, m, "enter")
	if v := plain(m.View()); !strings.Contains(v, "saved Alicia") || !strings.Contains(v, "Alicia") {
		t.Fatalf("not saved:\n%s", v)
	}

	m = press(t, m, "X")
	if v := plain(m.View()); !strings.Contains(v, "Delete Alicia from Telefonbuch?") {
		t.Fatalf("no delete confirm:\n%s", v)
	}
	m = press(t, m, "y")
	if v := plain(m.View()); !strings.Contains(v, "deleted Alicia") || strings.Contains(v, "Alicia  ") {
		t.Fatalf("not deleted:\n%s", v)
	}
	if n := len(fb.CallsTo("x_contact", "DeletePhonebookEntryUID")); n != 1 {
		t.Errorf("delete calls: %d", n)
	}
}

func TestBlockCaller(t *testing.T) {
	m, fb := setup(t)
	m = press(t, m, "5", "b")
	if v := plain(m.View()); !strings.Contains(v, "Block Alice (01701234567)?") {
		t.Fatalf("no block confirm:\n%s", v)
	}
	m = press(t, m, "y")
	if v := plain(m.View()); !strings.Contains(v, "blocked Alice (01701234567)") {
		t.Fatalf("not blocked:\n%s", v)
	}
	m = press(t, m, "b", "y")
	if v := plain(m.View()); !strings.Contains(v, "already blocked") {
		t.Fatalf("second block should report it:\n%s", v)
	}
	if n := len(fb.CallsTo("x_contact", "SetCallBarringEntry")); n != 1 {
		t.Errorf("SetCallBarringEntry calls: %d", n)
	}
}
