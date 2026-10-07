package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/satlavida/ragalay/internal/search"
)

type screen int

const (
	scrLoading screen = iota
	scrWelcome
	scrFolders
	scrModel // wizard: which model (plan2 S18)
	scrLicense
	scrPlan
	scrInstall
	scrMain
)

type tab int

const (
	tabSearch tab = iota
	tabStatus
	tabFolders
	tabModel
)

var tabNames = []string{"Search", "Status", "Folders", "Model"}

// Model is the Bubble Tea model.
type Model struct {
	b      Backend
	ctx    context.Context
	cancel context.CancelFunc
	width  int
	height int
	screen screen
	err    error

	// wizard
	first     FirstRun
	choices   []bool // folder picker: [0] = everything, then Subfolders
	cursor    int
	plan      Plan
	setupEv   SetupEvent
	setupLog  []string
	setupErr  error
	bar       progress.Model
	installed bool

	// main
	tab        tab
	input      textinput.Model
	inputOn    bool
	results    []search.Result
	notice     string
	selected   int
	searching  bool
	lastQuery  string
	status     Status
	activity   WatchEvent // latest background event
	watchStop  context.CancelFunc
	watching   bool
	watchGen   int
	restartReq bool
	reembedReq bool
	reembeding bool
	events     chan WatchEvent
	update     string
	folderSel  int
	folderIn   textinput.Model
	folderOn   bool
	message    string

	// model choice (Model view and wizard)
	models      Models
	modelSel    int
	form        svcForm
	formOn      bool
	keyIn       textinput.Model
	keyOn       bool
	confirmSw   *pendingSwitch
	busy        string
	modelNote   string
	switchAfter bool // start the rebuild once the main view knows the index's state
}

// New returns the model; Run starts it.
func New(b Backend) Model {
	ctx, cancel := context.WithCancel(context.Background())
	in := textinput.New()
	in.Placeholder = "Search your documents…"
	in.Prompt = "🔎 "
	in.CharLimit = 500
	fi := textinput.New()
	fi.Placeholder = "folder to add, e.g. notes"
	fi.Prompt = "+ "
	ki := textinput.New()
	ki.Prompt = "API key:   "
	ki.EchoMode = textinput.EchoPassword
	return Model{b: b, ctx: ctx, cancel: cancel, input: in, folderIn: fi, keyIn: ki,
		bar: progress.New(progress.WithDefaultGradient()), events: make(chan WatchEvent, 64)}
}

// Run starts the interface and blocks until the user quits.
func Run(b Backend) error {
	_, err := tea.NewProgram(New(b), tea.WithAltScreen()).Run()
	return err
}

// Messages.
type (
	firstRunMsg struct {
		fr  FirstRun
		err error
	}
	initDoneMsg struct{ err error }
	planMsg     struct {
		p   Plan
		err error
	}
	setupEventMsg SetupEvent
	setupDoneMsg  struct{ err error }
	statusMsg     struct {
		s   Status
		err error
	}
	searchMsg struct {
		q    string
		resp search.Response
		err  error
	}
	watchEventMsg WatchEvent
	watchDoneMsg  struct {
		gen int
		err error
	}
	reembedDone struct{ err error }
	noteMsg     struct {
		text string
		err  error
	}
	updateMsg string
	tickMsg   struct{}
)

func (m Model) Init() tea.Cmd {
	return func() tea.Msg {
		fr, err := m.b.FirstRun(m.ctx)
		return firstRunMsg{fr, err}
	}
}

func (m Model) cmdStatus() tea.Cmd {
	return func() tea.Msg {
		s, err := m.b.Status(m.ctx)
		return statusMsg{s, err}
	}
}

func (m Model) waitEvent() tea.Cmd {
	return func() tea.Msg {
		select {
		case ev := <-m.events:
			return watchEventMsg(ev)
		case <-m.ctx.Done():
			return nil
		}
	}
}

