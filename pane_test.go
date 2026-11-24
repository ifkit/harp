package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPaneTypingDoesNotScroll(t *testing.T) {
	p := newPane()
	p.resize(40, 5)
	p.setEntries([]entry{{notice, strings.Repeat("line\n", 50)}})
	p.vp.GotoTop()
	for _, r := range "udfbjkhl " {
		p.update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if off := p.vp.YOffset(); off != 0 {
		t.Fatalf("typing scrolled the pane to %d", off)
	}
	p.update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if p.vp.YOffset() == 0 {
		t.Fatal("PgDn did not scroll")
	}
}
