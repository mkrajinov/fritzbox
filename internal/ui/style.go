// Package ui holds shared styling and formatting for CLI and TUI output.
//
// Design rules: no edge stripes/borders for accents or active states. Selection and
// emphasis use filled pills in the accent gradient; groups use rounded cards with a
// small pill label. One colour per meaning.
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
)

// Palette: one colour per meaning.
var (
	AccentFrom = "#7C3AED" // violet
	AccentTo   = "#DB2777" // pink

	ColorOK    = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"} // up, on, connected
	ColorBad   = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"} // down, off, error, missed
	ColorWarn  = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"} // update available, 2FA
	ColorUp    = lipgloss.AdaptiveColor{Light: "#C2410C", Dark: "#FB923C"} // upload / outgoing (always left)
	ColorDown  = lipgloss.AdaptiveColor{Light: "#0369A1", Dark: "#38BDF8"} // download / incoming (always right)
	ColorMuted = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
	ColorText  = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#F3F4F6"}
	ColorChip  = lipgloss.AdaptiveColor{Light: "#EDE9FE", Dark: "#2E1065"}
	ColorCard  = lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#3F3F46"}
)

// Glyphs: one per meaning, used identically in CLI and TUI.
const (
	GlyphRouter   = "◈"
	GlyphStatus   = "◉"
	GlyphHosts    = "⌂"
	GlyphMesh     = "⋔"
	GlyphLog      = "☰"
	GlyphWLAN     = "≋"
	GlyphCalls    = "☏"
	GlyphHome     = "⌁"
	GlyphContacts = "▤"
	GlyphUp       = "↑"
	GlyphDown     = "↓"
	GlyphOn       = "●"
	GlyphOff      = "○"
	GlyphMissed   = "✕"
	GlyphIn       = "↙"
	GlyphOut      = "↗"
	GlyphTemp     = "°"
	GlyphWarning  = "!"
)

var (
	Muted = lipgloss.NewStyle().Foreground(ColorMuted)
	Bold  = lipgloss.NewStyle().Bold(true).Foreground(ColorText)
	OK    = lipgloss.NewStyle().Foreground(ColorOK)
	Bad   = lipgloss.NewStyle().Foreground(ColorBad)
	Warn  = lipgloss.NewStyle().Foreground(ColorWarn)
	Up    = lipgloss.NewStyle().Foreground(ColorUp)
	Down  = lipgloss.NewStyle().Foreground(ColorDown)
)

// GradientPill renders " text " with a per-cell background blended across the accent gradient.
func GradientPill(text string) string {
	runes := []rune(" " + text + " ")
	from, _ := colorful.Hex(AccentFrom)
	to, _ := colorful.Hex(AccentTo)
	var b strings.Builder
	for i, r := range runes {
		t := 0.0
		if len(runes) > 1 {
			t = float64(i) / float64(len(runes)-1)
		}
		c := from.BlendLuv(to, t).Clamped().Hex()
		b.WriteString(lipgloss.NewStyle().Background(lipgloss.Color(c)).Foreground(lipgloss.Color("#FFFFFF")).Bold(true).Render(string(r)))
	}
	return b.String()
}

// Pill renders an unselected pill (muted chip background).
func Pill(text string) string {
	return lipgloss.NewStyle().Background(ColorChip).Foreground(ColorText).Render(" " + text + " ")
}

// Label is the small pill section label above a card.
func Label(text string) string {
	return lipgloss.NewStyle().Background(ColorChip).Foreground(lipgloss.Color(AccentFrom)).Bold(true).Padding(0, 1).Render(text)
}

// Card wraps content in a rounded box with a pill label on top.
func Card(label, body string, width int) string {
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorCard).Padding(0, 1)
	if width > 0 {
		box = box.Width(width - 2)
	}
	return Label(label) + "\n" + box.Render(body)
}

// Switch renders a custom pill switch.
func Switch(on bool) string {
	if on {
		return GradientPill(" ON ●")
	}
	return Pill("○ OFF")
}

// Bytes formats a byte count with SI units (one format everywhere).
func Bytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

// Rate formats bytes/s as bits/s, which is how ISPs quote speeds.
func Rate(bytesPerSec int64) string { return Bits(bytesPerSec * 8) }

// Bits formats a bit rate.
func Bits(bits int64) string {
	const unit = 1000
	if bits < unit {
		return fmt.Sprintf("%d bit/s", bits)
	}
	div, exp := int64(unit), 0
	for m := bits / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cbit/s", float64(bits)/float64(div), "kMGTPE"[exp])
}

// Duration formats like "3d 04:12:09".
func Duration(d time.Duration) string {
	s := int64(d.Seconds())
	days := s / 86400
	s %= 86400
	out := fmt.Sprintf("%02d:%02d:%02d", s/3600, (s%3600)/60, s%60)
	if days > 0 {
		out = fmt.Sprintf("%dd %s", days, out)
	}
	return out
}

// OnOff renders a coloured on/off dot.
func OnOff(on bool) string {
	if on {
		return OK.Render(GlyphOn)
	}
	return Muted.Render(GlyphOff)
}

// Sparkline renders values (oldest first) as block characters.
func Sparkline(vals []int64, width int, style lipgloss.Style) string {
	if width <= 0 || len(vals) == 0 {
		return ""
	}
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	blocks := []rune(" ▁▂▃▄▅▆▇█")
	var mx int64 = 1
	for _, v := range vals {
		mx = max(mx, v)
	}
	var b strings.Builder
	for _, v := range vals {
		b.WriteRune(blocks[int(v*int64(len(blocks)-1)/mx)])
	}
	return style.Render(b.String())
}
