package main

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/cloudwego/eino/schema"
)

var commandNames = []string{"/sessions", "/new", "/fork", "/rewind", "/fork-rewind"}

type pickKind int

const (
	pickSessions pickKind = iota
	pickRewind
	pickForkRewind
)

type pickItem struct {
	label   string  // one line in the list
	entries []entry // shown in the preview; for sessions, loaded on first selection
	loaded  bool
	id      string // session id (sessions picker)
	idx     int    // index in history (message pickers)
}

// picker is a list with a preview window. It replaces the chat view while open.
type picker struct {
	kind     pickKind
	items    []pickItem
	sel, top int
	pane     pane
	confirm  string // not empty: waits for y, shows this text
	deleting bool   // the pending confirmation deletes a session
	shownSel int    // selection the preview was built for
}

func newPicker(kind pickKind) *picker {
	return &picker{kind: kind, pane: newPane(), shownSel: -1}
}

func (a *app) fail(format string, args ...any) {
	a.entries = append(a.entries, entry{failure, "error: " + fmt.Sprintf(format, args...)})
}

func (a *app) note(s string) { a.entries = append(a.entries, entry{notice, s}) }

// entriesFrom rebuilds the chat view from the history.
func entriesFrom(history []*schema.Message) []entry {
	var out []entry
	for _, m := range history {
		switch m.Role {
		case schema.System:
			out = append(out, entry{sentSystem, m.Content})
		case schema.User:
			out = append(out, entry{sentUser, m.Content})
		case schema.Assistant:
			out = append(out, entry{answer, m.Content})
		}
	}
	return out
}

// save appends msgs to the current session. The first save creates the session
// and writes the whole history, which already includes msgs.
func (a *app) save(msgs ...*schema.Message) {
	if a.store == nil {
		return
	}
	var err error
	if a.sessionID == "" {
		id := newSessionID()
		if err = a.store.Append(id, a.history...); err == nil {
			a.sessionID = id
		}
	} else {
		err = a.store.Append(a.sessionID, msgs...)
	}
	if err != nil {
		a.fail("save session: %v", err)
	}
}

func (a *app) setHistory(id string, history []*schema.Message) {
	a.sessionID, a.history = id, history
	a.entries = entriesFrom(history)
	a.think, a.reply = -1, -1
}

// reset returns to the empty state: no session until the next message.
func (a *app) reset() error {
	if m, ok := a.chatModel.(*acpModel); ok {
		if err := m.newSession(); err != nil {
			return err
		}
	}
	a.setHistory("", []*schema.Message{schema.SystemMessage(systemPrompt)})
	return nil
}

// forkAt saves history[:n] as a new session and switches to it.
func (a *app) forkAt(n int) {
	h := slices.Clone(a.history[:n])
	id := newSessionID()
	if err := a.store.Append(id, h...); err != nil {
		a.fail("save session: %v", err)
		return
	}
	a.setHistory(id, h)
	a.note("forked into session " + id)
}

