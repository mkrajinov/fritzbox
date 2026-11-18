package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mkrajinovic/fbctl/internal/aha"
	"github.com/mkrajinovic/fbctl/internal/callmon"
	"github.com/mkrajinovic/fbctl/internal/fritz"
	"github.com/mkrajinovic/fbctl/internal/tr064"
	"github.com/mkrajinovic/fbctl/internal/ui"
	"github.com/mkrajinovic/fbctl/internal/watch"
)

type tab int

const (
	tabOverview tab = iota
	tabHosts
	tabMesh
	tabWLAN
	tabCalls
	tabContacts
	tabPhone
	tabHome
	tabLog
	tabSystem
	tabCount
)

func (t tab) title() string {
	return [...]string{
		ui.GlyphStatus + " Overview", ui.GlyphHosts + " Hosts", ui.GlyphMesh + " Mesh", ui.GlyphWLAN + " WLAN",
		ui.GlyphCalls + " Calls", ui.GlyphContacts + " Contacts", ui.GlyphPhone + " Phone", ui.GlyphHome + " Smart Home", ui.GlyphLog + " Log", ui.GlyphSystem + " System",
	}[t]
}

const (
	ratePoll     = 3 * time.Second
	statusPoll   = 30 * time.Second
	historyLen   = 120
	callmonQueue = 32
	reqTimeout   = 15 * time.Second
)

// Options configures the TUI.
type Options struct {
	Routers   []string
	Current   string
	Connect   Connector
	NoCallmon bool   // tests
	StateDir  string // where fb watch keeps presence data; empty = the default (tests override)
}

// Model is the root Bubble Tea model.
type Model struct {
	opt    Options
	router int
	gen    int // bumps on router switch; stale messages are dropped
	tab    tab
	width  int
	height int

	backend Backend
	host    string
	connErr error

	status     *fritz.Status
	upHist     []int64 // oldest first
	downHist   []int64
	rateBusy   bool
	statusBusy bool

	hosts  []fritz.Host
	wlans  []fritz.WLAN
	calls  []fritz.Call
	devs   []aha.Device
	events []callmon.Event
	mesh   []fritz.MeshNode
	logEvs []fritz.LogEvent

	activeOnly bool   // hosts: hide inactive
	hostSort   string // ip, name, active
	detail     bool   // hosts: show details of the selected host
	access     map[string]string
	wifi       map[string]fritz.WLANClient // Wi-Fi signal by MAC (box radios only)
	presence   *watch.PresenceFile         // from fb watch
	hostInfo   map[string]fritz.HostInfo
	profiles   []fritz.FilterProfile

	tams      []fritz.TAM
	msgs      []fritz.Message
	defl      []fritz.Deflection
	phoneErr  error
	phoneLoad bool
	templates []aha.Template
	sys       *sysState
	sfCh      chan fritz.SecondFactorPrompt
	sf        *fritz.SecondFactorPrompt
	routerUp  map[string]bool

	callFilter int // index into callFilters
	callDays   int // index into callDays
	logGroup   int // index into logGroups

	books    []fritz.Phonebook
	book     int
	contacts []fritz.Contact

	input *inputPrompt

	cursor    [tabCount]int
	filter    textinput.Model
	filtering bool

	confirm *confirmation
	qr      string
	flash   string
	flashOK bool
	help    bool

	cancelMon context.CancelFunc
	monEvents chan callmon.Event
	monErrs   chan error
}

type confirmation struct {
	prompt string
	run    tea.Cmd
}

// New creates the model.
func New(o Options) Model {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "filter"
	idx := max(slices.Index(o.Routers, o.Current), 0)
	return Model{opt: o, router: idx, filter: ti, hostSort: "ip", sfCh: make(chan fritz.SecondFactorPrompt, 1)}
}