func tick() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// startWatch runs background scanning/indexing while the interface is open.
func (m *Model) startWatch() tea.Cmd {
	ctx, stop := context.WithCancel(m.ctx)
	m.watchGen++
	gen := m.watchGen
	m.watchStop, m.watching = stop, true
	events, b := m.events, m.b
	return func() tea.Msg {
		err := b.Watch(ctx, func(ev WatchEvent) {
			select {
			case events <- ev:
			default: // the view only needs the latest events
			}
		})
		return watchDoneMsg{gen, err}
	}
}

func (m *Model) enterMain() tea.Cmd {
	m.screen = scrMain
	m.inputOn = true
	m.input.Focus()
	return tea.Batch(m.cmdStatus(), m.cmdModels(), m.startWatch(), m.waitEvent(), tick(), textinput.Blink,
		func() tea.Msg {
			v, _ := m.b.CheckUpdate(m.ctx)
			return updateMsg(v)
		})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.bar.Width = max(20, min(60, msg.Width-10))
		m.input.Width = max(20, msg.Width-6)
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.cancel()
			return m, tea.Quit
		}
	}
	switch m.screen {
	case scrMain:
		return m.updateMain(msg)
	default:
		return m.updateWizard(msg)
	}
}

// ---- wizard ----

func (m Model) updateWizard(msg tea.Msg) (tea.Model, tea.Cmd) {
	if mm, cmd, ok := m.modelMsgs(msg); ok {
		return mm, cmd
	}
	switch msg := msg.(type) {
	case firstRunMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.first = msg.fr
		switch {
		case msg.fr.NeedInit:
			m.screen = scrWelcome
		case msg.fr.NeedSetup:
			m.screen = scrModel
			m.choices = make([]bool, len(msg.fr.Subfolders)+1)
			return m, m.cmdModels()
		default:
			cmd := m.enterMain()
			return m, cmd
		}
		m.choices = make([]bool, len(msg.fr.Subfolders)+1)
		m.choices[0] = true
		return m, nil
	case initDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		// A new folder chooses its model (plan2 S18).
		m.screen = scrModel
		return m, m.cmdModels()
	case planMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.plan, m.screen = msg.p, scrPlan
		return m, nil
	case setupEventMsg:
		m.setupEv = SetupEvent(msg)
		if msg.Note != "" {
			m.setupLog = append(m.setupLog, msg.Note)
			if len(m.setupLog) > 6 {
				m.setupLog = m.setupLog[len(m.setupLog)-6:]
			}
		}
		return m, m.waitSetup()
	case setupDoneMsg:
		if msg.err != nil {
			m.setupErr = msg.err
			return m, nil
		}
		m.installed = true
		cmd := m.enterMain()
		return m, cmd
	case tea.KeyMsg:
		return m.wizardKey(msg)
	}
	return m, nil
}

// setupEvents carries setup progress from the setup goroutine.
var setupEvents = make(chan SetupEvent, 64)

func (m Model) waitSetup() tea.Cmd {
	return func() tea.Msg {
		select {
		case ev := <-setupEvents:
			return setupEventMsg(ev)
		case <-m.ctx.Done():
			return nil
		}
	}
}

func (m Model) runSetup() tea.Cmd {
	run := func() tea.Msg {
		err := m.b.RunSetup(m.ctx, func(ev SetupEvent) {
			select {
			case setupEvents <- ev:
			default:
			}
		})
		return setupDoneMsg{err}
	}
	return tea.Batch(run, m.waitSetup())
}

