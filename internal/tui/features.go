package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mkrajinov/fritzbox/internal/aha"
	"github.com/mkrajinov/fritzbox/internal/fritz"
	"github.com/mkrajinov/fritzbox/internal/ui"
)

// inputPrompt is a one-line text modal (rename a host, set a WLAN password).
type inputPrompt struct {
	title  string
	hint   string
	ti     textinput.Model
	submit func(m *Model, value string) (tea.Cmd, error)
}

func newInput(title, hint, value string, submit func(*Model, string) (tea.Cmd, error)) *inputPrompt {
	ti := newTextInput()
	ti.Prompt = "› "
	ti.CharLimit = 63
	ti.Width = 48
	ti.SetValue(value)
	ti.CursorEnd()
	ti.Focus()
	return &inputPrompt{title: title, hint: hint, ti: ti, submit: submit}
}

func (m Model) inputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.input = nil
		m.flash, m.flashOK = "cancelled", true
		return m, nil
	case "enter":
		in := m.input
		cmd, err := in.submit(&m, strings.TrimSpace(in.ti.Value()))
		if err != nil {
			in.hint = ui.Bad.Render(err.Error())
			return m, nil
		}
		m.input = nil
		return m, cmd
	}
	var cmd tea.Cmd
	m.input.ti, cmd = m.input.ti.Update(k)
	return m, cmd
}

// More messages.
type (
	contactsMsg struct {
		gen      int
		books    []fritz.Phonebook
		book     int
		contacts []fritz.Contact
		err      error
	}
	accessMsg struct {
		gen    int
		ip     string
		access string
		info   *fritz.HostInfo
		err    error
	}
	pwChangedMsg struct {
		gen int
		w   fritz.WLAN
		pw  string
		err error
	}
	allStatusMsg struct {
		gen        int
		rows       []routerRow
		background bool // header refresh, not the A modal
	}
)

type routerRow struct {
	name string
	st   fritz.Status
	err  error
}

// Calls tab filters, mirroring `fb calls --missed|--in|--out --days N`.
var (
	callFilters = []string{"all", "missed", "in", "out"}
	callDays    = []int{30, 90, 0, 7} // 0 = everything the box keeps
)

func callMatches(filter string, t int) bool {
	switch filter {
	case "missed":
		return t == fritz.CallMissed || t == fritz.CallRejected
	case "in":
		return t == fritz.CallIn || t == fritz.CallActiveIn
	case "out":
		return t == fritz.CallOut || t == fritz.CallActiveOut
	}
	return true
}

func (m Model) visibleCalls() []fritz.Call {
	f := callFilters[m.callFilter]
	if f == "all" {
		return m.calls
	}
	var out []fritz.Call
	for _, c := range m.calls {
		if callMatches(f, c.Type) {
			out = append(out, c)
		}
	}
	return out
}

func daysLabel(d int) string {
	if d == 0 {
		return "all"
	}
	return fmt.Sprintf("last %d days", d)
}

var logGroups = append([]string{""}, fritz.LogGroups...)

func (m Model) visibleContacts() []fritz.Contact {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if q == "" {
		return m.contacts
	}
	var out []fritz.Contact
	for _, c := range m.contacts {
		hit := strings.Contains(strings.ToLower(c.Name), q)
		for _, n := range c.Numbers {
			hit = hit || strings.Contains(n.Number, q)
		}
		if hit {
			out = append(out, c)
		}
	}
	return out
}

func (m Model) fetchContacts(book int) tea.Cmd {
	b, gen := m.backend, m.gen
	return func() tea.Msg {
		c, cancel := ctx()
		defer cancel()
		books, err := b.Phonebooks(c)
		if err != nil || len(books) == 0 {
			return contactsMsg{gen: gen, books: books, err: err}
		}
		book = max(0, min(book, len(books)-1))
		_, contacts, err := b.Contacts(c, books[book].ID)
		return contactsMsg{gen: gen, books: books, book: book, contacts: contacts, err: err}
	}
}

func (m Model) fetchAccess(h fritz.Host) tea.Cmd {
	if h.IP == "" || m.backend == nil {
		return nil
	}
	b, gen := m.backend, m.gen
	return func() tea.Msg {
		c, cancel := ctx()
		defer cancel()
		if hi, err := b.HostInfo(c, h.IP); err == nil {
			return accessMsg{gen, h.IP, hi.WANAccess, &hi, nil}
		}
		acc, _, err := b.WANAccess(c, h.IP)
		return accessMsg{gen, h.IP, acc, nil, err}
	}
}

// detailAccess refreshes the internet-access line of the details panel for the selected host.
func (m Model) detailAccess() tea.Cmd {
	if !m.detail || m.tab != tabHosts {
		return nil
	}
	hosts := m.visibleHosts()
	if cur := m.cursor[tabHosts]; cur < len(hosts) {
		return m.fetchAccess(hosts[cur])
	}
	return nil
}

// fetchAllStatus polls every router profile at once, like `fb status --all`.
func (m Model) fetchAllStatus(background bool) tea.Cmd {
	names, conn, gen := slices.Clone(m.opt.Routers), m.opt.Connect, m.gen
	return func() tea.Msg {
		rows := make([]routerRow, len(names))
		var wg sync.WaitGroup
		for i, n := range names {
			rows[i].name = n
			wg.Go(func() {
				c, cancel := ctx()
				defer cancel()
				b, _, err := conn(c, n)
				if err == nil {
					rows[i].st, err = b.Status(c)
					if err != nil && rows[i].st.Model != "" {
						err = nil
					}
				}
				rows[i].err = err
			})
		}
		wg.Wait()
		return allStatusMsg{gen, rows, background}
	}
}