// Messages. Every async result carries the generation it was issued for.
type (
	connectedMsg struct {
		gen     int
		backend Backend
		host    string
		err     error
	}
	statusMsg struct {
		gen int
		st  fritz.Status
		err error
	}
	ratesMsg struct {
		gen int
		r   fritz.Rates
		err error
	}
	hostsMsg struct {
		gen      int
		hosts    []fritz.Host
		presence *watch.PresenceFile // nil without fb watch data
		err      error
	}
	// wifiMsg arrives separately so a slow device-list download never holds back the hosts.
	wifiMsg struct {
		gen  int
		wifi map[string]fritz.WLANClient // by MAC; nil when the box gave none
	}
	wlansMsg struct {
		gen int
		ws  []fritz.WLAN
		err error
	}
	callsMsg struct {
		gen   int
		calls []fritz.Call
		err   error
	}
	devsMsg struct {
		gen  int
		devs []aha.Device
		err  error
	}
	meshMsg struct {
		gen   int
		nodes []fritz.MeshNode
		err   error
	}
	logMsg struct {
		gen int
		evs []fritz.LogEvent
		err error
	}
	actionMsg struct {
		gen     int
		text    string
		err     error
		refresh tab
	}
	qrMsg struct {
		gen  int
		text string
		err  error
	}
	monEventMsg struct {
		gen int
		ev  callmon.Event
	}
	monErrMsg struct {
		gen int
		err error
	}
	rateTick   struct{ gen int }
	statusTick struct{ gen int }
)

func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), reqTimeout)
}

// Init connects to the initial router.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink, m.connect(), waitSecondFactor(m.sfCh)}
	if len(m.opt.Routers) > 1 {
		cmds = append(cmds, m.fetchAllStatus(true), tickRouters(m.gen))
	}
	return tea.Batch(cmds...)
}

// presencePath is where `fb watch` keeps the current router's presence data.
func (m Model) presencePath() string {
	if m.opt.StateDir != "" {
		return watch.KnownPath(m.opt.StateDir, m.routerName())
	}
	p, err := watch.DefaultKnownPath(m.routerName())
	if err != nil {
		return ""
	}
	return p
}

func (m Model) routerName() string {
	if len(m.opt.Routers) == 0 {
		return ""
	}
	return m.opt.Routers[m.router]
}

func (m Model) connect() tea.Cmd {
	gen, name, conn := m.gen, m.routerName(), m.opt.Connect
	return func() tea.Msg {
		c, cancel := ctx()
		defer cancel()
		b, host, err := conn(c, name)
		return connectedMsg{gen: gen, backend: b, host: host, err: err}
	}
}

func (m *Model) switchRouter(delta int) tea.Cmd {
	if len(m.opt.Routers) < 2 {
		return nil
	}
	m.stopMonitor()
	m.router = (m.router + delta + len(m.opt.Routers)) % len(m.opt.Routers)
	m.gen++
	m.backend, m.connErr, m.status = nil, nil, nil
	m.upHist, m.downHist = nil, nil
	m.hosts, m.wlans, m.calls, m.devs, m.events = nil, nil, nil, nil, nil
	m.mesh, m.logEvs, m.detail, m.access = nil, nil, false, nil
	m.wifi, m.presence = nil, nil
	m.resetAdmin()
	m.books, m.book, m.contacts, m.input = nil, 0, nil, nil
	m.rateBusy, m.statusBusy = false, false
	m.cursor = [tabCount]int{}
	m.flash = ""
	return m.connect()
}

// Fetch commands.

func (m Model) fetchStatus() tea.Cmd {
	b, gen := m.backend, m.gen
	return func() tea.Msg {
		c, cancel := ctx()
		defer cancel()
		st, err := b.Status(c)
		return statusMsg{gen, st, err}
	}
}

func (m Model) fetchRates() tea.Cmd {
	b, gen := m.backend, m.gen
	return func() tea.Msg {
		c, cancel := ctx()
		defer cancel()
		r, err := b.Rates(c)
		return ratesMsg{gen, r, err}
	}
}

