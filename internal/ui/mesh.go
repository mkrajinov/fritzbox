package ui

import (
	"fmt"

	"github.com/mkrajinovic/fbctl/internal/fritz"
)

// MeshHeader renders a mesh node title line (shared with the TUI).
func MeshHeader(n fritz.MeshNode) string {
	role := "router"
	if n.Role != "master" {
		role = "repeater"
	}
	line := GradientPill(GlyphRouter+" "+n.Name) + " " + Muted.Render(n.Model+" · "+role)
	if n.Uplink != nil {
		line += Muted.Render(fmt.Sprintf(" · uplink %s to %s %s", n.Uplink.Type, n.Parent, Kbit(n.Uplink.MaxKbit)))
	}
	return line
}

// MeshClientLine renders one connected device.
func MeshClientLine(c fritz.MeshLink) string {
	// Pad before styling: escape codes would otherwise break the column widths.
	name := fmt.Sprintf("%-28s", truncate(c.Name, 28))
	if c.IsMeshed {
		name = Bold.Render(name)
	}
	iface := c.Type
	if c.Iface != "" {
		iface += " " + c.Iface
	}
	return fmt.Sprintf("%s %-18s %s %s  %s", name, c.MAC, Muted.Render(fmt.Sprintf("%-16s", iface)),
		Up.Render(fmt.Sprintf("%s %-14s", GlyphUp, Kbit(c.TxKbit))), Down.Render(GlyphDown+" "+Kbit(c.RxKbit)))
}

// Kbit formats a kbit/s value from the mesh topology; 0 means unknown.
func Kbit(k int64) string {
	if k <= 0 {
		return "–"
	}
	return Bits(k * 1000)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
