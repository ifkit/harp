package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var answerStyle = func() ansi.StyleConfig {
	s := styles.NoTTYStyleConfig
	for _, h := range []*ansi.StyleBlock{&s.H1, &s.H2, &s.H3, &s.H4, &s.H5, &s.H6} {
		h.Prefix, h.Bold = "", new(true)
	}
	s.Strong = ansi.StylePrimitive{Bold: new(true)}
	s.Emph = ansi.StylePrimitive{Italic: new(true)}
	s.Code.Prefix, s.Code.Suffix = "", ""
	return s
}()

const systemPrompt = "You are a helpful assistant. Answer in Markdown."

var (
	styleMeta = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleUser = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleErr  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

type role int

const (
	sentSystem role = iota // sent to the API: shown gray
	sentUser
	thinking
	answer // rendered as Markdown
	failure
	notice // gray, not part of the history
	debugKeys
)

type entry struct {
	role role
	text string
}

type streamMsg struct {
	sr *schema.StreamReader[*schema.Message]
}
type (
	chunkMsg struct{ msg *schema.Message }
	doneMsg  struct{ err error }
)

type app struct {
	chatModel model.BaseChatModel
	history   []*schema.Message // exactly what the API receives
	entries   []entry
	think     int // index of the thinking entry of the current turn, -1 if none
	reply     int // index of the answer entry of the current turn, -1 if none
	sr        *schema.StreamReader[*schema.Message]
	busy      bool
	input     textinput.Model
	chat      pane
	ready     bool
	debug     bool
	hideMeta  bool // hides system and thinking entries
	store     *store
	sessionID string  // empty until the first message
	pick      *picker // not nil: a picker replaces the chat view
}

func newApp(cm model.BaseChatModel, debug bool, st *store) *app {
	in := textinput.New()
	in.Prompt = "> "
	return &app{
		chatModel: cm,
		history:   []*schema.Message{schema.SystemMessage(systemPrompt)},
		entries:   []entry{{sentSystem, systemPrompt}},
		think:     -1,
		reply:     -1,
		input:     in,
		debug:     debug,
		chat:      newPane(),
		store:     st,
	}
}

func (a *app) Init() tea.Cmd {
	return tea.Batch(a.input.Focus(), textinput.Blink)
}

func recv(sr *schema.StreamReader[*schema.Message]) tea.Cmd {
	return func() tea.Msg {
		m, err := sr.Recv()
		if err != nil {
			sr.Close()
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return doneMsg{err}
		}
		return chunkMsg{m}
	}
}

func (a *app) start() tea.Cmd {
	history := a.history
	return func() tea.Msg {
		sr, err := a.chatModel.Stream(context.Background(), history)
		if err != nil {
			return doneMsg{err}
		}
		return streamMsg{sr}
	}
}

// appendTo grows the entry at *idx, creating it on first use.
func (a *app) appendTo(idx *int, r role, s string) {
	if *idx < 0 {
		a.entries = append(a.entries, entry{r, ""})
		*idx = len(a.entries) - 1
	}
	a.entries[*idx].text += s
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if a.debug {
		a.debugInput(msg)
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.chat.resize(msg.Width, msg.Height-2)
		a.input.SetWidth(msg.Width - 3)
		a.ready = true
		if a.pick != nil {
			a.layoutPicker()
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return a, tea.Quit
		}
		if a.pick != nil {
			a.updatePicker(msg)
			return a, nil
		}
		switch msg.String() {
		case "ctrl+h":
			// Return early: textinput treats ctrl+h as backspace.
			a.hideMeta = !a.hideMeta
			a.refresh()
			return a, nil
		case "enter":
			text := strings.TrimSpace(a.input.Value())
			if text == "" {
				break
			}
			if slices.Contains(commandNames, text) {
				a.input.Reset()
				a.runCommand(text)
				break
			}
			if a.busy {
				break
			}
			a.input.Reset()
			a.history = append(a.history, schema.UserMessage(text))
			a.save(a.history[len(a.history)-1])
			a.entries = append(a.entries, entry{sentUser, text})
			a.think, a.reply, a.busy = -1, -1, true
			cmds = append(cmds, a.start())
		}
	case streamMsg:
		a.sr = msg.sr
		cmds = append(cmds, recv(a.sr))
	case chunkMsg:
		if msg.msg.ReasoningContent != "" {
			a.appendTo(&a.think, thinking, msg.msg.ReasoningContent)
		}
		if msg.msg.Content != "" {
			a.appendTo(&a.reply, answer, msg.msg.Content)
		}
		cmds = append(cmds, recv(a.sr))
	case doneMsg:
		a.busy = false
		if msg.err != nil {
			a.entries = append(a.entries, entry{failure, "error: " + msg.err.Error()})
		} else if a.reply >= 0 {
			a.history = append(a.history, schema.AssistantMessage(a.entries[a.reply].text, nil))
			a.save(a.history[len(a.history)-1])
		}
	}

	if !a.ready {
		return a, nil
	}
	var c tea.Cmd
	a.input, c = a.input.Update(msg)
	cmds = append(cmds, c)
	if a.pick != nil {
		c = a.pick.pane.update(msg)
	} else {
		c = a.chat.update(msg)
	}
	cmds = append(cmds, c)
	a.refresh()
	return a, tea.Batch(cmds...)
}

func (a *app) debugInput(msg tea.Msg) {
	var s string
	switch m := msg.(type) {
	case tea.KeyPressMsg:
		s = fmt.Sprintf("key:%q", m.String())
	case tea.MouseMsg:
		s = "mouse:" + m.String()
	default:
		return
	}
	if n := len(a.entries); a.entries[n-1].role == debugKeys {
		a.entries[n-1].text += "  " + s
	} else {
		a.entries = append(a.entries, entry{debugKeys, "debug: " + s})
	}
}

// renderEntry formats one entry for a pane of width w.
func renderEntry(e entry, w int, md *glamour.TermRenderer) string {
	switch e.role {
	case sentUser:
		return styleUser.Width(w).Render(e.text)
	case failure:
		return styleErr.Width(w).Render(e.text)
	case answer:
		if md != nil {
			if out, err := md.Render(e.text); err == nil {
				return strings.TrimRight(out, "\n")
			}
		}
		return e.text
	}
	return styleMeta.Width(w).Render(e.text) // system, thinking, notice, debug
}

// renderEntries joins the entries one per line. The Markdown renderer starts its output with a
// blank line, so only an answer and a system message need a blank line after them.
func renderEntries(es []entry, w int, md *glamour.TermRenderer) string {
	var parts []string
	for i, e := range es {
		if i > 0 && (es[i-1].role == answer || es[i-1].role == sentSystem) {
			parts = append(parts, "")
		}
		parts = append(parts, renderEntry(e, w, md))
	}
	return strings.Join(parts, "\n")
}

func (a *app) refresh() {
	var shown []entry
	for _, e := range a.entries {
		if !a.hideMeta || (e.role != sentSystem && e.role != thinking) {
			shown = append(shown, e)
		}
	}
	a.chat.setEntries(shown)
}

func (a *app) View() tea.View {
	v := tea.NewView(a.content())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (a *app) content() string {
	if !a.ready {
		return "starting..."
	}
	status := "PgUp/PgDn, Ctrl+N/Ctrl+P, wheel: scroll | Enter: send | /sessions /new /fork /rewind /fork-rewind | Ctrl+H: hide system/thinking | Ctrl+C: quit"
	body := a.chat.view()
	switch {
	case a.pick != nil:
		status, body = a.pickerStatus(), a.pickerView()
	case a.busy:
		status = "waiting for model..."
	}
	return body + "\n" + styleMeta.Render(status) + "\n" + a.input.View()
}

func main() {
	var (
		url   = flag.String("openai", "http://localhost:1234/v1", "OpenAI-compatible API base URL")
		name  = flag.String("model", "current", "Model name")
		key   = flag.String("key", "", "API key")
		debug = flag.Bool("debug", false, "Debug mode")
		agent = flag.String("acp", "", "ACP agent command, e.g. \"npx @zed-industries/claude-code-acp\" (replaces --openai)")
	)
	flag.Parse()

	var (
		cm  model.BaseChatModel
		err error
	)
	if *agent != "" {
		cwd, _ := os.Getwd()
		var a *acpModel
		if a, err = newACP(context.Background(), strings.Fields(*agent), cwd); err == nil {
			defer a.Close()
			cm = a
		}
	} else {
		cm, err = openai.NewChatModel(context.Background(), &openai.ChatModelConfig{
			BaseURL: *url, APIKey: *key, Model: *name,
		})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	st, err := defaultStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := tea.NewProgram(newApp(cm, *debug, st)).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