func (m Model) fetchTab(t tab) tea.Cmd {
	b, gen := m.backend, m.gen
	if b == nil {
		return nil
	}
	switch t {
	case tabOverview:
		return m.fetchStatus()
	case tabHosts:
		state := m.presencePath()
		hosts := func() tea.Msg {
			c, cancel := ctx()
			defer cancel()
			msg := hostsMsg{gen: gen}
			if state != "" {
				if p, err := watch.LoadPresence(state); err == nil {
					msg.presence = &p
				}
			}
			msg.hosts, msg.err = b.Hosts(c)
			return msg
		}
		wifi := func() tea.Msg {
			c, cancel := ctx()
			defer cancel()
			msg := wifiMsg{gen: gen}
			if cs, err := b.WLANAssociations(c); err == nil && len(cs) > 0 {
				msg.wifi = make(map[string]fritz.WLANClient, len(cs))
				for _, cl := range cs {
					msg.wifi[strings.ToUpper(cl.MAC)] = cl
				}
			}
			return msg
		}
		return tea.Batch(hosts, wifi)
	case tabMesh:
		return func() tea.Msg {
			c, cancel := ctx()
			defer cancel()
			n, err := b.Mesh(c)
			return meshMsg{gen, n, err}
		}
	case tabLog:
		group := logGroups[m.logGroup]
		return func() tea.Msg {
			c, cancel := ctx()
			defer cancel()
			e, err := b.DeviceLog(c, group)
			return logMsg{gen, e, err}
		}
	case tabWLAN:
		return func() tea.Msg {
			c, cancel := ctx()
			defer cancel()
			w, err := b.WLANs(c)
			return wlansMsg{gen, w, err}
		}
	case tabContacts:
		return m.fetchContacts(m.book)
	case tabPhone:
		return m.fetchPhone()
	case tabSystem:
		return m.fetchSystem()
	case tabCalls:
		days := callDays[m.callDays]
		return func() tea.Msg {
			c, cancel := ctx()
			defer cancel()
			calls, err := b.Calls(c, days)
			return callsMsg{gen, calls, err}
		}
	case tabHome:
		return tea.Batch(func() tea.Msg {
			c, cancel := ctx()
			defer cancel()
			d, err := b.SmartDevices(c)
			return devsMsg{gen, d, err}
		}, m.fetchTemplates())
	}
	return nil
}

func tickRates(gen int) tea.Cmd {
	return tea.Tick(ratePoll, func(time.Time) tea.Msg { return rateTick{gen} })
}

func tickStatus(gen int) tea.Cmd {
	return tea.Tick(statusPoll, func(time.Time) tea.Msg { return statusTick{gen} })
}

func (m Model) action(desc string, refresh tab, f func(context.Context, Backend) error) tea.Cmd {
	b, gen := m.backend, m.gen
	return func() tea.Msg {
		c, cancel := ctx()
		defer cancel()
		return actionMsg{gen: gen, text: desc, err: f(c, b), refresh: refresh}
	}
}

// Call monitor plumbing: a goroutine feeds channels, a tea.Cmd waits on them.

func (m *Model) startMonitor() tea.Cmd {
	if m.opt.NoCallmon || m.host == "" {
		return nil
	}
	c, cancel := context.WithCancel(context.Background())
	m.cancelMon = cancel
	m.monEvents = make(chan callmon.Event, callmonQueue)
	m.monErrs = make(chan error, 4)
	go callmon.Watch(c, m.host, m.monEvents, m.monErrs)
	return m.waitMonitor()
}

func (m Model) waitMonitor() tea.Cmd {
	ev, errs, gen := m.monEvents, m.monErrs, m.gen
	if ev == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case e := <-ev:
			return monEventMsg{gen, e}
		case err := <-errs:
			return monErrMsg{gen, err}
		}
	}
}

