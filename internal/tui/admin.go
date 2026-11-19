package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mkrajinov/fritzbox/internal/aha"
	"github.com/mkrajinov/fritzbox/internal/fritz"
	"github.com/mkrajinov/fritzbox/internal/ui"
)

const routerPoll = 60 * time.Second

type sysState struct {
	upd       fritz.UpdateInfo
	updErr    error
	dhcp      fritz.DHCP
	dhcpErr   error
	remote    fritz.Remote
	remoteErr error
}

type (
	sfMsg    struct{ p fritz.SecondFactorPrompt }
	phoneMsg struct {
		gen  int
		tams []fritz.TAM
		msgs []fritz.Message
		defl []fritz.Deflection
		err  error
	}
	templatesMsg struct {
		gen int
		ts  []aha.Template
	}
	profilesMsg struct {
		gen int
		ps  []fritz.FilterProfile
	}
	sysMsg struct {
		gen int
		s   sysState
	}
	routerTick  struct{ gen int }
	channelsMsg struct {
		gen int
		w   fritz.WLAN
		ci  fritz.ChannelInfo
		err error
	}
)

func (m *Model) resetAdmin() {
	m.hostInfo, m.profiles = nil, nil
	m.tams, m.msgs, m.defl, m.phoneErr, m.phoneLoad = nil, nil, nil, nil, false
	m.templates, m.sys, m.sf = nil, nil, nil
}

// Second factor: the backend calls the handler from the goroutine running the write; the
// prompt travels through sfCh into the Bubble Tea loop.

func (m Model) installSecondFactor() {
	ch := m.sfCh
	m.backend.SetSecondFactorHandler(func(p fritz.SecondFactorPrompt) {
		select {
		case ch <- p:
		default: // a prompt is already pending; the box handles one confirmation at a time
		}
	})
}

func waitSecondFactor(ch chan fritz.SecondFactorPrompt) tea.Cmd {
	return func() tea.Msg { return sfMsg{<-ch} }
}

func (m Model) secondFactorView() string {
	p := m.sf
	return modalBox.Render(ui.Bold.Render(ui.GlyphWarning+" Confirm on the box") + "\n\n" +
		ui.Muted.Render(p.Router+" wants a confirmation for "+p.Action) + "\n\n" +
		ui.Bold.Render(p.Instructions()) + "\n\n" +
		ui.Muted.Render(fmt.Sprintf("waiting up to %s · esc = cancel", fritz.SecondFactorTimeout)))
}

// Fetches.

func (m Model) fetchPhone() tea.Cmd {
	b, gen := m.backend, m.gen
	return func() tea.Msg {
		c, cancel := ctx()
		defer cancel()
		var out phoneMsg
		out.gen = gen
		var errs []string
		var err error
		if out.tams, err = b.TAMs(c); err != nil {
			errs = append(errs, "answering machine: "+err.Error())
		} else if out.msgs, err = b.Messages(c); err != nil {
			errs = append(errs, "messages: "+err.Error())
		}
		if out.defl, err = b.Deflections(c); err != nil {
			errs = append(errs, "call forwarding: "+err.Error())
		}
		if len(errs) > 0 {
			out.err = fmt.Errorf("%s", strings.Join(errs, " · "))
		}
		return out
	}
}

func (m Model) fetchTemplates() tea.Cmd {
	b, gen := m.backend, m.gen
	return func() tea.Msg {
		c, cancel := ctx()
		defer cancel()
		ts, _ := b.Templates(c) // optional: older boxes or users without smart home rights
		return templatesMsg{gen, ts}
	}
}

func (m Model) fetchProfiles() tea.Cmd {
	if m.profiles != nil || m.backend == nil {
		return nil
	}
	b, gen := m.backend, m.gen
	return func() tea.Msg {
		c, cancel := ctx()
		defer cancel()
		ps, _ := b.FilterProfiles(c)
		if ps == nil {
			ps = []fritz.FilterProfile{}
		}
		return profilesMsg{gen, ps}
	}
}

