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
	GlyphPhone    = "✆"
	GlyphSystem   = "⚙"
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

// Signal renders a 0–100 signal value as four bars plus the number, coloured by quality
// (≥70 excellent, ≥50 good, ≥30 fair, else weak — the same thresholds as fritz.SignalLabel).
func Signal(s int) string {
	n := 1
	switch {
	case s >= 70:
		n = 4
	case s >= 50:
		n = 3
	case s >= 30:
		n = 2
	}
	bars := strings.Repeat("▮", n) + strings.Repeat("▯", 4-n)
	st := Bad
	switch n {
	case 4, 3:
		st = OK
	case 2:
		st = Warn
	}
	return st.Render(bars) + fmt.Sprintf(" %3d", s)
}

// Ago formats a duration coarsely: "just now", "12m", "2h 05m", "3d 4h".
func Ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

// When formats a time relative to now: "18:02" today, "Mon 18:02" within a week, else the date.
func When(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	switch {
	case t.IsZero():
		return "–"
	case t.YearDay() == now.YearDay() && t.Year() == now.Year():
		return t.Format("15:04")
	case now.Sub(t) < 6*24*time.Hour:
		return t.Format("Mon 15:04")
	}
	return t.Format("2006-01-02 15:04")
}

// Presence renders "● home since 18:02 (2h 05m)" or "○ away since Mon 08:10 (1d 3h)".
func Presence(home bool, since, now time.Time) string {
	state := Muted.Render(GlyphOff + " away")
	if home {
		state = OK.Render(GlyphOn + " home")
	}
	if since.IsZero() {
		return state
	}
	return state + " since " + When(since, now) + Muted.Render(" ("+Ago(now.Sub(since))+")")
}
