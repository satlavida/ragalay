package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// The Model view and the wizard's model step (plan2 §5.8, S18): pick
// EmbeddingGemma 2, Jina v5 or an OpenAI-compatible service, then switch.
// A switch always goes through a confirmation that says what will happen.

const serviceProfile = "openai"

var imageModes = []string{"none", "jina", "vllm", "llamacpp"}

// svcForm edits a service's address, model, key variable and image mode.
type svcForm struct {
	fields []textinput.Model // base URL, model, key variable name
	image  int               // index in imageModes
	focus  int               // 0..2 fields, 3 = image mode
}

func newSvcForm(s Service) svcForm {
	mk := func(prompt, placeholder, value string) textinput.Model {
		t := textinput.New()
		t.Prompt, t.Placeholder, t.CharLimit = prompt, placeholder, 300
		t.SetValue(value)
		return t
	}
	f := svcForm{fields: []textinput.Model{
		mk("Address:   ", "http://localhost:11434/v1", s.BaseURL),
		mk("Model:     ", "nomic-embed-text", s.Model),
		mk("Key name:  ", "empty for a server on this computer, e.g. OPENAI_API_KEY", s.KeyEnv),
	}}
	for i, m := range imageModes {
		if m == s.ImageInput {
			f.image = i
		}
	}
	f.fields[0].Focus()
	return f
}

func (f *svcForm) service() Service {
	return Service{BaseURL: strings.TrimSpace(f.fields[0].Value()), Model: strings.TrimSpace(f.fields[1].Value()),
		KeyEnv: strings.TrimSpace(f.fields[2].Value()), ImageInput: imageModes[f.image]}
}

func (f *svcForm) move(d int) {
	f.focus = (f.focus + d + 4) % 4
	for i := range f.fields {
		if i == f.focus {
			f.fields[i].Focus()
		} else {
			f.fields[i].Blur()
		}
	}
}

// key handles a key; done reports Enter (submit) or Esc (cancel).
func (f *svcForm) key(k tea.KeyMsg) (submit, cancel bool, cmd tea.Cmd) {
	switch k.String() {
	case "enter":
		return true, false, nil
	case "esc":
		return false, true, nil
	case "tab", "down":
		f.move(1)
		return false, false, nil
	case "shift+tab", "up":
		f.move(-1)
		return false, false, nil
	case "left", "right":
		if f.focus == 3 {
			d := 1
			if k.String() == "left" {
				d = len(imageModes) - 1
			}
			f.image = (f.image + d) % len(imageModes)
			return false, false, nil
		}
	}
	if f.focus < 3 {
		f.fields[f.focus], cmd = f.fields[f.focus].Update(k)
	}
	return false, false, cmd
}

func (f svcForm) view() string {
	var b strings.Builder
	for _, t := range f.fields {
		b.WriteString("  " + t.View() + "\n")
	}
	img := "Images:    "
	for i, m := range imageModes {
		if i == f.image {
			img += selStyle.Render("["+m+"]") + " "
		} else {
			img += dim.Render(m) + " "
		}
	}
	if f.focus == 3 {
		img = selStyle.Render("> ") + img + dim.Render("←/→")
	} else {
		img = "  " + img
	}
	b.WriteString(img + "\n")
	b.WriteString(dim.Render("  Images: none = keyword search only; jina / vllm / llamacpp if the service embeds images.") + "\n")
	return b.String()
}

// pendingSwitch is a choice waiting for the user's confirmation.
type pendingSwitch struct {
	choice ModelChoice
	plan   ModelPlan
}

// Messages.
type (
	modelsMsg struct {
		m   Models
		err error
	}
	planModelMsg struct {
		c   ModelChoice
		p   ModelPlan
		err error
	}
	usedModelMsg struct {
		c   ModelChoice
		p   ModelPlan
		err error
	}
	envSetMsg struct{ err error }
)

func (m Model) cmdModels() tea.Cmd {
	return func() tea.Msg {
		ms, err := m.b.Models(m.ctx)
		return modelsMsg{ms, err}
	}
}