func (m Model) fetchSystem() tea.Cmd {
	b, gen := m.backend, m.gen
	return func() tea.Msg {
		c, cancel := ctx()
		defer cancel()
		var s sysState
		var wg sync.WaitGroup
		wg.Go(func() { s.upd, s.updErr = b.UpdateInfo(c) })
		wg.Go(func() { s.dhcp, s.dhcpErr = b.DHCP(c) })
		wg.Go(func() { s.remote, s.remoteErr = b.Remote(c) })
		wg.Wait()
		return sysMsg{gen, s}
	}
}

func tickRouters(gen int) tea.Cmd {
	return tick(routerPoll, func(time.Time) tea.Msg { return routerTick{gen} })
}

// adminUpdate handles the messages added for the admin features. ok=false means "not mine".
func (m Model) adminUpdate(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case sfMsg:
		if m.backend != nil {
			p := msg.p
			m.sf, m.confirm, m.input = &p, nil, nil
		}
		return m, listenSecondFactor(m.sfCh), true
	case phoneMsg:
		if msg.gen == m.gen {
			m.tams, m.msgs, m.defl, m.phoneErr, m.phoneLoad = msg.tams, msg.msgs, msg.defl, msg.err, true
			m.clampCursor(tabPhone, m.listLen(tabPhone))
		}
		return m, nil, true
	case templatesMsg:
		if msg.gen == m.gen {
			m.templates = msg.ts
			m.clampCursor(tabHome, m.listLen(tabHome))
		}
		return m, nil, true
	case profilesMsg:
		if msg.gen == m.gen {
			m.profiles = msg.ps
		}
		return m, nil, true
	case sysMsg:
		if msg.gen == m.gen {
			s := msg.s
			m.sys = &s
		}
		return m, nil, true
	case channelsMsg:
		if msg.gen == m.gen {
			m.openChannelInput(msg.w, msg.ci, msg.err)
			return m, textinput.Blink, true
		}
		return m, nil, true
	case routerTick:
		if msg.gen != m.gen {
			return m, nil, true
		}
		return m, tea.Batch(m.fetchAllStatus(true), tickRouters(m.gen)), true
	}
	return m, nil, false
}

// Helpers for the views.

func (m Model) profileName(id string) string {
	for _, p := range m.profiles {
		if p.ID == id {
			return p.Name
		}
	}
	return orDash(id)
}

func onlineTimeText(h fritz.HostInfo) string {
	if h.TimeMax == 0 {
		return "access profile · no time limit"
	}
	return fmt.Sprintf("access profile · %d of %d min used today", h.TimeUsed, h.TimeMax)
}

func onOffText(v bool) string {
	if v {
		return ui.OK.Render(ui.GlyphOn + " on")
	}
	return ui.Muted.Render(ui.GlyphOff + " off")
}

func (m *Model) askDial(number, label string) {
	number = strings.ReplaceAll(strings.TrimSpace(number), " ", "")
	if number == "" {
		m.flash, m.flashOK = "no number to call", false
		return
	}
	m.confirm = &confirmation{
		prompt: fmt.Sprintf("Call %s? The box rings your phone first (dialing help must be on).", label),
		run: m.action("calling "+label+": pick up your phone · x hangs up", m.tab, func(c context.Context, b Backend) error {
			return b.Dial(c, number)
		}),
	}
}

func (m Model) hangup() tea.Cmd {
	return m.action("hung up", m.tab, func(c context.Context, b Backend) error { return b.Hangup(c) })
}

// phoneRows: answering machines, then messages, then forwarding rules.
func (m Model) phoneRow(i int) (kind string, idx int) {
	switch {
	case i < len(m.tams):
		return "tam", i
	case i < len(m.tams)+len(m.msgs):
		return "msg", i - len(m.tams)
	case i < len(m.tams)+len(m.msgs)+len(m.defl):
		return "defl", i - len(m.tams) - len(m.msgs)
	}
	return "", 0
}