func (m *Model) stopMonitor() {
	if m.cancelMon != nil {
		m.cancelMon()
		m.cancelMon = nil
	}
	m.monEvents, m.monErrs = nil, nil
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case connectedMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		if msg.err != nil {
			m.connErr = msg.err
			return m, nil
		}
		m.backend, m.host = msg.backend, msg.host
		m.installSecondFactor()
		m.statusBusy, m.rateBusy = true, true
		cmds := []tea.Cmd{m.fetchStatus(), m.fetchRates(), tickRates(m.gen), tickStatus(m.gen), m.startMonitor()}
		if m.tab != tabOverview {
			cmds = append(cmds, m.fetchTab(m.tab))
		}
		return m, tea.Batch(cmds...)

	case statusMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.statusBusy = false
		if msg.err != nil && msg.st.Model == "" {
			m.setFlash(msg.err)
			return m, nil
		}
		st := msg.st
		m.status = &st
		if len(m.upHist) == 0 && len(st.Rates.UpHist) > 0 {
			// Seed the sparklines with the box's own 5 s history (newest-first → oldest-first).
			m.upHist = reversed(st.Rates.UpHist)
			m.downHist = reversed(st.Rates.DownHist)
		}
		return m, nil

	case ratesMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.rateBusy = false
		if msg.err == nil {
			if m.status != nil {
				m.status.Rates.Up, m.status.Rates.Down = msg.r.Up, msg.r.Down
			}
			m.upHist = appendCapped(m.upHist, msg.r.Up)
			m.downHist = appendCapped(m.downHist, msg.r.Down)
		}
		return m, nil

	case rateTick:
		if msg.gen != m.gen || m.backend == nil {
			return m, nil
		}
		cmds := []tea.Cmd{tickRates(m.gen)}
		// One request in flight per router: skip the tick if the last poll hasn't returned.
		if !m.rateBusy {
			m.rateBusy = true
			cmds = append(cmds, m.fetchRates())
		}
		return m, tea.Batch(cmds...)

	case statusTick:
		if msg.gen != m.gen || m.backend == nil {
			return m, nil
		}
		cmds := []tea.Cmd{tickStatus(m.gen)}
		if !m.statusBusy {
			m.statusBusy = true
			cmds = append(cmds, m.fetchStatus())
		}
		return m, tea.Batch(cmds...)

	case hostsMsg:
		if msg.gen == m.gen {
			m.hosts, m.presence = msg.hosts, msg.presence
			m.setFlash(msg.err)
			m.clampCursor(tabHosts, len(m.visibleHosts()))
		}
		return m, nil
	case wifiMsg:
		if msg.gen == m.gen {
			m.wifi = msg.wifi
		}
		return m, nil
	case wlansMsg:
		if msg.gen == m.gen {
			m.wlans = msg.ws
			m.setFlash(msg.err)
			m.clampCursor(tabWLAN, len(m.wlans))
		}
		return m, nil
	case callsMsg:
		if msg.gen == m.gen {
			m.calls = msg.calls
			m.setFlash(msg.err)
			m.clampCursor(tabCalls, len(m.visibleCalls()))
		}
		return m, nil
	case meshMsg:
		if msg.gen == m.gen {
			m.mesh = msg.nodes
			m.setFlash(msg.err)
			m.clampCursor(tabMesh, m.listLen(tabMesh))
		}
		return m, nil
	case logMsg:
		if msg.gen == m.gen {
			m.logEvs = msg.evs
			m.setFlash(msg.err)
			m.clampCursor(tabLog, len(m.logEvs))
		}
		return m, nil
	case contactsMsg:
		if msg.gen == m.gen {
			m.books, m.book, m.contacts = msg.books, msg.book, msg.contacts
			if m.contacts == nil {
				m.contacts = []fritz.Contact{}
			}
			m.setFlash(msg.err)
			m.clampCursor(tabContacts, len(m.visibleContacts()))
		}
		return m, nil
	case accessMsg:
		if msg.gen == m.gen {
			if m.access == nil {
				m.access = map[string]string{}
			}
			m.access[msg.ip] = msg.access
			if msg.info != nil {
				if m.hostInfo == nil {
					m.hostInfo = map[string]fritz.HostInfo{}
				}
				m.hostInfo[msg.ip] = *msg.info
			}
			if msg.err != nil {
				m.access[msg.ip] = "unknown (" + msg.err.Error() + ")"
			}
		}
		return m, nil
	case pwChangedMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.sf = nil
		if msg.err != nil {
			m.setFlash(msg.err)
			return m, nil
		}
		m.flash, m.flashOK = fmt.Sprintf("%s (%s) password changed", msg.w.Label(), msg.w.SSID), true
		m.qr = renderQR(msg.w, msg.pw) + "\n" + row("⚿", msg.pw, "new password")
		return m, m.fetchTab(tabWLAN)
	case allStatusMsg:
		if m.routerUp == nil {
			m.routerUp = map[string]bool{}
		}
		for _, r := range msg.rows {
			m.routerUp[r.name] = r.err == nil
		}
		if msg.gen == m.gen && !msg.background {
			m.qr = renderAllStatus(msg.rows)
		}
		return m, nil
	case devsMsg:
		if msg.gen == m.gen {
			m.devs = msg.devs
			m.setFlash(msg.err)
			m.clampCursor(tabHome, len(m.devs))
		}
		return m, nil

	case actionMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.sf = nil
		if msg.err != nil {
			m.setFlash(msg.err)
			return m, nil
		}
		m.flash, m.flashOK = msg.text, true
		if msg.refresh == tabHosts {
			m.access, m.hostInfo = nil, nil // block/unblock, priority, profile change it
			return m, tea.Batch(m.fetchTab(tabHosts), m.detailAccess())
		}
		return m, m.fetchTab(msg.refresh)

	case qrMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		if msg.err != nil {
			m.setFlash(msg.err)
			return m, nil
		}
		m.qr = msg.text
		return m, nil

	case monEventMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.events = append(m.events, msg.ev)
		if len(m.events) > 50 {
			m.events = m.events[len(m.events)-50:]
		}
		cmds := []tea.Cmd{m.waitMonitor()}
		if msg.ev.Kind == "DISCONNECT" && m.tab == tabCalls {
			cmds = append(cmds, m.fetchTab(tabCalls))
		}
		return m, tea.Batch(cmds...)

	case monErrMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		// Not fatal: the monitor keeps retrying in the background. Only show it on the Calls tab.
		if m.tab == tabCalls {
			m.setFlash(msg.err)
		}
		return m, m.waitMonitor()
	}

	if mm, cmd, ok := m.adminUpdate(msg); ok {
		return mm, cmd
	}
	if m.filtering {
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		return m, cmd
	}
	if m.input != nil {
		var cmd tea.Cmd
		m.input.ti, cmd = m.input.ti.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) setFlash(err error) {
	if err == nil {
		return
	}
	m.flash, m.flashOK = err.Error(), false
	if tr064.NeedsSecondFactor(err) {
		m.flash = "Not confirmed on the box: " + err.Error()
	}
}