func (m Model) wizardKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if m.err != nil {
		if key == "q" || key == "esc" || key == "enter" {
			m.cancel()
			return m, tea.Quit
		}
		return m, nil
	}
	switch m.screen {
	case scrModel:
		if mm, cmd, ok := m.modelKey(k); ok {
			return mm, cmd
		}
		if key == "q" || key == "esc" {
			m.cancel()
			return m, tea.Quit
		}
	case scrWelcome:
		switch key {
		case "enter":
			if len(m.first.Subfolders) == 0 {
				return m, m.cmdInit(nil)
			}
			m.screen, m.cursor = scrFolders, 0
		case "q", "esc":
			m.cancel()
			return m, tea.Quit
		}
	case scrFolders:
		switch key {
		case "up", "k":
			m.cursor = max(0, m.cursor-1)
		case "down", "j":
			m.cursor = min(len(m.choices)-1, m.cursor+1)
		case " ", "x":
			m.choices[m.cursor] = !m.choices[m.cursor]
			if m.cursor == 0 && m.choices[0] {
				for i := 1; i < len(m.choices); i++ {
					m.choices[i] = false
				}
			} else if m.cursor > 0 && m.choices[m.cursor] {
				m.choices[0] = false
			}
		case "enter":
			var picked []string
			if !m.choices[0] {
				for i, on := range m.choices[1:] {
					if on {
						picked = append(picked, m.first.Subfolders[i])
					}
				}
			}
			return m, m.cmdInit(picked)
		case "q", "esc":
			m.cancel()
			return m, tea.Quit
		}
	case scrLicense:
		switch key {
		case "y", "Y":
			return m, func() tea.Msg {
				p, err := m.b.SetupPlan(m.ctx)
				return planMsg{p, err}
			}
		case "n", "q", "esc":
			m.cancel()
			return m, tea.Quit
		}
	case scrPlan:
		switch key {
		case "enter", "y":
			m.screen = scrInstall
			return m, m.runSetup()
		case "q", "esc", "n":
			m.cancel()
			return m, tea.Quit
		}
	case scrInstall:
		if m.setupErr != nil {
			switch key {
			case "r":
				m.setupErr = nil
				return m, m.runSetup()
			case "q", "esc":
				m.cancel()
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m Model) cmdInit(folders []string) tea.Cmd {
	return func() tea.Msg { return initDoneMsg{m.b.Init(m.ctx, m.first.Dir, folders)} }
}

// ---- main ----

func (m Model) updateMain(msg tea.Msg) (tea.Model, tea.Cmd) {
	if mm, cmd, ok := m.modelMsgs(msg); ok {
		return mm, cmd
	}
	switch msg := msg.(type) {
	case statusMsg:
		if msg.err == nil {
			m.status = msg.s
		}
		// A model chosen in the wizard (or before its download) starts
		// its switch once the index's state is known.
		if m.switchAfter {
			m.switchAfter = false
			if m.status.Mismatch != "" && !m.reembeding && !m.reembedReq {
				m.message = "Switching models…"
				return m, m.beginReembed()
			}
		}
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.cmdStatus(), tick())
	case watchEventMsg:
		m.activity = WatchEvent(msg)
		cmds := []tea.Cmd{m.waitEvent()}
		if msg.Kind == "indexed" || msg.Kind == "scan" || msg.Kind == "idle" {
			cmds = append(cmds, m.cmdStatus())
		}
		return m, tea.Batch(cmds...)
	case watchDoneMsg:
		if msg.gen != m.watchGen {
			return m, nil // an older watch finishing after a restart
		}
		m.watching = false
		if msg.err != nil && m.ctx.Err() == nil && !m.reembedReq && !m.restartReq {
			m.activity = WatchEvent{Kind: "busy", Text: msg.err.Error()}
		}
		if m.restartReq {
			m.restartReq = false
			m.activity = WatchEvent{}
			cmd := m.startWatch()
			return m, cmd
		}
		if m.reembedReq {
			m.reembedReq, m.reembeding = false, true
			events := m.events
			return m, func() tea.Msg {
				return reembedDone{m.b.Reembed(m.ctx, func(ev WatchEvent) {
					select {
					case events <- ev:
					default:
					}
				})}
			}
		}
		return m, nil
	case reembedDone:
		m.reembeding = false
		if msg.err != nil {
			m.message = "Rebuild failed: " + msg.err.Error()
		} else {
			m.message = "Index rebuilt."
		}
		cmd := m.startWatch()
		return m, tea.Batch(m.cmdStatus(), m.cmdModels(), cmd)
	case searchMsg:
		if msg.q != m.lastQuery {
			return m, nil // a newer search is running
		}
		m.searching = false
		if msg.err != nil {
			m.notice = msg.err.Error()
			m.results = nil
		} else {
			m.results, m.notice, m.selected = msg.resp.Results, msg.resp.Notice, 0
		}
		return m, nil
	case noteMsg:
		if msg.err != nil {
			m.message = msg.err.Error()
		} else {
			m.message = msg.text
		}
		return m, m.cmdStatus()
	case updateMsg:
		m.update = string(msg)
		return m, nil
	case tea.KeyMsg:
		return m.mainKey(msg)
	}
	var cmd tea.Cmd
	if m.inputOn {
		m.input, cmd = m.input.Update(msg)
	}
	if m.folderOn {
		m.folderIn, cmd = m.folderIn.Update(msg)
	}
	if m.keyOn {
		m.keyIn, cmd = m.keyIn.Update(msg)
	} else if m.formOn && m.form.focus < 3 {
		m.form.fields[m.form.focus], cmd = m.form.fields[m.form.focus].Update(msg)
	}
	return m, cmd
}

// beginReembed stops background indexing and runs the rebuild (R, or a
// model switch).
func (m *Model) beginReembed() tea.Cmd {
	m.reembedReq = true
	if m.watching {
		m.watchStop()
		return nil
	}
	gen := m.watchGen
	return func() tea.Msg { return watchDoneMsg{gen: gen} }
}

func (m Model) mainKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	// Text entry first: typing must not trigger shortcuts.
	if m.inputOn && m.tab == tabSearch {
		switch key {
		case "enter":
			q := strings.TrimSpace(m.input.Value())
			if q == "" {
				return m, nil
			}
			m.lastQuery, m.searching = q, true
			return m, func() tea.Msg {
				resp, err := m.b.Search(m.ctx, q, search.Options{K: 20})
				return searchMsg{q, resp, err}
			}
		case "esc", "down", "tab":
			if key == "tab" {
				m.inputOn = false
				m.input.Blur()
				m.tab = tabStatus
				return m, m.cmdStatus()
			}
			if len(m.results) > 0 {
				m.inputOn = false
				m.input.Blur()
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return m, cmd
	}
	if m.tab == tabModel {
		if mm, cmd, ok := m.modelKey(k); ok {
			return mm, cmd
		}
	}
	if m.folderOn {
		switch key {
		case "enter":
			p := strings.TrimSpace(m.folderIn.Value())
			m.folderOn = false
			m.folderIn.Blur()
			m.folderIn.SetValue("")
			if p == "" {
				return m, nil
			}
			return m, func() tea.Msg {
				note, err := m.b.AddFolder(m.ctx, p)
				return noteMsg{note, err}
			}
		case "esc":
			m.folderOn = false
			m.folderIn.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.folderIn, cmd = m.folderIn.Update(k)
		return m, cmd
	}

	switch key {
	case "q":
		m.cancel()
		return m, tea.Quit
	case "tab":
		m.tab = (m.tab + 1) % tab(len(tabNames))
		if m.tab == tabSearch {
			m.inputOn = true
			m.input.Focus()
		}
		return m, tea.Batch(m.cmdStatus(), m.cmdModels())
	case "shift+tab":
		m.tab = (m.tab + tab(len(tabNames)) - 1) % tab(len(tabNames))
		return m, tea.Batch(m.cmdStatus(), m.cmdModels())
	case "/", "s":
		m.tab, m.inputOn = tabSearch, true
		m.input.Focus()
		return m, textinput.Blink
	case "r":
		if m.reembeding {
			return m, nil
		}
		m.message = "Rescanning…"
		if m.watching {
			m.restartReq = true
			m.watchStop() // the watch restarts when it has stopped
			return m, nil
		}
		cmd := m.startWatch()
		return m, cmd
	case "R":
		if m.status.Mismatch != "" && !m.reembeding {
			m.message = "Rebuilding the index…"
			return m, m.beginReembed()
		}
	}
	switch m.tab {
	case tabSearch:
		switch key {
		case "up", "k":
			m.selected = max(0, m.selected-1)
		case "down", "j":
			m.selected = min(len(m.results)-1, m.selected+1)
		case "enter":
			if r, ok := m.current(); ok {
				target := r.Path
				if r.ParentPath != "" {
					target = r.ParentPath // the document the image is in
				}
				return m, m.cmdOpen(target)
			}
		case "o":
			if r, ok := m.current(); ok {
				return m, m.cmdOpen(r.Path) // the image (or file) itself
			}
		}
	case tabFolders:
		n := len(m.status.Folders)
		switch key {
		case "up", "k":
			m.folderSel = max(0, m.folderSel-1)
		case "down", "j":
			m.folderSel = min(n-1, m.folderSel+1)
		case "a":
			m.folderOn = true
			m.folderIn.Focus()
			return m, textinput.Blink
		case "d", "x":
			if f, ok := m.currentFolder(); ok {
				return m, func() tea.Msg {
					note, err := m.b.RemoveFolder(m.ctx, f.Path, key == "x")
					return noteMsg{note, err}
				}
			}
		}
	}
	return m, nil
}

func (m Model) current() (search.Result, bool) {
	if m.selected < 0 || m.selected >= len(m.results) {
		return search.Result{}, false
	}
	return m.results[m.selected], true
}

func (m Model) currentFolder() (Folder, bool) {
	if m.folderSel < 0 || m.folderSel >= len(m.status.Folders) {
		return Folder{}, false
	}
	return m.status.Folders[m.folderSel], true
}

func (m Model) cmdOpen(path string) tea.Cmd {
	return func() tea.Msg {
		if err := m.b.Open(path); err != nil {
			return noteMsg{err: fmt.Errorf("could not open %s: %w", path, err)}
		}
		return noteMsg{text: "Opened " + path}
	}
}

// ---- view ----

var (
	accent    = lipgloss.AdaptiveColor{Light: "#5A3FD6", Dark: "#A78BFA"}
	muted     = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
	warnColor = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	errColor  = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	title     = lipgloss.NewStyle().Bold(true).Foreground(accent)
	dim       = lipgloss.NewStyle().Foreground(muted)
	warn      = lipgloss.NewStyle().Foreground(warnColor).Bold(true)
	bad       = lipgloss.NewStyle().Foreground(errColor)
	selStyle  = lipgloss.NewStyle().Foreground(accent).Bold(true)
	box       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(muted).Padding(0, 1)
)

func (m Model) View() string {
	if m.err != nil {
		return "\n  " + bad.Render("Something went wrong:") + "\n\n  " + m.err.Error() + "\n\n  " + dim.Render("Press Enter to close.")
	}
	switch m.screen {
	case scrLoading:
		return "\n  Starting ragalay…"
	case scrWelcome:
		return m.viewWelcome()
	case scrFolders:
		return m.viewFolders()
	case scrModel:
		return m.viewWizardModel()
	case scrLicense:
		return m.viewLicense()
	case scrPlan:
		return m.viewPlan()
	case scrInstall:
		return m.viewInstall()
	}
	return m.viewMain()
}

func (m Model) viewWelcome() string {
	var b strings.Builder
	b.WriteString("\n  " + title.Render("Welcome to ragalay") + "\n\n")
	b.WriteString("  ragalay makes the documents in this folder searchable:\n\n")
	b.WriteString("    " + selStyle.Render(m.first.Dir) + "\n\n")
	b.WriteString("  It reads Markdown, PDF and image files. Nothing leaves your computer.\n\n")
	b.WriteString(dim.Render("  Enter: continue    q: quit"))
	return b.String()
}

func (m Model) viewFolders() string {
	var b strings.Builder
	b.WriteString("\n  " + title.Render("Which folders should ragalay search?") + "\n\n")
	labels := append([]string{"Everything in this folder"}, m.first.Subfolders...)
	for i, l := range labels {
		mark := "[ ]"
		if m.choices[i] {
			mark = "[x]"
		}
		line := fmt.Sprintf("%s %s", mark, l)
		if i == m.cursor {
			line = selStyle.Render("> " + line)
		} else {
			line = "  " + line
		}
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("\n" + dim.Render("  ↑/↓: move    space: select    Enter: done    You can change this later."))
	return b.String()
}

func (m Model) viewWizardModel() string {
	var b strings.Builder
	b.WriteString("\n  " + title.Render("Which AI model should ragalay use?") + "\n\n")
	b.WriteString(dim.Render("  You can change this later in the Model view.") + "\n\n")
	b.WriteString(m.viewModels())
	b.WriteString("\n" + dim.Render("  "+strings.Replace(m.modelHelp(), "Tab: next view   ", "", 1)))
	return b.String()
}

func (m Model) viewLicense() string {
	var b strings.Builder
	b.WriteString("\n  " + title.Render("One-time setup: the AI models") + "\n\n")
	for _, l := range strings.Split(m.first.License, "\n") {
		b.WriteString("  " + l + "\n")
	}
	b.WriteString("\n  Do you accept these terms?  " + selStyle.Render("y") + " = yes,  " + dim.Render("n = no (quit)"))
	return b.String()
}

func (m Model) viewPlan() string {
	var b strings.Builder
	b.WriteString("\n  " + title.Render("Download the AI models") + "\n\n")
	b.WriteString("  Your computer: " + m.plan.Accelerator + "\n\n")
	for _, it := range m.plan.Items {
		b.WriteString(fmt.Sprintf("    %-46s %s\n", it.Name, human(it.Size)))
	}
	b.WriteString(fmt.Sprintf("    %-46s %s\n\n", "Total", human(m.plan.Total)))
	b.WriteString("  This happens once per computer. Leave this window open while it downloads.\n\n")
	b.WriteString(dim.Render("  Enter: start    q: quit"))
	return b.String()
}

func (m Model) viewInstall() string {
	var b strings.Builder
	b.WriteString("\n  " + title.Render("Setting up") + "\n\n")
	if m.setupEv.Steps > 0 {
		b.WriteString(fmt.Sprintf("  Step %d of %d: %s\n\n", m.setupEv.Step, m.setupEv.Steps, m.setupEv.Title))
	}
	if m.setupEv.Total > 0 {
		pct := float64(m.setupEv.Done) / float64(m.setupEv.Total)
		b.WriteString("  " + m.bar.ViewAs(min(1, pct)) + "\n")
		b.WriteString(dim.Render(fmt.Sprintf("  %s of %s", human(m.setupEv.Done), human(m.setupEv.Total))) + "\n\n")
	}
	for _, l := range m.setupLog {
		b.WriteString(dim.Render("  "+l) + "\n")
	}
	if m.setupErr != nil {
		b.WriteString("\n  " + bad.Render("Setup stopped: ") + m.setupErr.Error() + "\n\n")
		b.WriteString(dim.Render("  r: try again (finished parts are kept)    q: quit"))
	}
	return b.String()
}

func (m Model) viewMain() string {
	var b strings.Builder
	// Header: tabs and activity.
	var tabs []string
	for i, n := range tabNames {
		if tab(i) == m.tab {
			tabs = append(tabs, selStyle.Render("["+n+"]"))
		} else {
			tabs = append(tabs, dim.Render(" "+n+" "))
		}
	}
	b.WriteString(title.Render("ragalay") + "  " + strings.Join(tabs, " ") + "   " + m.activityLine() + "\n")
	if m.status.Mismatch != "" {
		b.WriteString(warn.Render("The model settings changed. Press R to switch (search keeps using the current model meanwhile).") + "\n")
	}
	if m.status.Switch != "" {
		b.WriteString(dim.Render("Switching models: "+m.status.Switch+". Search uses the previous model until it finishes.") + "\n")
	}
	if !m.status.SetupReady && m.screen == scrMain {
		b.WriteString(warn.Render("The AI models are not set up. Run ragalay setup.") + "\n")
	}
	if m.update != "" {
		b.WriteString(dim.Render("A new version ("+m.update+") is available: run ragalay update.") + "\n")
	}
	if m.message != "" {
		b.WriteString(dim.Render(m.message) + "\n")
	}
	b.WriteString("\n")
	switch m.tab {
	case tabSearch:
		b.WriteString(m.viewSearch())
	case tabStatus:
		b.WriteString(m.viewStatus())
	case tabFolders:
		b.WriteString(m.viewFoldersTab())
	case tabModel:
		b.WriteString(m.viewModels())
	}
	b.WriteString("\n" + dim.Render(m.help()))
	return b.String()
}

func (m Model) activityLine() string {
	a := m.activity
	switch a.Kind {
	case "progress":
		eta := ""
		if a.ETA > 0 {
			eta = ", ~" + shortDur(a.ETA) + " left"
		}
		return dim.Render(fmt.Sprintf("indexing %d/%d%s", a.Done+1, a.Total, eta))
	case "busy":
		return warn.Render("another ragalay is indexing")
	case "error":
		return bad.Render("error: " + a.Text)
	}
	if m.status.Queued > 0 {
		return dim.Render(fmt.Sprintf("%d waiting", m.status.Queued))
	}
	return dim.Render(fmt.Sprintf("%d documents indexed", m.status.Done))
}

func (m Model) viewSearch() string {
	var b strings.Builder
	b.WriteString(m.input.View() + "\n\n")
	if m.searching {
		return b.String() + dim.Render("  searching…")
	}
	if m.notice != "" {
		b.WriteString(warn.Render("  "+m.notice) + "\n")
	}
	if len(m.results) == 0 {
		if m.lastQuery != "" {
			b.WriteString(dim.Render("  No results.") + "\n")
		}
		return b.String()
	}
	// Results list (top) and preview (bottom), sized to the window.
	listRows := max(3, (m.height-12)/2/2)
	start := max(0, min(m.selected-listRows/2, len(m.results)-listRows))
	for i := start; i < min(len(m.results), start+listRows); i++ {
		r := m.results[i]
		where := r.Path
		if r.Page > 0 {
			where += fmt.Sprintf(" · p.%d", r.Page)
		}
		line := fmt.Sprintf("%2d. %s", i+1, where)
		sub := firstLine(r.Text, m.width-8)
		if r.ParentPath != "" {
			sub = "image in " + r.ParentPath
		}
		if i == m.selected {
			b.WriteString(selStyle.Render("> "+line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
		b.WriteString(dim.Render("     "+sub) + "\n")
	}
	if r, ok := m.current(); ok {
		var p strings.Builder
		head := r.Path
		if r.HeadingPath != "" {
			head += " — " + r.HeadingPath
		}
		p.WriteString(title.Render(head) + "\n")
		if r.PairedPath != "" {
			p.WriteString(dim.Render("transcription: "+r.PairedPath) + "\n")
		}
		text := r.Text
		if text == "" {
			text = "(" + r.Modality + " match: open it to see)"
		}
		p.WriteString(wrap(text, max(30, m.width-8), max(4, m.height-listRows*2-14)))
		b.WriteString(box.Width(max(30, m.width-4)).Render(p.String()) + "\n")
	}
	return b.String()
}

func (m Model) viewStatus() string {
	s := m.status
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  Folder:     %s\n", s.Root))
	b.WriteString(fmt.Sprintf("  Documents:  %d indexed, %d waiting, %d failed\n", s.Done, s.Pending, s.Failed))
	b.WriteString(fmt.Sprintf("  Chunks:     %d\n", s.Chunks))
	if s.Device != "" {
		b.WriteString(fmt.Sprintf("  Indexing on: %s\n", s.Device))
	}
	if s.OtherIndex != "" {
		b.WriteString(warn.Render("  Another ragalay ("+s.OtherIndex+") is indexing this folder.") + "\n")
	}
	if a := m.activity; a.Kind == "progress" {
		b.WriteString(fmt.Sprintf("\n  Now: %s (%d of %d", a.Current, a.Done+1, a.Total))
		if a.ETA > 0 {
			b.WriteString(", about " + shortDur(a.ETA) + " left")
		}
		b.WriteString(")\n")
	}
	if len(s.FailedDocs) > 0 {
		b.WriteString("\n  " + bad.Render("Could not index:") + "\n")
		for i, f := range s.FailedDocs {
			if i == 10 {
				b.WriteString(dim.Render(fmt.Sprintf("    … and %d more", len(s.FailedDocs)-10)) + "\n")
				break
			}
			b.WriteString(fmt.Sprintf("    %s  %s\n", f.Path, dim.Render(firstLine(f.Error, 60))))
		}
	}
	return b.String()
}

func (m Model) viewFoldersTab() string {
	var b strings.Builder
	if m.status.Whole {
		b.WriteString("  Searching everything in this folder. Add a folder to search only that.\n\n")
	}
	for i, f := range m.status.Folders {
		label := fmt.Sprintf("%-32s %5d documents", f.Path, f.Documents)
		if f.Kept {
			label += dim.Render("  (kept, not scanned)")
		} else if !f.Exists {
			label += bad.Render("  (missing)")
		}
		if i == m.folderSel {
			b.WriteString(selStyle.Render("> "+label) + "\n")
		} else {
			b.WriteString("  " + label + "\n")
		}
	}
	if m.folderOn {
		b.WriteString("\n" + m.folderIn.View() + "\n")
	}
	return b.String()
}

func (m Model) help() string {
	switch {
	case m.tab == tabModel:
		return m.modelHelp()
	case m.tab == tabSearch && m.inputOn:
		return "Enter: search   ↓/Esc: results   Tab: next view   Ctrl+C: quit"
	case m.tab == tabSearch:
		return "↑/↓: choose   Enter: open file   o: open image   /: new search   Tab: next view   r: rescan   q: quit"
	case m.tab == tabFolders && m.folderOn:
		return "Enter: add   Esc: cancel"
	case m.tab == tabFolders:
		return "a: add folder   d: remove (drop its documents)   x: remove but keep searchable   Tab: next view   q: quit"
	}
	return "Tab: next view   r: rescan   q: quit"
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n/1024)
}

func shortDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()+0.5))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()+0.5))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func firstLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); n > 1 && len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// wrap word-wraps text to width and keeps at most lines lines.
func wrap(s string, width, lines int) string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		cur := ""
		for _, w := range words {
			if cur != "" && len([]rune(cur))+1+len([]rune(w)) > width {
				out = append(out, cur)
				cur = w
				continue
			}
			if cur != "" {
				cur += " "
			}
			cur += w
		}
		if cur != "" {
			out = append(out, cur)
		}
		if len(out) >= lines {
			break
		}
	}
	if len(out) > lines {
		out = append(out[:lines-1], "…")
	}
	return strings.Join(out, "\n")
}
