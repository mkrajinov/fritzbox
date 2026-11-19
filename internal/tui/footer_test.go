package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestActionBarFitsWidth(t *testing.T) {
	for _, tb := range []tab{tabOverview, tabHosts, tabWLAN, tabCalls, tabContacts, tabPhone, tabSystem, tabHome, tabLog} {
		m := Model{tab: tb, opt: Options{Routers: []string{"home", "office"}}}
		for _, w := range []int{40, 60, 100, 160, 240} {
			bar := actionBar(m.hints(), w)
			if got := lipgloss.Width(bar); got != w {
				t.Errorf("tab %v width %d: bar is %d cells", tb, w, got)
			}
			plain := plain(bar)
			if w >= 60 && (!strings.Contains(plain, "?  help") || !strings.Contains(plain, "q  quit")) {
				t.Errorf("tab %v width %d: help or quit missing: %q", tb, w, plain)
			}
		}
	}
	m := Model{tab: tabHosts}
	if plain := plain(actionBar(m.hints(), 240)); !strings.Contains(plain, "o  auto wake") {
		t.Errorf("wide bar should show every hint: %q", plain)
	}
}