func (m *Model) clampCursor(t tab, n int) {
	m.cursor[t] = max(0, min(m.cursor[t], n-1))
}

// visibleHosts applies the active-only toggle, the text filter and the sort order.
func (m Model) visibleHosts() []fritz.Host {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	out := make([]fritz.Host, 0, len(m.hosts))
	for _, h := range m.hosts {
		if m.activeOnly && !h.Active {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(h.Name+" "+h.IP+" "+h.MAC+" "+h.Interface), q) {
			continue
		}
		out = append(out, h)
	}
	switch m.hostSort {
	case "active":
		fritz.SortHosts(out, "ip")
		slices.SortStableFunc(out, func(a, b fritz.Host) int {
			switch {
			case a.Active == b.Active:
				return 0
			case a.Active:
				return -1
			}
			return 1
		})
	default:
		fritz.SortHosts(out, m.hostSort)
	}
	return out
}

var hostSorts = []string{"ip", "name", "active"}

// meshRows flattens the mesh into selectable rows: one per node header and one per client.
func (m Model) meshRows() int {
	n := 0
	for _, node := range m.mesh {
		n += 1 + len(node.Clients)
	}
	return n
}

func (m Model) listLen(t tab) int {
	switch t {
	case tabHosts:
		return len(m.visibleHosts())
	case tabWLAN:
		return len(m.wlans)
	case tabCalls:
		return len(m.visibleCalls())
	case tabContacts:
		return len(m.visibleContacts())
	case tabPhone:
		return len(m.tams) + len(m.msgs) + len(m.defl)
	case tabHome:
		return len(m.devs) + len(m.templates)
	case tabMesh:
		return m.meshRows()
	case tabLog:
		return len(m.logEvs)
	}
	return 0
}