// cmdPlan tests a service (a broken address should not start a switch),
// then asks what the switch involves.
func (m Model) cmdPlan(c ModelChoice) tea.Cmd {
	return func() tea.Msg {
		if c.Profile == serviceProfile {
			if _, err := m.b.TestModel(m.ctx, c); err != nil {
				return planModelMsg{c: c, err: fmt.Errorf("the service did not answer: %w", err)}
			}
		}
		p, err := m.b.PlanModel(m.ctx, c)
		return planModelMsg{c, p, err}
	}
}

func (m Model) cmdUse(ps pendingSwitch, allow bool) tea.Cmd {
	return func() tea.Msg {
		err := m.b.UseModel(m.ctx, ps.choice, allow)
		return usedModelMsg{ps.choice, ps.plan, err}
	}
}

func (m Model) currentChoice() ModelChoice {
	if m.modelSel < 0 || m.modelSel >= len(m.models.Options) {
		return ModelChoice{}
	}
	return ModelChoice{Profile: m.models.Options[m.modelSel].Name}
}

// modelMsgs handles the model messages in any screen.
func (m Model) modelMsgs(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case modelsMsg:
		if msg.err == nil {
			m.models = msg.m
			for i, o := range m.models.Options {
				if o.Active {
					m.modelSel = i
				}
			}
		}
		return m, nil, true
	case planModelMsg:
		m.busy = ""
		if msg.err != nil {
			m.modelNote = msg.err.Error()
			return m, nil, true
		}
		ps := pendingSwitch{msg.c, msg.p}
		// Nothing to rebuild and nothing leaving the computer: no question.
		if msg.p.Documents == 0 && !msg.p.Remote {
			m.busy = "Saving…"
			return m, m.cmdUse(ps, false), true
		}
		m.confirmSw = &ps
		return m, nil, true
	case usedModelMsg:
		m.busy = ""
		if msg.err != nil {
			m.modelNote = msg.err.Error()
			return m, nil, true
		}
		m.formOn, m.confirmSw, m.modelNote = false, nil, ""
		m.switchAfter = msg.p.Documents > 0
		if msg.p.NeedSetup {
			if m.watching {
				m.watchStop()
			}
			m.first.License, m.first.NeedSetup = msg.p.License, true
			m.screen = scrLicense
			return m, nil, true
		}
		if m.screen != scrMain {
			cmd := m.enterMain()
			return m, cmd, true
		}
		m.message = "Switching models…"
		cmd := m.beginReembed()
		m.switchAfter = false
		return m, tea.Batch(cmd, m.cmdModels()), true
	case envSetMsg:
		if msg.err != nil {
			m.modelNote = "Could not set the key: " + msg.err.Error()
		} else {
			m.modelNote = "Key saved for this user. Windows opened from now on will see it."
		}
		return m, m.cmdModels(), true
	}
	return m, nil, false
}

// modelKey handles keys while choosing a model (Model view or wizard).
// handled is false for keys the caller should process.
func (m Model) modelKey(k tea.KeyMsg) (Model, tea.Cmd, bool) {
	key := k.String()
	if m.busy != "" {
		return m, nil, true
	}
	if m.confirmSw != nil {
		switch key {
		case "y", "Y", "enter":
			ps := *m.confirmSw
			m.busy = "Saving…"
			return m, m.cmdUse(ps, ps.plan.Remote), true
		case "n", "N", "esc":
			m.confirmSw = nil
		}
		return m, nil, true
	}
	if m.keyOn {
		switch key {
		case "enter":
			name, value := m.form.service().KeyEnv, m.keyIn.Value()
			m.keyOn = false
			m.keyIn.SetValue("")
			m.keyIn.Blur()
			if name == "" || value == "" {
				m.modelNote = "Type the key name in the form first."
				return m, nil, true
			}
			return m, func() tea.Msg { return envSetMsg{m.b.SetUserEnv(name, value)} }, true
		case "esc":
			m.keyOn = false
			m.keyIn.Blur()
			return m, nil, true
		}
		var cmd tea.Cmd
		m.keyIn, cmd = m.keyIn.Update(k)
		return m, cmd, true
	}
	if m.formOn {
		if key == "ctrl+k" && m.models.CanSetEnv {
			m.keyOn = true
			m.keyIn.Focus()
			return m, textinput.Blink, true
		}
		submit, cancel, cmd := m.form.key(k)
		switch {
		case cancel:
			m.formOn, m.modelNote = false, ""
		case submit:
			c := ModelChoice{Profile: serviceProfile, Service: m.form.service()}
			m.busy, m.modelNote = "Checking the service…", ""
			return m, m.cmdPlan(c), true
		}
		return m, cmd, true
	}
	switch key {
	case "up", "k":
		m.modelSel = max(0, m.modelSel-1)
	case "down", "j":
		m.modelSel = min(len(m.models.Options)-1, m.modelSel+1)
	case "enter":
		c := m.currentChoice()
		if c.Profile == "" {
			return m, nil, true
		}
		m.modelNote = ""
		if c.Profile == serviceProfile {
			m.form, m.formOn = newSvcForm(m.models.Service), true
			return m, textinput.Blink, true
		}
		m.busy = "Checking…"
		return m, m.cmdPlan(c), true
	default:
		return m, nil, false
	}
	return m, nil, true
}

