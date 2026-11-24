package main

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type stubModel struct{ reply string }

func (s stubModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage(s.reply, nil), nil
}

func (s stubModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage(s.reply, nil)}), nil
}

func testApp(t *testing.T, cm model.BaseChatModel) *app {
	t.Helper()
	a := newApp(cm, false, &store{t.TempDir()})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return a
}

func press(a *app, k tea.Key) { a.Update(tea.KeyPressMsg(k)) }

func pressText(a *app, s string) { press(a, tea.Key{Code: rune(s[0]), Text: s}) }

// say types text and presses Enter. A message is streamed to the end; a command returns at once.
func say(a *app, text string) {
	a.input.SetValue(text)
	press(a, tea.Key{Code: tea.KeyEnter})
	if !a.busy {
		return
	}
	sr, err := a.chatModel.Stream(context.Background(), a.session.history)
	if err != nil {
		panic(err)
	}
	a.Update(streamMsg{sr})
	for a.busy {
		a.Update(recv(sr)())
	}
}

func lastEntry(a *app) entry { return a.entries[len(a.entries)-1] }

func files(t *testing.T, a *app) []string {
	t.Helper()
	list, err := os.ReadDir(a.store.dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, f := range list {
		out = append(out, strings.TrimSuffix(f.Name(), ".jsonl"))
	}
	return out
}

func fileMessages(t *testing.T, a *app, id string) []*schema.Message {
	t.Helper()
	msgs, err := a.store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

func TestSessionCreatedByFirstMessage(t *testing.T) {
	a := testApp(t, stubModel{"pong"})
	if len(files(t, a)) != 0 {
		t.Fatal("file created at launch")
	}
	say(a, "ping")
	ids := files(t, a)
	if len(ids) != 1 || ids[0] != a.session.ID {
		t.Fatalf("ids=%v session=%q", ids, a.session.ID)
	}
	msgs := fileMessages(t, a, a.session.ID)
	if len(msgs) != 3 || msgs[0].Role != schema.System || msgs[1].Content != "ping" || msgs[2].Content != "pong" {
		t.Fatalf("msgs=%v", msgs)
	}
	say(a, "again")
	if n := len(fileMessages(t, a, a.session.ID)); n != 5 {
		t.Fatalf("messages=%d", n)
	}
}

func TestEntriesFrom(t *testing.T) {
	got := entriesFrom([]*schema.Message{schema.SystemMessage("s"), schema.UserMessage("u"), schema.AssistantMessage("a", nil)})
	want := []entry{{sentSystem, "s"}, {sentUser, "u"}, {answer, "a"}}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got=%v", got)
	}
}

func TestCommandParsing(t *testing.T) {
	a := testApp(t, stubModel{"pong"})
	a.busy = true
	a.input.SetValue("/new")
	press(a, tea.Key{Code: tea.KeyEnter})
	if e := lastEntry(a); e.role != failure || a.session.ID != "" {
		t.Fatalf("busy: %v", e)
	}
	a.busy = false
	say(a, "/unknown")
	if len(a.session.history) != 3 || a.session.history[1].Content != "/unknown" {
		t.Fatalf("history=%v", a.session.history)
	}
}

func TestSessionsPicker(t *testing.T) {
	a := testApp(t, stubModel{"pong"})
	say(a, "/sessions")
	if lastEntry(a).role != failure || a.pick != nil {
		t.Fatal("empty list must show an error")
	}
	say(a, "first")
	first := a.session.ID
	say(a, "/new")
	say(a, "second")
	second := a.session.ID
	if first == second {
		t.Fatal("same id")
	}

	say(a, "/sessions")
	if a.pick == nil || len(a.pick.items) != 2 {
		t.Fatalf("pick=%v", a.pick)
	}
	press(a, tea.Key{Code: tea.KeyEscape})
	if a.pick != nil {
		t.Fatal("esc must close")
	}

	say(a, "/sessions")
	for i, it := range a.pick.items {
		if it.id == first {
			a.pick.sel = i
		}
	}
	press(a, tea.Key{Code: tea.KeyEnter})
	if a.pick != nil || a.session.ID != first || a.session.history[1].Content != "first" {
		t.Fatalf("session=%q history=%v", a.session.ID, a.session.history)
	}
	say(a, "more")
	if n := len(fileMessages(t, a, first)); n != 5 {
		t.Fatalf("messages=%d", n)
	}

	// delete the open session: decline first, then confirm
	say(a, "/sessions")
	for i, it := range a.pick.items {
		if it.id == first {
			a.pick.sel = i
		}
	}
	pressText(a, "d")
	pressText(a, "n")
	if len(files(t, a)) != 2 {
		t.Fatal("declined delete removed a file")
	}
	pressText(a, "d")
	pressText(a, "y")
	if ids := files(t, a); len(ids) != 1 || ids[0] != second {
		t.Fatalf("ids=%v", ids)
	}
	if a.session.ID != "" || len(a.session.history) != 1 {
		t.Fatal("deleting the open session must reset")
	}
	if a.pick == nil || len(a.pick.items) != 1 {
		t.Fatal("picker must stay open with the rest")
	}
}

func TestNewAndFork(t *testing.T) {
	a := testApp(t, stubModel{"pong"})
	say(a, "/fork")
	if lastEntry(a).role != failure {
		t.Fatal("fork without messages must fail")
	}
	say(a, "hi")
	old := a.session.ID
	say(a, "/fork")
	if a.session.ID == old || len(fileMessages(t, a, a.session.ID)) != 3 || len(fileMessages(t, a, old)) != 3 {
		t.Fatalf("session=%q old=%q", a.session.ID, old)
	}
	say(a, "next")
	if len(fileMessages(t, a, old)) != 3 || len(fileMessages(t, a, a.session.ID)) != 5 {
		t.Fatal("fork and original must be separate")
	}

	cur := a.session.ID
	say(a, "/new")
	if a.session.ID != "" || len(a.entries) != 1 || a.entries[0].role != sentSystem {
		t.Fatalf("entries=%v", a.entries)
	}
	if len(files(t, a)) != 2 || len(fileMessages(t, a, cur)) != 5 {
		t.Fatal("/new must not touch files")
	}
}

func rewindFixture(t *testing.T) (*app, string) {
	a := testApp(t, stubModel{"pong"})
	say(a, "one")
	say(a, "two")
	id := a.session.ID
	say(a, "/rewind")
	if a.pick == nil || len(a.pick.items) != 4 || a.pick.sel != 3 {
		t.Fatalf("pick=%+v", a.pick)
	}
	return a, id
}

func TestRewind(t *testing.T) {
	a, id := rewindFixture(t)
	press(a, tea.Key{Code: tea.KeyUp})
	press(a, tea.Key{Code: tea.KeyUp}) // second item: the first answer
	press(a, tea.Key{Code: tea.KeyEnter})
	if a.pick == nil || a.pick.confirm == "" {
		t.Fatal("rewind must ask for confirmation")
	}
	pressText(a, "n")
	if a.pick == nil || len(a.session.history) != 5 || len(fileMessages(t, a, id)) != 5 {
		t.Fatal("declined rewind changed something")
	}
	press(a, tea.Key{Code: tea.KeyEnter})
	pressText(a, "y")
	if a.pick != nil || len(a.session.history) != 3 || a.session.history[2].Content != "pong" {
		t.Fatalf("history=%v", a.session.history)
	}
	if n := len(fileMessages(t, a, id)); n != 3 {
		t.Fatalf("file messages=%d", n)
	}
	if len(a.entries) != 4 || lastEntry(a).role != notice {
		t.Fatalf("entries=%v", a.entries)
	}
}

func TestForkRewind(t *testing.T) {
	a := testApp(t, stubModel{"pong"})
	say(a, "/fork-rewind")
	if lastEntry(a).role != failure {
		t.Fatal("empty history must fail")
	}
	say(a, "one")
	say(a, "two")
	id := a.session.ID
	say(a, "/fork-rewind")
	press(a, tea.Key{Code: tea.KeyUp})
	press(a, tea.Key{Code: tea.KeyUp})
	press(a, tea.Key{Code: tea.KeyEnter})
	if a.pick != nil || a.session.ID == id || len(a.session.history) != 3 {
		t.Fatalf("session=%q history=%v", a.session.ID, a.session.history)
	}
	if len(fileMessages(t, a, a.session.ID)) != 3 || len(fileMessages(t, a, id)) != 5 {
		t.Fatal("original must not change")
	}
}

func TestPreviewScroll(t *testing.T) {
	long := strings.Repeat("line\n\n", 100)
	a := testApp(t, stubModel{long})
	say(a, "hi")
	say(a, "/rewind")
	sel := a.pick.sel
	press(a, tea.Key{Code: 'n', Mod: tea.ModCtrl})
	press(a, tea.Key{Code: 'n', Mod: tea.ModCtrl})
	if a.pick.pane.vp.YOffset() != 6 || a.pick.sel != sel {
		t.Fatalf("offset=%d sel=%d", a.pick.pane.vp.YOffset(), a.pick.sel)
	}
	press(a, tea.Key{Code: 'p', Mod: tea.ModCtrl})
	if a.pick.pane.vp.YOffset() != 3 {
		t.Fatalf("offset=%d", a.pick.pane.vp.YOffset())
	}
	if !strings.Contains(a.content(), "line") {
		t.Fatal("picker view must show the preview")
	}
}

func TestPickerJKAndO(t *testing.T) {
	a := testApp(t, stubModel{strings.Repeat("line\n\n", 100)})
	say(a, "hi")
	say(a, "/rewind")
	pressText(a, "k")
	if a.pick.sel != 0 || a.pick.pane.vp.YOffset() != 0 {
		t.Fatalf("sel=%d offset=%d", a.pick.sel, a.pick.pane.vp.YOffset())
	}
	pressText(a, "j")
	if a.pick.sel != 1 {
		t.Fatalf("sel=%d", a.pick.sel)
	}
	pressText(a, "o")
	if a.pick == nil || a.pick.confirm == "" {
		t.Fatal("o must select like Enter")
	}
}

func TestSessionPreviewShowsMessages(t *testing.T) {
	a := testApp(t, stubModel{"pong"})
	say(a, "ping")
	say(a, "/sessions")
	view := a.content()
	if !strings.Contains(view, "ping") || !strings.Contains(view, "pong") {
		t.Fatalf("preview lacks the conversation:\n%s", view)
	}
	pressText(a, "o")
	if a.pick != nil {
		t.Fatal("o must open the session")
	}
}

func TestAnswerIsFollowedByBlankLine(t *testing.T) {
	a := testApp(t, stubModel{"pong"})
	say(a, "q1")
	say(a, "q2")
	lines := strings.Split(a.chat.vp.GetContent(), "\n")
	for i, l := range lines {
		if strings.Contains(l, "q2") && strings.TrimSpace(lines[i-1]) != "" {
			t.Fatalf("no blank line before q2: %q", lines[i-1])
		}
	}
}

func TestChatScrollsLikePreview(t *testing.T) {
	a := testApp(t, stubModel{strings.Repeat("line\n\n", 100)})
	say(a, "hi")
	a.chat.vp.GotoTop()
	press(a, tea.Key{Code: 'n', Mod: tea.ModCtrl})
	if a.chat.vp.YOffset() != 3 {
		t.Fatalf("ctrl+n: offset=%d", a.chat.vp.YOffset())
	}
	press(a, tea.Key{Code: 'p', Mod: tea.ModCtrl})
	if a.chat.vp.YOffset() != 0 {
		t.Fatalf("ctrl+p: offset=%d", a.chat.vp.YOffset())
	}
	a.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if a.chat.vp.YOffset() == 0 {
		t.Fatal("wheel must scroll the chat")
	}

	say(a, "/rewind")
	chatOffset := a.chat.vp.YOffset()
	a.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if a.pick.pane.vp.YOffset() == 0 || a.chat.vp.YOffset() != chatOffset {
		t.Fatalf("wheel must scroll the preview only: preview=%d chat=%d", a.pick.pane.vp.YOffset(), a.chat.vp.YOffset())
	}
}

func TestSystemIsFollowedByBlankLine(t *testing.T) {
	a := testApp(t, stubModel{"pong"})
	say(a, "q1")
	lines := strings.Split(a.chat.vp.GetContent(), "\n")
	if strings.TrimSpace(lines[1]) != "" || !strings.Contains(lines[2], "q1") {
		t.Fatalf("lines=%q", lines[:3])
	}
}
