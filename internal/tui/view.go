package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/skip2/go-qrcode"

	"github.com/mkrajinovic/fbctl/internal/aha"
	"github.com/mkrajinovic/fbctl/internal/fritz"
	"github.com/mkrajinovic/fbctl/internal/ui"
)

// The selected row is a filled chip-coloured pill; no edge stripe or bar marker.
const rowIndent = "   "

var (
	selRow   = lipgloss.NewStyle().Background(ui.ColorChip).Bold(true)
	hintKey  = lipgloss.NewStyle().Foreground(ui.ColorText).Bold(true)
	modalBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(ui.AccentTo)).Padding(1, 3)
)

// View renders the screen.
func (m Model) View() string {
	w := max(m.width, 60)
	var b strings.Builder
	b.WriteString(m.header(w))
	b.WriteString("\n\n")
	b.WriteString(m.tabs())
	b.WriteString("\n\n")

	body := m.body(w)
	switch {
	case m.confirm != nil:
		body = modalBox.Render(ui.Bold.Render(m.confirm.prompt) + "\n\n" + ui.Muted.Render("y / enter = confirm   any other key = cancel"))
	case m.qr != "":
		body = modalBox.Render(m.qr + "\n" + ui.Muted.Render("any key to close"))
	case m.help:
		body = modalBox.Render(helpText())
	}
	b.WriteString(body)
	b.WriteString("\n\n")
	b.WriteString(m.footer())
	return b.String()
}