// adminKey handles the keys added for the admin features; ok=false falls through to tabKey.
func (m Model) adminKey(key string) (Model, tea.Cmd, bool) {
	cur := m.cursor[m.tab]
	switch m.tab {
	case tabHosts:
		hosts := m.visibleHosts()
		if cur >= len(hosts) || !slices.Contains([]string{"p", "o", "f", "t"}, key) {
			return m, nil, false
		}
		h := hosts[cur]
		hi, known := m.hostInfo[h.IP]
		if !known {
			m.detail = true
			m.flash, m.flashOK = "loading host details… press the key again", true
			return m, tea.Batch(m.fetchAccess(h), m.fetchProfiles()), true
		}
		name := orDash(h.Name)
		switch key {
		case "p":
			on := !hi.Priority
			return m, m.action(fmt.Sprintf("priority %s for %s", map[bool]string{true: "on", false: "off"}[on], name), tabHosts, func(c context.Context, b Backend) error {
				return b.SetPriority(c, h.IP, on)
			}), true
		case "o":
			on := !hi.AutoWOL
			return m, m.action(fmt.Sprintf("auto wake %s for %s", map[bool]string{true: "on", false: "off"}[on], name), tabHosts, func(c context.Context, b Backend) error {
				return b.SetAutoWOL(c, h.MAC, on)
			}), true
		case "f":
			if len(m.profiles) == 0 {
				m.flash, m.flashOK = "no access profiles reported by the box", false
				return m, m.fetchProfiles(), true
			}
			next := m.profiles[0]
			for i, p := range m.profiles {
				if p.ID == hi.ProfileID {
					next = m.profiles[(i+1)%len(m.profiles)]
				}
			}
			m.confirm = &confirmation{
				prompt: fmt.Sprintf("Move %s from profile %s to %s?", name, m.profileName(hi.ProfileID), next.Name),
				run: m.action(fmt.Sprintf("%s now uses profile %s", name, next.Name), tabHosts, func(c context.Context, b Backend) error {
					return b.SetFilterProfile(c, h.IP, next.ID)
				}),
			}
			return m, nil, true
		case "t":
			return m, m.action("extra online time granted to "+name, tabHosts, func(c context.Context, b Backend) error {
				_, _, err := b.AddTicket(c, h.IP)
				return err
			}), true
		}
	case tabWLAN:
		if cur >= len(m.wlans) {
			return m, nil, false
		}
		w := m.wlans[cur]
		switch key {
		case "W":
			return m, m.action(fmt.Sprintf("WPS active on %s (%s): press WPS on the device within 2 minutes", w.Label(), w.SSID), tabWLAN, func(c context.Context, b Backend) error {
				_, err := b.StartWPS(c, w.Index, false)
				return err
			}), true
		case "O":
			anyOn := slices.ContainsFunc(m.wlans, func(x fritz.WLAN) bool { return x.Enabled })
			run := m.action(map[bool]string{true: "all WLANs off", false: "all WLANs on"}[anyOn], tabWLAN, func(c context.Context, b Backend) error {
				return b.SetWLANGlobal(c, !anyOn)
			})
			if !anyOn {
				return m, run, true
			}
			m.confirm = &confirmation{prompt: fmt.Sprintf("Switch off all WLANs of %s? Wireless devices lose the connection; turn them on again with the button on the box or from a wired device.", m.routerName()), run: run}
			return m, nil, true
		case "e":
			m.input = newInput(fmt.Sprintf("SSID for %s", w.Label()), "network name · devices must rejoin · enter = next · esc = cancel", w.SSID,
				func(m *Model, v string) (tea.Cmd, error) {
					if v == "" || v == w.SSID {
						return nil, nil
					}
					m.confirm = &confirmation{
						prompt: fmt.Sprintf("Rename %s from %s to %s? Devices must rejoin the new network.", w.Label(), w.SSID, v),
						run: m.action(fmt.Sprintf("%s is now %s", w.Label(), v), tabWLAN, func(c context.Context, b Backend) error {
							return b.SetSSID(c, w.Index, v)
						}),
					}
					return nil, nil
				})
			m.input.ti.CharLimit = 32
			return m, textinput.Blink, true
		case "n":
			b, gen := m.backend, m.gen
			return m, func() tea.Msg {
				c, cancel := ctx()
				defer cancel()
				ci, err := b.Channels(c, w.Index)
				return channelsMsg{gen, w, ci, err}
			}, true
		case "H":
			hide := !w.Hidden
			verb := map[bool]string{true: "hidden", false: "visible"}[hide]
			m.confirm = &confirmation{
				prompt: fmt.Sprintf("Make %s (%s) %s?", w.Label(), w.SSID, verb),
				run: m.action(fmt.Sprintf("%s (%s) %s", w.Label(), w.SSID, verb), tabWLAN, func(c context.Context, b Backend) error {
					return b.SetHidden(c, w.Index, hide)
				}),
			}
			return m, nil, true
		}
	case tabCalls:
		calls := m.visibleCalls()
		switch key {
		case "c":
			if cur < len(calls) {
				c := calls[cur]
				m.askDial(c.Number, orDash(firstNonEmptyStr(c.Name, c.Number)))
			}
			return m, nil, true
		case "x":
			return m, m.hangup(), true
		case "b":
			if cur < len(calls) {
				m.askBlock(calls[cur])
			}
			return m, nil, true
		}
	case tabContacts:
		cs := m.visibleContacts()
		if mm, cmd, ok := m.contactKey(key, cs, cur); ok {
			return mm, cmd, true
		}
		switch key {
		case "d":
			if cur < len(cs) && len(cs[cur].Numbers) > 0 {
				c := cs[cur]
				m.askDial(c.Numbers[0].Number, c.Name+" ("+c.Numbers[0].Number+")")
			}
			return m, nil, true
		case "x":
			return m, m.hangup(), true
		}
	case tabHome:
		if cur < len(m.devs) || (key != " " && key != "enter") {
			return m, nil, false
		}
		if i := cur - len(m.devs); i < len(m.templates) {
			t := m.templates[i]
			m.confirm = &confirmation{
				prompt: fmt.Sprintf("Run the template %s?", t.Name),
				run: m.action("applied "+t.Name, tabHome, func(c context.Context, b Backend) error {
					return b.ApplyTemplate(c, t.ID)
				}),
			}
		}
		return m, nil, true
	case tabPhone:
		return m.phoneKey(key, cur)
	case tabSystem:
		return m.systemKey(key)
	}
	return m, nil, false
}