// viewModels renders the model chooser (Model view and wizard).
func (m Model) viewModels() string {
	var b strings.Builder
	switch {
	case m.confirmSw != nil:
		b.WriteString(m.viewConfirm())
	case m.formOn:
		b.WriteString("  " + title.Render("Another service (OpenAI-compatible)") + "\n\n")
		b.WriteString(dim.Render("  Ollama, LM Studio, llama-server or vLLM on this computer, or an online service.") + "\n\n")
		b.WriteString(m.form.view())
		if m.keyOn {
			b.WriteString("\n  " + m.keyIn.View() + "\n")
		} else if s := m.form.service(); s.KeyEnv != "" {
			b.WriteString("\n" + dim.Render(indent(m.models.KeyHelp)) + "\n")
		}
	default:
		for i, o := range m.models.Options {
			state := ""
			switch {
			case o.Active:
				state = "  (this folder)"
			case o.Local && !o.Installed:
				state = "  (downloads first)"
			}
			line := fmt.Sprintf("%-38s %s", o.Title, dim.Render(o.Pitch+state))
			if i == m.modelSel {
				b.WriteString(selStyle.Render("> ") + line + "\n")
			} else {
				b.WriteString("  " + line + "\n")
			}
		}
	}
	if m.busy != "" {
		b.WriteString("\n  " + dim.Render(m.busy) + "\n")
	}
	if m.modelNote != "" {
		b.WriteString("\n  " + warn.Render(m.modelNote) + "\n")
	}
	return b.String()
}

func (m Model) viewConfirm() string {
	ps := m.confirmSw
	var b strings.Builder
	name := ps.choice.Profile
	for _, o := range m.models.Options {
		if o.Name == ps.choice.Profile {
			name = o.Title
		}
	}
	if ps.choice.Profile == serviceProfile {
		name = ps.choice.Service.Model + " at " + ps.plan.Host
	}
	b.WriteString("  " + title.Render("Switch to "+name+"?") + "\n\n")
	if ps.plan.NeedSetup {
		b.WriteString("  The model is downloaded first (one time per computer).\n")
	}
	if ps.plan.Documents > 0 {
		b.WriteString(fmt.Sprintf("  Every document is indexed again (%d documents). Search keeps using the\n  current model until that finishes.\n", ps.plan.Documents))
	}
	if ps.plan.Remote {
		b.WriteString("\n  " + warn.Render(fmt.Sprintf("Your documents and searches will be sent to %s,", ps.plan.Host)) + "\n")
		b.WriteString("  " + warn.Render("which is not on this computer.") + "\n")
	}
	b.WriteString("\n  " + selStyle.Render("y") + " = switch,  " + dim.Render("n = cancel") + "\n")
	return b.String()
}

func (m Model) modelHelp() string {
	switch {
	case m.confirmSw != nil:
		return "y: switch   n: cancel"
	case m.keyOn:
		return "Enter: save the key for this user   Esc: cancel"
	case m.formOn && m.models.CanSetEnv:
		return "Tab: next field   Enter: check and use   Ctrl+K: set the API key   Esc: back"
	case m.formOn:
		return "Tab: next field   Enter: check and use   Esc: back"
	}
	return "↑/↓: choose   Enter: use this model   Tab: next view   q: quit"
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + strings.TrimSpace(l)
	}
	return strings.Join(lines, "\n")
}