func (m Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if key == "ctrl+c" {
		m.stopMonitor()
		return m, tea.Quit
	}
	// Modal states first.
	if m.sf != nil {
		if key == "esc" {
			m.sf.Abort()
			m.sf = nil
			m.flash, m.flashOK = "confirmation cancelled", true
		}
		return m, nil
	}
	if m.confirm != nil {
		switch key {
		case "y", "Y", "enter":
			run := m.confirm.run
			m.confirm = nil
			return m, run
		default:
			m.confirm = nil
			m.flash, m.flashOK = "cancelled", true
		}
		return m, nil
	}
	if m.input != nil {
		return m.inputKey(k)
	}
	if m.qr != "" || m.help {
		m.qr, m.help = "", false
		return m, nil
	}
	if m.filtering {
		switch key {
		case "esc":
			m.filtering = false
			m.filter.Blur()
			m.filter.SetValue("")
		case "enter":
			m.filtering = false
			m.filter.Blur()
		default:
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(k)
			m.clampCursor(m.tab, m.listLen(m.tab))
			return m, cmd
		}
		return m, nil
	}

	switch key {
	case "q":
		m.stopMonitor()
		return m, tea.Quit
	case "?":
		m.help = true
		return m, nil
	case "tab", "right", "l":
		return m.setTab((m.tab + 1) % tabCount)
	case "shift+tab", "left", "h":
		return m.setTab((m.tab + tabCount - 1) % tabCount)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		return m.setTab(tab(key[0] - '1'))
	case "0":
		return m.setTab(tabSystem)
	case "]":
		return m, m.switchRouter(1)
	case "[":
		return m, m.switchRouter(-1)
	case "r":
		m.flash = ""
		if m.backend == nil {
			m.connErr = nil
			return m, m.connect()
		}
		return m, m.fetchTab(m.tab)
	case "down", "j":
		m.cursor[m.tab] = min(m.cursor[m.tab]+1, max(m.listLen(m.tab)-1, 0))
		return m, m.detailAccess()
	case "up", "k":
		m.cursor[m.tab] = max(m.cursor[m.tab]-1, 0)
		return m, m.detailAccess()
	}
	if m.backend == nil {
		return m, nil
	}
	return m.tabKey(key)
}

func (m Model) setTab(t tab) (tea.Model, tea.Cmd) {
	if t != m.tab {
		m.filter.SetValue("") // the filter belongs to the tab it was typed in
	}
	m.tab = t
	m.flash = ""
	return m, m.fetchTab(t)
}