func firstNonEmptyStr(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func joinInts(v []int) string {
	s := make([]string, len(v))
	for i, x := range v {
		s[i] = strconv.Itoa(x)
	}
	return strings.Join(s, " ")
}

func (m Model) phoneKey(key string, cur int) (Model, tea.Cmd, bool) {
	kind, i := m.phoneRow(cur)
	switch key {
	case "x":
		if kind == "msg" {
			msg := m.msgs[i]
			m.confirm = &confirmation{
				prompt: fmt.Sprintf("Delete the message from %s (%s)?", orDash(firstNonEmptyStr(msg.Name, msg.Number)), msg.Date.Format("02.01. 15:04")),
				run: m.action("message deleted", tabPhone, func(c context.Context, b Backend) error {
					return b.DeleteMessage(c, msg.TAM, msg.Index)
				}),
			}
			return m, nil, true
		}
		return m, m.hangup(), true
	case "c":
		if kind == "msg" {
			msg := m.msgs[i]
			m.askDial(msg.Number, orDash(firstNonEmptyStr(msg.Name, msg.Number)))
		}
		return m, nil, true
	case "p":
		if kind == "msg" {
			cmd := m.playMessage(m.msgs[i])
			return m, cmd, true
		}
		return m, nil, true
	case " ", "enter":
		switch kind {
		case "tam":
			t := m.tams[i]
			return m, m.action(fmt.Sprintf("%s %s", t.Name, map[bool]string{true: "off", false: "on"}[t.Enabled]), tabPhone, func(c context.Context, b Backend) error {
				return b.SetTAM(c, t.Index, !t.Enabled)
			}), true
		case "msg":
			msg := m.msgs[i]
			return m, m.action(map[bool]string{true: "marked as heard", false: "marked as new"}[msg.New], tabPhone, func(c context.Context, b Backend) error {
				return b.MarkMessage(c, msg.TAM, msg.Index, msg.New)
			}), true
		case "defl":
			d := m.defl[i]
			return m, m.action(fmt.Sprintf("forwarding %s: %s", map[bool]string{true: "off", false: "on"}[d.Enabled], d.Describe()), tabPhone, func(c context.Context, b Backend) error {
				return b.SetDeflection(c, d.ID, !d.Enabled)
			}), true
		}
		return m, nil, true
	}
	return m, nil, false
}

func (m Model) systemKey(key string) (Model, tea.Cmd, bool) {
	s := m.sys
	if s == nil {
		return m, nil, false
	}
	switch key {
	case "u":
		b, gen := m.backend, m.gen
		m.flash, m.flashOK = "checking for firmware…", true
		return m, func() tea.Msg {
			c, cancel := ctx()
			defer cancel()
			return actionMsg{gen: gen, text: "firmware check done", err: b.CheckUpdate(c), refresh: tabSystem}
		}, true
	case "I":
		if !s.upd.Available {
			m.flash, m.flashOK = "no firmware update available (u checks)", false
			return m, nil, true
		}
		m.confirm = &confirmation{
			prompt: fmt.Sprintf("Install FRITZ!OS %s on %s? Internet and phones are down for several minutes.", s.upd.Version, m.routerName()),
			run: m.action("update started; the box reboots when it is done", tabSystem, func(c context.Context, b Backend) error {
				_, err := b.DoUpdate(c)
				return err
			}),
		}
		return m, nil, true
	case "a":
		modes := fritz.AutoUpdateModes
		next := modes[(slices.Index(modes, s.upd.AutoMode)+1)%len(modes)]
		return m, m.action("auto update: "+next, tabSystem, func(c context.Context, b Backend) error {
			return b.SetAutoUpdate(c, next)
		}), true
	case "e":
		m.input = newInput("DHCP range", "first and last address, separated by a space · enter = next · esc = cancel", s.dhcp.Min+" "+s.dhcp.Max,
			func(m *Model, v string) (tea.Cmd, error) {
				f := strings.Fields(strings.ReplaceAll(v, "-", " "))
				if len(f) != 2 {
					return nil, fmt.Errorf("two addresses, e.g. 192.168.178.20 192.168.178.200")
				}
				m.confirm = &confirmation{
					prompt: fmt.Sprintf("Set the DHCP range to %s – %s? Devices get new addresses on their next lease.", f[0], f[1]),
					run: m.action(fmt.Sprintf("DHCP range %s – %s", f[0], f[1]), tabSystem, func(c context.Context, b Backend) error {
						return b.SetDHCPRange(c, f[0], f[1])
					}),
				}
				return nil, nil
			})
		return m, textinput.Blink, true
	case "H":
		on := !s.remote.HTTPSEnabled
		run := m.action("remote HTTPS "+map[bool]string{true: "on", false: "off"}[on], tabSystem, func(c context.Context, b Backend) error {
			_, err := b.SetRemoteHTTPS(c, on)
			return err
		})
		if !on {
			return m, run, true
		}
		m.confirm = &confirmation{prompt: fmt.Sprintf("Make the %s web UI reachable from the internet over HTTPS?", m.routerName()), run: run}
		return m, nil, true
	case "L":
		on := !s.remote.LetsEncrypt
		return m, m.action("Let's Encrypt "+map[bool]string{true: "on", false: "off"}[on], tabSystem, func(c context.Context, b Backend) error {
			return b.SetLetsEncrypt(c, on)
		}), true
	case "M":
		on := !s.remote.MyFritzEnabled
		return m, m.action("MyFRITZ "+map[bool]string{true: "on", false: "off"}[on], tabSystem, func(c context.Context, b Backend) error {
			return b.SetMyFritz(c, on)
		}), true
	case "b":
		name := m.routerName()
		m.input = newInput("Backup password", "encrypts the export; you need it to restore in the web UI · enter = save · esc = cancel", "",
			func(m *Model, v string) (tea.Cmd, error) {
				if len(v) < 4 {
					return nil, fmt.Errorf("at least 4 characters")
				}
				path := fmt.Sprintf("fritzbox-%s-%s.export", name, time.Now().Format("20060102-1504"))
				b, gen := m.backend, m.gen
				return func() tea.Msg {
					c, cancel := ctx()
					defer cancel()
					data, err := b.Backup(c, v)
					if err == nil {
						err = os.WriteFile(path, data, 0o600)
					}
					abs, _ := filepath.Abs(path)
					return actionMsg{gen: gen, text: "backup saved to " + abs, err: err, refresh: tabSystem}
				}, nil
			})
		m.input.ti.EchoMode = textinput.EchoPassword
		return m, textinput.Blink, true
	}
	return m, nil, false
}

// Views.

func (m Model) phoneView(w int) string {
	if !m.phoneLoad {
		return ui.Card("Phone", ui.Muted.Render("loading…"), w)
	}
	var lines []string
	row := 0
	add := func(line string) {
		lines = append(lines, mark(row == m.cursor[tabPhone], line, w))
		row++
	}
	if m.phoneErr != nil {
		lines = append(lines, ui.Bad.Render(trunc(m.phoneErr.Error(), w-8)))
	}
	newCount := 0
	for _, msg := range m.msgs {
		if msg.New {
			newCount++
		}
	}
	for _, t := range m.tams {
		add(ui.Switch(t.Enabled) + "  " + ui.Bold.Render(t.Name) + "  " + ui.Muted.Render(fmt.Sprintf("answering machine %d", t.Index)))
	}
	if len(m.tams) > 0 && len(m.msgs) == 0 {
		lines = append(lines, rowIndent+ui.Muted.Render("no messages"))
	}
	for _, msg := range m.msgs {
		dot := ui.Muted.Render(ui.GlyphOff)
		if msg.New {
			dot = ui.Warn.Render(ui.GlyphOn)
		}
		add(fmt.Sprintf("%s %s  %-20s %-24s %s", dot, msg.Date.Format("2006-01-02 15:04"), trunc(orDash(msg.Number), 20), trunc(msg.Name, 24), ui.Muted.Render(msg.Duration)))
	}
	if len(m.defl) > 0 {
		lines = append(lines, "", rowIndent+ui.Muted.Render("Call forwarding"))
	}
	for _, d := range m.defl {
		add(ui.Switch(d.Enabled) + "  " + d.Describe())
	}
	title := fmt.Sprintf("Phone · %d messages · %d new", len(m.msgs), newCount)
	return ui.Card(title, strings.Join(lines, "\n"), w)
}

func (m Model) templateLines(w int) []string {
	var lines []string
	for i, t := range m.templates {
		hint := "template"
		if n := len(t.Devices); n > 0 {
			hint = fmt.Sprintf("template · %d devices", n)
		}
		lines = append(lines, mark(len(m.devs)+i == m.cursor[tabHome], ui.Pill(" ▶ ")+"  "+row(ui.GlyphHome, t.Name, hint), w))
	}
	return lines
}

func (m Model) systemView(w int) string {
	s := m.sys
	if s == nil {
		return ui.Card("System", ui.Muted.Render("loading…"), w)
	}
	errRow := func(err error) string { return ui.Bad.Render(trunc(err.Error(), w-8)) }

	var fw []string
	if s.updErr != nil {
		fw = append(fw, errRow(s.updErr))
	} else {
		u := s.upd
		avail := ui.Muted.Render("up to date")
		if u.Available {
			avail = ui.Warn.Render("update " + orDash(u.Version) + " available")
		}
		fw = append(fw,
			row(ui.GlyphRouter, "FRITZ!OS "+orDash(firstNonEmptyStr(u.Current, m.firmware())), avail),
			row("↻", "auto update: "+orDash(u.AutoMode), "a cycles notify → important → all"),
		)
	}
	out := ui.Card("Firmware · u check · I install · a auto mode", strings.Join(fw, "\n"), w)

	var net []string
	if s.dhcpErr != nil {
		net = append(net, errRow(s.dhcpErr))
	} else {
		d := s.dhcp
		net = append(net,
			row("ip", d.Min+" – "+d.Max, "DHCP range · server "+map[bool]string{true: "on", false: "off"}[d.Enabled]),
			row("⌂", d.Router+" / "+d.Subnet, "box address · domain "+orDash(d.Domain)),
		)
	}
	out += "\n" + ui.Card("Home network · e edit DHCP range", strings.Join(net, "\n"), w)

	var rem []string
	if s.remoteErr != nil {
		rem = append(rem, errRow(s.remoteErr))
	} else {
		r := s.remote
		https := "remote HTTPS"
		if r.HTTPSEnabled {
			https += " · port " + r.HTTPSPort
		}
		ddns := "DynDNS"
		if r.DDNSEnabled {
			ddns += fmt.Sprintf(" · %s via %s", r.DDNSDomain, r.DDNSProvider)
		}
		mf := "MyFRITZ"
		if r.MyFritzName != "" {
			mf += " · " + r.MyFritzName
		}
		rem = append(rem,
			ui.Switch(r.HTTPSEnabled)+"  "+row("H", https, "web UI from the internet"),
			ui.Switch(r.LetsEncrypt)+"  "+row("L", "Let's Encrypt", orDash(r.LEState)),
			ui.Switch(r.MyFritzEnabled)+"  "+row("M", mf, orDash(r.MyFritzState)),
			ui.Switch(r.DDNSEnabled)+"  "+row("·", ddns, "change in the web UI"),
		)
	}
	out += "\n" + ui.Card("Remote access · H https · L Let's Encrypt · M MyFRITZ", strings.Join(rem, "\n"), w)
	out += "\n" + ui.Card("Backup", row("⇩", "b  save an encrypted configuration export", "written to the current directory"), w)
	return out
}

func (m Model) firmware() string {
	if m.status != nil {
		return m.status.Firmware
	}
	return ""
}

func (m *Model) openChannelInput(w fritz.WLAN, ci fritz.ChannelInfo, err error) {
	hint := "channel number or auto · enter = save · esc = cancel"
	if err == nil && len(ci.Possible) > 0 {
		hint = fmt.Sprintf("possible: %s or auto · enter = save · esc = cancel", joinInts(ci.Possible))
	}
	cur := chanStr(w.Channel)
	m.input = newInput(fmt.Sprintf("Channel for %s (%s)", w.Label(), w.SSID), hint, cur,
		func(m *Model, v string) (tea.Cmd, error) {
			ch := 0
			if v != "auto" {
				n, err := strconv.Atoi(v)
				if err != nil || n <= 0 {
					return nil, fmt.Errorf("a channel number or auto")
				}
				if len(ci.Possible) > 0 && !slices.Contains(ci.Possible, n) {
					return nil, fmt.Errorf("channel %d is not possible here", n)
				}
				ch = n
			}
			return m.action(fmt.Sprintf("%s channel %s", w.Label(), v), tabWLAN, func(c context.Context, b Backend) error {
				return b.SetChannel(c, w.Index, ch)
			}), nil
		})
}