func (a *app) runCommand(cmd string) {
	if a.busy {
		a.fail("%s: wait for the reply to end", cmd)
		return
	}
	_, isACP := a.chatModel.(*acpModel)
	switch cmd {
	case "/new":
		if err := a.reset(); err != nil {
			a.fail("new session: %v", err)
		}
	case "/sessions":
		list, err := a.store.List()
		switch {
		case err != nil:
			a.fail("list sessions: %v", err)
		case len(list) == 0:
			a.fail("no saved sessions")
		default:
			p := newPicker(pickSessions)
			for _, s := range list {
				p.items = append(p.items, pickItem{
					label: s.Modified.Format("2006-01-02 15:04") + "  " + firstLine(s.Title),
					id:    s.ID,
				})
			}
			a.openPicker(p)
		}
	case "/fork", "/rewind", "/fork-rewind":
		switch {
		case isACP:
			a.fail("%s is not available with --acp: the agent keeps the history", cmd)
		case len(a.history) < 2:
			a.fail("%s: no messages", cmd)
		case cmd == "/fork":
			a.forkAt(len(a.history))
		default:
			p := newPicker(pickRewind)
			if cmd == "/fork-rewind" {
				p.kind = pickForkRewind
			}
			for i, m := range a.history {
				if m.Role == schema.System {
					continue
				}
				p.items = append(p.items, pickItem{label: fmt.Sprintf("%-9s %s", m.Role, firstLine(m.Content)), entries: entriesFrom([]*schema.Message{m}), loaded: true, idx: i})
			}
			a.openPicker(p)
		}
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if s == "" {
		return "(empty)"
	}
	return s
}

func (a *app) openPicker(p *picker) {
	p.sel = len(p.items) - 1
	if p.kind == pickSessions {
		p.sel = 0
	}
	a.pick = p
	a.layoutPicker()
}

// layoutPicker keeps the selection visible and the preview in sync with it.
func (a *app) layoutPicker() {
	p := a.pick
	w, h := a.chat.w, a.chat.vp.Height()
	pw := max(w-w/2-1, 1)
	if p.sel < p.top {
		p.top = p.sel
	}
	if p.sel >= p.top+h {
		p.top = p.sel - h + 1
	}
	p.pane.resize(pw, h)
	if it := &p.items[p.sel]; p.kind == pickSessions && !it.loaded {
		it.loaded = true
		if msgs, err := a.store.Load(it.id); err == nil {
			it.entries = entriesFrom(msgs)
		} else {
			it.entries = []entry{{failure, "error: " + err.Error()}}
		}
	}
	if p.sel != p.shownSel {
		var shown []entry
		for _, e := range p.items[p.sel].entries {
			if e.role != sentSystem {
				shown = append(shown, e)
			}
		}
		p.pane.setEntries(shown)
		p.pane.vp.GotoTop()
		p.shownSel = p.sel
	}
}

func (a *app) pickerView() string {
	p := a.pick
	w, h := a.chat.w, a.chat.vp.Height()
	lw := w / 2
	cell := lipgloss.NewStyle().Inline(true).MaxWidth(lw)
	var rows []string
	for i := p.top; i < min(p.top+h, len(p.items)); i++ {
		s := cell.Render(p.items[i].label)
		s += strings.Repeat(" ", max(lw-lipgloss.Width(s), 0))
		if i == p.sel {
			s = lipgloss.NewStyle().Reverse(true).Render(s)
		}
		rows = append(rows, s)
	}
	list := lipgloss.NewStyle().Width(lw).Height(h).Render(strings.Join(rows, "\n"))
	sep := styleMeta.Render(strings.TrimSuffix(strings.Repeat("│\n", h), "\n"))
	return lipgloss.JoinHorizontal(lipgloss.Top, list, sep, p.pane.view())
}

func (a *app) pickerStatus() string {
	p := a.pick
	if p.confirm != "" {
		return p.confirm
	}
	keys := "Up/Down, j/k: select | Ctrl+N/Ctrl+P: scroll preview | Enter/o: "
	switch p.kind {
	case pickSessions:
		return keys + "open | d: delete | Esc: close"
	case pickRewind:
		return keys + "rewind to this message | Esc: close"
	}
	return keys + "fork from this message | Esc: close"
}

func (a *app) updatePicker(msg tea.KeyPressMsg) {
	p := a.pick
	if p.confirm != "" {
		if msg.String() == "y" {
			it := p.items[p.sel]
			p.confirm = ""
			if p.deleting {
				a.deleteSession(it.id)
			} else {
				a.rewindTo(it.idx + 1)
				a.pick = nil
			}
		} else {
			p.confirm = ""
		}
		return
	}
	switch msg.String() {
	case "up", "k":
		p.sel = max(p.sel-1, 0)
	case "down", "j":
		p.sel = min(p.sel+1, len(p.items)-1)
	case "ctrl+n":
		p.pane.scroll(scrollStep)
	case "ctrl+p":
		p.pane.scroll(-scrollStep)
	case "esc":
		a.pick = nil
	case "d":
		if p.kind == pickSessions {
			p.deleting = true
			p.confirm = "Delete this session for good? Press y to confirm, any other key to cancel."
		}
	case "enter", "o":
		it := p.items[p.sel]
		switch p.kind {
		case pickSessions:
			a.pick = nil
			a.openSession(it.id)
		case pickRewind:
			p.deleting = false
			p.confirm = "Rewind deletes all messages after the selected one for good. Press y to confirm, any other key to cancel."
		case pickForkRewind:
			a.pick = nil
			a.forkAt(it.idx + 1)
		}
	}
	if a.pick != nil {
		a.layoutPicker()
	}
}

func (a *app) openSession(id string) {
	if _, ok := a.chatModel.(*acpModel); ok {
		a.fail("opening a saved session is not available with --acp: the agent keeps the history")
		return
	}
	h, err := a.store.Load(id)
	switch {
	case err != nil:
		a.fail("load session: %v", err)
	case len(h) == 0:
		a.fail("session %s has no messages", id)
	default:
		if h[0].Role != schema.System {
			h = append([]*schema.Message{schema.SystemMessage(systemPrompt)}, h...)
		}
		a.setHistory(id, h)
	}
}

func (a *app) deleteSession(id string) {
	p := a.pick
	if err := a.store.Delete(id); err != nil {
		a.fail("delete session: %v", err)
		a.pick = nil
		return
	}
	if id == a.sessionID {
		if err := a.reset(); err != nil {
			a.fail("new session: %v", err)
		}
	}
	p.deleting = false
	p.items = slices.Delete(p.items, p.sel, p.sel+1)
	if len(p.items) == 0 {
		a.pick = nil
		return
	}
	p.sel = min(p.sel, len(p.items)-1)
	p.shownSel = -1
}

// rewindTo cuts the history to its first n messages and rewrites the session file.
func (a *app) rewindTo(n int) {
	h := slices.Clone(a.history[:n])
	if err := a.store.Rewrite(a.sessionID, h); err != nil {
		a.fail("rewrite session: %v", err)
		return
	}
	a.setHistory(a.sessionID, h)
	a.note("rewound session " + a.sessionID)
}
