package main

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
)

// scrollStep is the number of lines Ctrl+N and Ctrl+P scroll.
const scrollStep = 3

// pane shows entries in a scrollable view with a scrollbar. The chat and the picker preview use it,
// so both render and scroll the same way: wheel, Ctrl+N, and Ctrl+P.
type pane struct {
	vp      viewport.Model
	md      *glamour.TermRenderer
	entries []entry
	w       int // width with the scrollbar column
}

func newPane() pane { return pane{vp: viewport.New()} }

func (p *pane) resize(w, h int) {
	p.vp.SetWidth(max(w-1, 0))
	p.vp.SetHeight(h)
	if w != p.w {
		p.w = w
		p.md, _ = glamour.NewTermRenderer(glamour.WithStyles(answerStyle), glamour.WithWordWrap(max(w-3, 1)))
		p.render()
	}
}

func (p *pane) setEntries(es []entry) {
	p.entries = es
	p.render()
}

// render keeps the view at the bottom when it was there.
func (p *pane) render() {
	atEnd := p.vp.AtBottom()
	p.vp.SetContent(renderEntries(p.entries, p.vp.Width(), p.md))
	if atEnd {
		p.vp.GotoBottom()
	}
}

func (p *pane) scroll(lines int) {
	if lines > 0 {
		p.vp.ScrollDown(lines)
	} else {
		p.vp.ScrollUp(-lines)
	}
}

func (p *pane) update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "ctrl+n":
			p.scroll(scrollStep)
			return nil
		case "ctrl+p":
			p.scroll(-scrollStep)
			return nil
		}
	}
	var c tea.Cmd
	p.vp, c = p.vp.Update(msg)
	return c
}

func (p *pane) view() string {
	return lipgloss.JoinHorizontal(lipgloss.Top, p.vp.View(), p.scrollbar())
}

func (p *pane) scrollbar() string {
	h, total := p.vp.Height(), p.vp.TotalLineCount()
	thumb, top := h, 0
	if total > h {
		thumb = max(1, h*h/total)
		top = int(p.vp.ScrollPercent() * float64(h-thumb))
	}
	rows := make([]string, h)
	for i := range rows {
		if i >= top && i < top+thumb {
			rows[i] = "█"
		} else {
			rows[i] = styleMeta.Render("│")
		}
	}
	return strings.Join(rows, "\n")
}