func (m Model) tabKey(key string) (tea.Model, tea.Cmd) {
	if mm, cmd, ok := m.adminKey(key); ok {
		return mm, cmd
	}
	cur := m.cursor[m.tab]
	switch m.tab {
	case tabOverview:
		switch key {
		case "R":
			m.confirm = &confirmation{
				prompt: fmt.Sprintf("Reconnect %s? The internet connection drops for a few seconds.", m.routerName()),
				run: m.action("reconnect triggered", tabOverview, func(c context.Context, b Backend) error {
					return b.Reconnect(c)
				}),
			}
		case "B":
			m.confirm = &confirmation{
				prompt: fmt.Sprintf("Reboot %s? Network and phones are down for ~2 minutes.", m.routerName()),
				run: m.action("reboot triggered", tabOverview, func(c context.Context, b Backend) error {
					return b.Reboot(c)
				}),
			}
		case "A":
			m.flash, m.flashOK = "polling all routers…", true
			return m, m.fetchAllStatus(false)
		}
	case tabHosts:
		hosts := m.visibleHosts()
		switch key {
		case "/":
			m.filtering = true
			return m, m.filter.Focus()
		case "a":
			m.activeOnly = !m.activeOnly
			m.clampCursor(tabHosts, len(m.visibleHosts()))
		case "s":
			m.hostSort = hostSorts[(slices.Index(hostSorts, m.hostSort)+1)%len(hostSorts)]
		case "enter":
			m.detail = !m.detail
			return m, tea.Batch(m.detailAccess(), m.fetchProfiles())
		case "n":
			if cur < len(hosts) {
				m.renameHost(hosts[cur])
				return m, textinput.Blink
			}
		case "b", "u":
			if cur < len(hosts) {
				h, block := hosts[cur], key == "b"
				verb := map[bool]string{true: "blocked", false: "allowed"}[block]
				run := m.action(fmt.Sprintf("internet %s for %s", verb, orDash(h.Name)), tabHosts, func(c context.Context, b Backend) error {
					return b.SetInternetBlocked(c, h.IP, block)
				})
				if !block {
					return m, run
				}
				m.confirm = &confirmation{prompt: fmt.Sprintf("Block internet for %s (%s)? LAN access keeps working.", orDash(h.Name), h.IP), run: run}
			}
		case "w":
			if cur < len(hosts) {
				h := hosts[cur]
				m.confirm = &confirmation{
					prompt: fmt.Sprintf("Send Wake-on-LAN to %s (%s)?", orDash(h.Name), h.MAC),
					run: m.action("magic packet sent to "+h.MAC, tabHosts, func(c context.Context, b Backend) error {
						return b.WakeOnLAN(c, h.MAC)
					}),
				}
			}
		}
	case tabWLAN:
		if cur >= len(m.wlans) {
			return m, nil
		}
		w := m.wlans[cur]
		switch key {
		case " ", "enter":
			enable := !w.Enabled
			verb := map[bool]string{true: "enabled", false: "disabled"}[enable]
			run := m.action(fmt.Sprintf("%s (%s) %s", w.Label(), w.SSID, verb), tabWLAN, func(c context.Context, b Backend) error {
				return b.SetWLAN(c, w.Index, enable)
			})
			if !enable && !w.Guest {
				m.confirm = &confirmation{prompt: fmt.Sprintf("Disable %s (%s)? If you are connected through it, you lose this session.", w.Label(), w.SSID), run: run}
				return m, nil
			}
			return m, run
		case "c":
			gen, b := m.gen, m.backend
			return m, func() tea.Msg {
				c, cancel := ctx()
				defer cancel()
				pw, err := b.WLANPassword(c, w.Index)
				if err != nil {
					return qrMsg{gen: gen, err: err}
				}
				return qrMsg{gen: gen, text: renderQR(w, pw)}
			}
		case "p":
			return m, m.showWLANPassword(w)
		case "P":
			if err := m.changeWLANPassword(w); err != nil {
				m.setFlash(err)
				return m, nil
			}
			return m, textinput.Blink
		}
	case tabHome:
		if cur >= len(m.devs) {
			return m, nil
		}
		d := m.devs[cur]
		switch key {
		case " ", "enter":
			if d.Switch != nil {
				return m, m.action(d.Name+" toggled", tabHome, func(c context.Context, b Backend) error {
					return b.SetSwitch(c, d.AIN, "toggle")
				})
			}
			if d.Thermostat != nil { // space on a thermostat: off ↔ 20 °C
				v := aha.TempOff
				if d.Thermostat.Target == aha.TempOff {
					v = 40
				}
				m.devs[cur].Thermostat.Target = v
				return m, m.setThermostat(d, v)
			}
		case "+", "=", "-":
			if d.Thermostat == nil {
				return m, nil
			}
			v := d.Thermostat.Target
			if v == aha.TempOff || v == aha.TempOn || v < 16 {
				v = 40 // 20 °C as a starting point
			}
			if key == "-" {
				v = max(16, v-1)
			} else {
				v = min(56, v+1)
			}
			m.devs[cur].Thermostat.Target = v // optimistic; refreshed after the call
			return m, m.setThermostat(d, v)
		}
	case tabCalls:
		switch key {
		case "f":
			m.callFilter = (m.callFilter + 1) % len(callFilters)
			m.clampCursor(tabCalls, len(m.visibleCalls()))
		case "d":
			m.callDays = (m.callDays + 1) % len(callDays)
			m.calls = nil
			return m, m.fetchTab(tabCalls)
		}
	case tabContacts:
		switch key {
		case "/":
			m.filtering = true
			return m, m.filter.Focus()
		case "p":
			if len(m.books) > 1 {
				m.book = (m.book + 1) % len(m.books)
				m.contacts, m.cursor[tabContacts] = nil, 0
				return m, m.fetchContacts(m.book)
			}
		}
	case tabLog:
		if key == "g" {
			m.logGroup = (m.logGroup + 1) % len(logGroups)
			m.logEvs = nil
			return m, m.fetchTab(tabLog)
		}
	}
	return m, nil
}

func reversed(s []int64) []int64 {
	out := slices.Clone(s)
	slices.Reverse(out)
	return out
}

func appendCapped(s []int64, v int64) []int64 {
	s = append(s, v)
	if len(s) > historyLen {
		s = s[len(s)-historyLen:]
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}
