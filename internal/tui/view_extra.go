package tui

import (
	"fmt"
	"strings"

	"github.com/mkrajinovic/fbctl/internal/fritz"
	"github.com/mkrajinovic/fbctl/internal/ui"
)

func hostDetail(h fritz.Host, w int) string {
	state := ui.Muted.Render(ui.GlyphOff + " inactive")
	if h.Active {
		state = ui.OK.Render(ui.GlyphOn + " active")
	}
	link := h.Interface
	if h.SpeedMbit > 0 {
		link += fmt.Sprintf(" · %d Mbit/s", h.SpeedMbit)
	}
	if h.Port != "" && h.Port != "0" {
		link += " · port " + h.Port
	}
	net := "home network"
	if h.Guest {
		net = "guest network"
	}
	rows := []string{
		row(ui.GlyphHosts, orDash(h.Name), state),
		row("ip", h.IP, net),
		row("id", h.MAC, "MAC address"),
		row("⇄", orDash(link), "connection"),
	}
	return ui.Card("Details", strings.Join(rows, "\n"), w)
}

func (m Model) meshView(w int) string {
	if m.mesh == nil {
		return ui.Card("Mesh", ui.Muted.Render("loading…"), w)
	}
	if len(m.mesh) == 0 {
		return ui.Card("Mesh", ui.Muted.Render("no mesh nodes reported"), w)
	}
	// Flatten into rows so the cursor can move across nodes and their devices.
	var all []string
	for _, n := range m.mesh {
		all = append(all, ui.MeshHeader(n))
		for _, c := range n.Clients {
			all = append(all, "  "+ui.MeshClientLine(c))
		}
	}
	from, to := m.listWindow(len(all), m.cursor[tabMesh])
	lines := make([]string, 0, to-from)
	for i := from; i < to; i++ {
		lines = append(lines, mark(i == m.cursor[tabMesh], all[i], w))
	}
	devices := 0
	for _, n := range m.mesh {
		devices += len(n.Clients)
	}
	return ui.Card(fmt.Sprintf("Mesh · %d nodes · %d connected devices", len(m.mesh), devices), strings.Join(lines, "\n"), w)
}

func (m Model) logView(w int) string {
	if m.logEvs == nil {
		return ui.Card("Event log", ui.Muted.Render("loading…"), w)
	}
	from, to := m.listWindow(len(m.logEvs), m.cursor[tabLog])
	lines := make([]string, 0, to-from)
	msgW := max(20, w-36)
	for i := from; i < to; i++ {
		e := m.logEvs[i]
		line := fmt.Sprintf("%s  %s  %s", ui.Muted.Render(e.Time.Format("01-02 15:04:05")), ui.Muted.Render(fmt.Sprintf("%-4s", e.Group)), trunc(e.Msg, msgW))
		lines = append(lines, mark(i == m.cursor[tabLog], line, w))
	}
	return ui.Card(fmt.Sprintf("Event log · %d events", len(m.logEvs)), strings.Join(lines, "\n"), w)
}