func renderAllStatus(rows []routerRow) string {
	lines := []string{ui.Bold.Render("All routers"), ""}
	for _, r := range rows {
		if r.err != nil {
			lines = append(lines, fmt.Sprintf("%s %-12s %s", ui.Bad.Render(ui.GlyphOff), r.name, ui.Bad.Render(trunc(r.err.Error(), 60))))
			continue
		}
		st := r.st
		wan := ui.Muted.Render("no WAN")
		if st.HasWAN {
			wan = ui.Bad.Render("offline")
			if st.Connected {
				wan = ui.OK.Render("online")
			}
		}
		lines = append(lines, fmt.Sprintf("%s %-12s %-22s %-8s %s  %-16s %s %s", ui.OK.Render(ui.GlyphOn), r.name,
			trunc(st.Model, 22), st.Firmware, wan, orDash(st.ExternalIPv4),
			ui.Up.Render(ui.GlyphUp+" "+ui.Rate(st.Rates.Up)), ui.Down.Render(ui.GlyphDown+" "+ui.Rate(st.Rates.Down))))
	}
	return strings.Join(lines, "\n")
}

// WLAN password: show, generate + edit, confirm, set, then show the join QR code.

func (m Model) showWLANPassword(w fritz.WLAN) tea.Cmd {
	gen, b := m.gen, m.backend
	return func() tea.Msg {
		c, cancel := ctx()
		defer cancel()
		pw, err := b.WLANPassword(c, w.Index)
		if err != nil {
			return qrMsg{gen: gen, err: err}
		}
		return qrMsg{gen: gen, text: ui.Bold.Render(w.Label()+" · "+w.SSID) + "\n\n" + row("⚿", pw, "password")}
	}
}

func (m *Model) changeWLANPassword(w fritz.WLAN) error {
	pw, err := fritz.GeneratePassword(16)
	if err != nil {
		return err
	}
	m.input = newInput(
		fmt.Sprintf("New password for %s (%s)", w.Label(), w.SSID),
		"generated for you; edit or type your own (8–63 characters) · enter = next · esc = cancel",
		pw,
		func(m *Model, v string) (tea.Cmd, error) {
			if l := len(v); l < 8 || l > 63 {
				return nil, fmt.Errorf("8–63 characters, got %d", l)
			}
			for _, r := range v {
				if r < 0x20 || r > 0x7e {
					return nil, fmt.Errorf("only printable ASCII characters")
				}
			}
			b, gen := m.backend, m.gen
			m.confirm = &confirmation{
				prompt: fmt.Sprintf("Change the password of %s (%s)? Connected devices drop and must rejoin.", w.Label(), w.SSID),
				run: func() tea.Msg {
					c, cancel := ctx()
					defer cancel()
					return pwChangedMsg{gen, w, v, b.SetWLANPassword(c, w.Index, v)}
				},
			}
			return nil, nil
		})
	return nil
}

func (m *Model) renameHost(h fritz.Host) {
	m.input = newInput(
		fmt.Sprintf("Rename %s (%s)", orDash(h.Name), h.MAC),
		"name shown in the box and in fb · enter = save · esc = cancel",
		h.Name,
		func(m *Model, v string) (tea.Cmd, error) {
			if v == "" {
				return nil, fmt.Errorf("name must not be empty")
			}
			if v == h.Name {
				return nil, nil
			}
			return m.action(fmt.Sprintf("%s renamed to %s", h.MAC, v), tabHosts, func(c context.Context, b Backend) error {
				return b.RenameHost(c, h.MAC, v)
			}), nil
		})
}

func (m Model) setThermostat(d aha.Device, v int) tea.Cmd {
	return m.action(fmt.Sprintf("%s target %s", d.Name, aha.DecodeTemp(v)), tabHome, func(c context.Context, b Backend) error {
		return b.SetThermostatRaw(c, d.AIN, v)
	})
}

func accessText(a string) string {
	switch a {
	case "":
		return ui.Muted.Render("checking…")
	case "granted":
		return ui.OK.Render(ui.GlyphOn + " allowed")
	case "denied":
		return ui.Bad.Render(ui.GlyphOff + " blocked")
	}
	return ui.Warn.Render(a)
}

func (m Model) contactsView(w int) string {
	var lines []string
	if m.filtering || m.filter.Value() != "" {
		lines = append(lines, m.filter.View(), "")
	}
	if m.contacts == nil && m.books == nil {
		return ui.Card("Contacts", ui.Muted.Render("loading…"), w)
	}
	cs := m.visibleContacts()
	if len(cs) == 0 {
		lines = append(lines, ui.Muted.Render("no contacts"))
	}
	from, to := m.listWindow(len(cs), m.cursor[tabContacts])
	numW := max(20, w-44)
	for i := from; i < to; i++ {
		c := cs[i]
		var nums []string
		for _, n := range c.Numbers {
			nums = append(nums, n.Number+" "+ui.Muted.Render(n.Type))
		}
		line := fmt.Sprintf("%-30s %s", trunc(orDash(c.Name), 30), trunc(strings.Join(nums, "  "), numW+len(c.Numbers)*12))
		lines = append(lines, mark(i == m.cursor[tabContacts], line, w))
	}
	title := "Contacts"
	if m.book < len(m.books) {
		title = fmt.Sprintf("Contacts · %s (%d of %d) · %d entries", m.books[m.book].Name, m.book+1, len(m.books), len(m.contacts))
	}
	return ui.Card(title, strings.Join(lines, "\n"), w)
}

func (m Model) inputView() string {
	in := m.input
	return modalBox.Render(ui.Bold.Render(in.title) + "\n\n" + in.ti.View() + "\n\n" + ui.Muted.Render(in.hint))
}
