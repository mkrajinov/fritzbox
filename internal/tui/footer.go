package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/mkrajinov/fritzbox/internal/ui"
)

// Hint groups, each with its own chip colour. Groups are separated by a thin divider.
const (
	groupTab    = iota // actions of the current tab
	groupGlobal        // available on every tab
	groupSystem        // help, quit (right-aligned)
)

// hint is one key in the action bar. Lower prio survives longer on narrow terminals;
// prio 0 is never dropped. danger marks actions that reboot, disconnect or install.
type hint struct {
	key, label string
	group      int
	prio       int
	danger     bool
}

var (
	colBar      = lipgloss.AdaptiveColor{Light: "#F4F4F5", Dark: "#27272A"}
	colChipText = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#18181B"}
	colDanger   = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	groupColors = map[int]lipgloss.TerminalColor{
		groupTab:    lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#22D3EE"},
		groupGlobal: lipgloss.AdaptiveColor{Light: "#7E22CE", Dark: "#C084FC"},
		groupSystem: lipgloss.AdaptiveColor{Light: "#57534E", Dark: "#A8A29E"},
	}
	styBar    = lipgloss.NewStyle().Background(colBar).Foreground(ui.ColorText)
	styBarDim = lipgloss.NewStyle().Background(colBar).Foreground(ui.ColorMuted)
)

func (m Model) hints() []hint {
	var h []hint
	n := 0
	add := func(k, d string) { h = append(h, hint{k, d, groupTab, 1 + n/2, false}); n++ }
	danger := func(k, d string) { h = append(h, hint{k, d, groupTab, 1 + n/2, true}); n++ }
	switch m.tab {
	case tabOverview:
		if len(m.opt.Routers) > 1 {
			add("A", "all routers")
		}
		danger("R", "reconnect")
		danger("B", "reboot")
	case tabHosts:
		add("enter", "details")
		add("/", "filter")
		add("a", "active only")
		add("s", "sort")
		add("w", "wake")
		add("b/u", "block/unblock")
		add("n", "rename")
		add("t", "ticket")
		add("f", "profile")
		add("p", "priority")
		add("o", "auto wake")
	case tabWLAN:
		add("space", "toggle")
		add("c", "QR code")
		add("p/P", "show/change password")
		add("e", "SSID")
		add("n", "channel")
		add("H", "hide")
		add("W", "WPS")
		danger("O", "all off/on")
	case tabCalls:
		add("f", "filter")
		add("d", "days")
		add("c", "call back")
		add("b", "block caller")
		add("x", "hang up")
	case tabContacts:
		add("/", "search")
		add("d", "dial")
		add("n", "new")
		add("e", "edit")
		danger("X", "delete")
		if len(m.books) > 1 {
			add("p", "next phonebook")
		}
		add("x", "hang up")
	case tabPhone:
		add("p", "play message")
		add("space", "toggle / mark heard")
		danger("x", "delete / hang up")
		add("c", "call back")
	case tabSystem:
		add("u", "check update")
		danger("I", "install")
		add("a", "auto mode")
		add("e", "DHCP range")
		add("H/L/M", "https/LE/MyFRITZ")
		add("b", "backup")
	case tabHome:
		add("space", "switch / off / run template")
		add("+/-", "temp")
	case tabLog:
		add("g", "group")
	}
	h = append(h, hint{"tab", "switch", groupGlobal, 2, false})
	if len(m.opt.Routers) > 1 {
		h = append(h, hint{"[ ]", "router", groupGlobal, 3, false})
	}
	return append(h,
		hint{"r", "refresh", groupGlobal, 4, false},
		hint{"?", "help", groupSystem, 0, false},
		hint{"q", "quit", groupSystem, 0, false},
	)
}

func (m Model) footer() string {
	line := actionBar(m.hints(), max(m.width, 60))
	if m.flash != "" {
		st := ui.Bad
		if m.flashOK {
			st = ui.OK
		}
		line = st.Render(m.flash) + "\n" + line
	}
	return line
}

func renderHint(h hint) string {
	bg := groupColors[h.group]
	if h.danger {
		bg = colDanger
	}
	chip := lipgloss.NewStyle().Background(bg).Foreground(colChipText).Bold(true).Padding(0, 1).Render(h.key)
	return chip + styBar.Render(" "+h.label)
}

// actionBar renders hints into exactly width cells on a filled bar: tab and global actions
// on the left, help and quit on the right, dropping the least important hints until it fits.
func actionBar(hints []hint, width int) string {
	hints = append([]hint(nil), hints...)
	for {
		left, right := renderGroups(hints, false), renderGroups(hints, true)
		if gap := width - lipgloss.Width(left) - lipgloss.Width(right); gap >= 2 {
			return left + styBar.Render(strings.Repeat(" ", gap)) + right
		}
		drop := -1
		for i, h := range hints {
			if h.prio > 0 && (drop < 0 || h.prio >= hints[drop].prio) {
				drop = i
			}
		}
		if drop < 0 { // only help and quit left: let lipgloss cut it
			return lipgloss.NewStyle().MaxWidth(width).Render(left + styBar.Render("  ") + right)
		}
		hints = append(hints[:drop], hints[drop+1:]...)
	}
}

// renderGroups joins the hints of the left groups (tab, global) or the right one (system),
// with a thin divider between groups.
func renderGroups(hints []hint, system bool) string {
	var b strings.Builder
	b.WriteString(styBar.Render(" "))
	prev := -1
	for _, h := range hints {
		if (h.group == groupSystem) != system {
			continue
		}
		switch {
		case prev == -1:
		case prev != h.group:
			b.WriteString(styBarDim.Render("  │  "))
		default:
			b.WriteString(styBar.Render("   "))
		}
		b.WriteString(renderHint(h))
		prev = h.group
	}
	b.WriteString(styBar.Render(" "))
	return b.String()
}