func (m Model) header(w int) string {
	var pills []string
	for i, n := range m.opt.Routers {
		label := ui.GlyphRouter + " " + n
		if i == m.router {
			pills = append(pills, ui.GradientPill(label))
		} else {
			pills = append(pills, ui.Pill(label))
		}
	}
	left := strings.Join(pills, " ")
	right := m.summary()
	gap := max(1, w-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", gap) + right
}

// summary is the live one-line status at the top: WAN state, ↑ up / ↓ down, uptime.
func (m Model) summary() string {
	switch {
	case m.connErr != nil:
		return ui.Bad.Render("not connected")
	case m.backend == nil:
		return ui.Muted.Render("connecting…")
	case m.status == nil:
		return ui.Muted.Render("loading…")
	}
	st := m.status
	if !st.HasWAN {
		return ui.Muted.Render(st.Model + " · no WAN")
	}
	conn := ui.Bad.Render(ui.GlyphOff + " offline")
	if st.Connected {
		conn = ui.OK.Render(ui.GlyphOn + " online")
	}
	return fmt.Sprintf("%s  %s  %s  %s", conn,
		ui.Up.Render(ui.GlyphUp+" "+ui.Rate(st.Rates.Up)),
		ui.Down.Render(ui.GlyphDown+" "+ui.Rate(st.Rates.Down)),
		ui.Muted.Render("up "+ui.Duration(st.ConnUptime)))
}

func (m Model) tabs() string {
	var out []string
	for t := range tabCount {
		if t == m.tab {
			out = append(out, ui.GradientPill(t.title()))
		} else {
			out = append(out, ui.Pill(t.title()))
		}
	}
	return strings.Join(out, " ")
}

func (m Model) footer() string {
	var hints []string
	add := func(k, d string) { hints = append(hints, hintKey.Render(k)+" "+ui.Muted.Render(d)) }
	add("tab", "switch")
	if len(m.opt.Routers) > 1 {
		add("[ ]", "router")
	}
	switch m.tab {
	case tabOverview:
		add("R", "reconnect")
		add("B", "reboot")
	case tabHosts:
		add("/", "filter")
		add("a", "active only")
		add("s", "sort")
		add("enter", "details")
		add("w", "wake")
		add("b/u", "block/unblock internet")
	case tabWLAN:
		add("space", "toggle")
		add("c", "QR code")
	case tabHome:
		add("space", "switch")
		add("+/-", "temp")
	}
	add("r", "refresh")
	add("?", "help")
	add("q", "quit")
	line := strings.Join(hints, "   ")
	if m.flash != "" {
		st := ui.Bad
		if m.flashOK {
			st = ui.OK
		}
		line = st.Render(m.flash) + "\n" + line
	}
	return line
}

func (m Model) body(w int) string {
	if m.connErr != nil {
		return ui.Card("Connection", ui.Bad.Render(m.connErr.Error())+"\n"+ui.Muted.Render("press r to retry, [ ] to pick another router"), w)
	}
	if m.backend == nil {
		return ui.Muted.Render("connecting to " + m.routerName() + "…")
	}
	switch m.tab {
	case tabOverview:
		return m.overview(w)
	case tabHosts:
		return m.hostsView(w)
	case tabWLAN:
		return m.wlanView(w)
	case tabCalls:
		return m.callsView(w)
	case tabHome:
		return m.homeView(w)
	case tabMesh:
		return m.meshView(w)
	case tabLog:
		return m.logView(w)
	}
	return ""
}

func row(icon, title, hint string) string {
	chip := lipgloss.NewStyle().Background(ui.ColorChip).Foreground(lipgloss.Color(ui.AccentFrom)).Padding(0, 1).Render(icon)
	return chip + " " + ui.Bold.Render(title) + "  " + ui.Muted.Render(hint)
}

func (m Model) overview(w int) string {
	st := m.status
	if st == nil {
		return ui.Muted.Render("loading status…")
	}
	dev := strings.Join([]string{
		row(ui.GlyphRouter, st.Model, "FRITZ!OS "+st.Firmware),
		row(ui.GlyphStatus, "Uptime "+ui.Duration(st.Uptime), "since last reboot"),
	}, "\n")
	if st.UpdateAvailable != "" {
		dev += "\n" + row(ui.GlyphWarning, ui.Warn.Render("Update "+st.UpdateAvailable), "available in the web UI")
	}
	out := ui.Card("Device", dev, w)
	if !st.HasWAN {
		return out + "\n" + ui.Card("Internet", ui.Muted.Render("this box has no WAN (repeater or mesh node)"), w)
	}
	conn := ui.Bad.Render(st.ConnStatus)
	if st.Connected {
		conn = ui.OK.Render("Connected")
	}
	wan := []string{
		row(ui.GlyphStatus, conn, fmt.Sprintf("%s via %s for %s", strings.TrimSuffix(st.ConnectionType, "1"), st.AccessType, ui.Duration(st.ConnUptime))),
		row("v4", orDash(st.ExternalIPv4), "external IPv4"),
	}
	if st.ExternalIPv6 != "" {
		wan = append(wan, row("v6", st.ExternalIPv6, "external IPv6"))
	}
	if st.LastError != "" {
		wan = append(wan, row(ui.GlyphWarning, ui.Bad.Render(st.LastError), "last connection error"))
	}
	wan = append(wan, row("⇅", ui.Up.Render(ui.GlyphUp+" "+ui.Bits(st.MaxUpBits))+"  "+ui.Down.Render(ui.GlyphDown+" "+ui.Bits(st.MaxDownBits)), "line rate"))
	wan = append(wan, row("Σ", ui.Up.Render(ui.GlyphUp+" "+ui.Bytes(st.TotalSent))+"  "+ui.Down.Render(ui.GlyphDown+" "+ui.Bytes(st.TotalReceived)), "transferred"))
	out += "\n" + ui.Card("Internet", strings.Join(wan, "\n"), w)

	spark := max(10, w-30)
	traffic := strings.Join([]string{
		ui.Up.Render(fmt.Sprintf("%s %-14s", ui.GlyphUp, ui.Rate(st.Rates.Up))) + " " + ui.Sparkline(m.upHist, spark, ui.Up),
		ui.Down.Render(fmt.Sprintf("%s %-14s", ui.GlyphDown, ui.Rate(st.Rates.Down))) + " " + ui.Sparkline(m.downHist, spark, ui.Down),
	}, "\n")
	return out + "\n" + ui.Card("Live traffic", traffic, w)
}

// listWindow returns the slice bounds that keep the cursor visible.
func (m Model) listWindow(n, cursor int) (int, int) {
	rows := max(5, m.height-14)
	if m.tab == tabHosts && m.detail {
		rows = max(5, rows-8)
	}
	if n <= rows {
		return 0, n
	}
	start := min(max(0, cursor-rows/2), n-rows)
	return start, start + rows
}

// mark renders a list row. The selected row is a full-width filled bar: the chip background
// is re-applied after every inner style reset so coloured cells (dots, muted text) keep it.
func mark(sel bool, line string, w int) string {
	if !sel {
		return rowIndent + line
	}
	on := sgrPrefix(selRow)
	content := " " + line + " "
	if on != "" {
		content = strings.ReplaceAll(content, "\x1b[0m", "\x1b[0m"+on)
		content = strings.ReplaceAll(content, "\x1b[m", "\x1b[m"+on)
	}
	// Card frame: 2 border cells + 2 padding cells; keep 2 cells of indent on the left.
	inner := w - 4 - 2
	if pad := inner - lipgloss.Width(content); pad > 0 {
		content += strings.Repeat(" ", pad)
	}
	if on == "" {
		return "  " + selRow.Render(content)
	}
	return "  " + on + content + "\x1b[0m"
}

// sgrPrefix returns the escape sequence a style emits before its text ("" without colour support).
func sgrPrefix(st lipgloss.Style) string {
	r := st.Render("x")
	if i := strings.Index(r, "x"); i > 0 {
		return r[:i]
	}
	return ""
}

func (m Model) hostsView(w int) string {
	hosts := m.visibleHosts()
	var lines []string
	if m.filtering || m.filter.Value() != "" {
		lines = append(lines, m.filter.View(), "")
	}
	active := 0
	for _, h := range m.hosts {
		if h.Active {
			active++
		}
	}
	if m.hosts == nil {
		lines = append(lines, ui.Muted.Render("loading…"))
	}
	from, to := m.listWindow(len(hosts), m.cursor[tabHosts])
	for i := from; i < to; i++ {
		h := hosts[i]
		iface := h.Interface
		if h.Guest {
			iface += " · guest"
		}
		line := fmt.Sprintf("%s %-28s %-16s %-18s %s", ui.OnOff(h.Active), trunc(orDash(h.Name), 28), h.IP, h.MAC, ui.Muted.Render(iface))
		lines = append(lines, mark(i == m.cursor[tabHosts], line, w))
	}
	title := fmt.Sprintf("Hosts · %d active / %d known · sort %s", active, len(m.hosts), m.hostSort)
	if m.activeOnly {
		title += " · active only"
	}
	out := ui.Card(title, strings.Join(lines, "\n"), w)
	if m.detail && m.cursor[tabHosts] < len(hosts) {
		out += "\n" + hostDetail(hosts[m.cursor[tabHosts]], w)
	}
	return out
}

func (m Model) wlanView(w int) string {
	if m.wlans == nil {
		return ui.Card("WLAN", ui.Muted.Render("loading…"), w)
	}
	var lines []string
	for i, wl := range m.wlans {
		hint := fmt.Sprintf("%s · channel %s · %s · %d clients", wl.Standard, chanStr(wl.Channel), wl.Beacon, wl.Clients)
		line := ui.Switch(wl.Enabled) + "  " + row(ui.GlyphWLAN, fmt.Sprintf("%-8s %s", wl.Label(), wl.SSID), hint)
		lines = append(lines, mark(i == m.cursor[tabWLAN], line, w))
	}
	return ui.Card("WLAN", strings.Join(lines, "\n"), w)
}

func chanStr(c int) string {
	if c == 0 {
		return "auto"
	}
	return fmt.Sprint(c)
}

func callGlyph(t int) string {
	switch t {
	case fritz.CallIn, fritz.CallActiveIn:
		return ui.Down.Render(ui.GlyphIn)
	case fritz.CallOut, fritz.CallActiveOut:
		return ui.Up.Render(ui.GlyphOut)
	case fritz.CallMissed, fritz.CallRejected:
		return ui.Bad.Render(ui.GlyphMissed)
	}
	return " "
}

func (m Model) callsView(w int) string {
	var live []string
	if len(m.events) == 0 {
		live = append(live, ui.Muted.Render("waiting for call events on :1012 (enable once with #96*5*)"))
	}
	for i := max(0, len(m.events)-5); i < len(m.events); i++ {
		e := m.events[i]
		live = append(live, fmt.Sprintf("%s %-10s %s → %s", ui.Muted.Render(e.Time.Format("15:04:05")), strings.ToLower(e.Kind), orDash(e.Caller), orDash(e.Callee)))
	}
	out := ui.Card("Live", strings.Join(live, "\n"), w)

	var lines []string
	if m.calls == nil {
		lines = append(lines, ui.Muted.Render("loading…"))
	}
	from, to := m.listWindow(len(m.calls), m.cursor[tabCalls])
	for i := from; i < to; i++ {
		c := m.calls[i]
		dur := ""
		if c.Type != fritz.CallMissed {
			dur = fmt.Sprintf("%d:%02d", int(c.Duration.Hours()), int(c.Duration.Minutes())%60)
		}
		line := fmt.Sprintf("%s %s  %-20s %-24s %5s  %s", callGlyph(c.Type), c.Date.Format("2006-01-02 15:04"), trunc(orDash(c.Number), 20), trunc(c.Name, 24), dur, ui.Muted.Render(c.Device))
		lines = append(lines, mark(i == m.cursor[tabCalls], line, w))
	}
	return out + "\n" + ui.Card(fmt.Sprintf("Call list · last 30 days · %d", len(m.calls)), strings.Join(lines, "\n"), w)
}

func (m Model) homeView(w int) string {
	if m.devs == nil {
		return ui.Card("Smart Home", ui.Muted.Render("loading…"), w)
	}
	if len(m.devs) == 0 {
		return ui.Card("Smart Home", ui.Muted.Render("no smart home devices"), w)
	}
	var lines []string
	for i, d := range m.devs {
		var ctrl, hint string
		switch {
		case d.Switch != nil:
			ctrl = ui.Switch(d.Switch.State == "1")
			if d.PowerMeter != nil {
				hint = fmt.Sprintf("%.1f W · %.2f kWh", float64(d.PowerMeter.Power)/1000, float64(d.PowerMeter.Energy)/1000)
			}
		case d.Thermostat != nil:
			ctrl = ui.GradientPill(fmt.Sprintf("%6s", aha.DecodeTemp(d.Thermostat.Target)))
			hint = "now " + aha.DecodeTemp(d.Thermostat.Actual)
			if d.Thermostat.Window == 1 {
				hint += " · window open"
			}
		default:
			ctrl = ui.Pill("  –   ")
			if d.Temperature != nil {
				hint = fmt.Sprintf("%.1f°C", float64(d.Temperature.Celsius)/10)
			}
		}
		if d.Present != 1 {
			hint = ui.Bad.Render("offline") + " " + hint
		}
		line := ctrl + "  " + row(ui.GlyphHome, d.Name, strings.TrimSpace(d.Product+" · "+hint))
		lines = append(lines, mark(i == m.cursor[tabHome], line, w))
	}
	return ui.Card("Smart Home", strings.Join(lines, "\n"), w)
}

func renderQR(w fritz.WLAN, pw string) string {
	q, err := qrcode.New(fritz.WiFiQRPayload(w, pw), qrcode.Medium)
	if err != nil {
		return err.Error()
	}
	return q.ToSmallString(false) + ui.Bold.Render(w.SSID) + "  " + ui.Muted.Render(w.Label())
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func helpText() string {
	return strings.Join([]string{
		ui.Bold.Render("Keys"),
		"",
		"tab / shift+tab, 1–7   switch tab",
		"[ / ]                  previous / next router",
		"j k / ↑ ↓              move",
		"r                      refresh",
		"",
		"Overview   R reconnect · B reboot",
		"Hosts      / filter · a active only · s sort · enter details · w wake-on-LAN · b/u block/unblock internet",
		"WLAN       space toggle · c QR code",
		"Smart Home space switch · + / - target temperature",
		"",
		"q / ctrl+c quit",
	}, "\n")
}
