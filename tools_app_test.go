package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// toolModel calls REPLjs with the user text as code and then answers with the tool result.
// With loop set, it calls the tool in each round.
type toolModel struct{ loop bool }

func (m toolModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, nil
}

func (m toolModel) Stream(_ context.Context, h []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	last := h[len(h)-1]
	if last.Role == schema.Tool && !m.loop {
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("got "+last.Content, nil)}), nil
	}
	code := last.Content
	for i := len(h) - 1; h[i].Role != schema.User; i-- {
		code = h[i-1].Content
	}
	args, _ := json.Marshal(map[string]string{"code": code})
	call := schema.ToolCall{ID: "c1", Function: schema.FunctionCall{Name: "REPLjs", Arguments: string(args)}}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", []schema.ToolCall{call})}), nil
}

// drive runs cmd and feeds the chat messages it returns back to the app until the reply ends.
func drive(a *app, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			drive(a, c)
		}
	case streamMsg, chunkMsg, doneMsg, toolDoneMsg:
		_, next := a.Update(msg)
		drive(a, next)
	}
}

func ask(a *app, text string) {
	a.input.SetValue(text)
	_, cmd := a.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	drive(a, cmd)
}

func TestToolThenAnswer(t *testing.T) {
	a := testApp(t, toolModel{})
	ask(a, "6 * 7")
	var roles []schema.RoleType
	for _, m := range a.history {
		roles = append(roles, m.Role)
	}
	want := []schema.RoleType{schema.System, schema.User, schema.Assistant, schema.Tool, schema.Assistant}
	if len(roles) != len(want) || a.history[3].Content != "42" || a.history[4].Content != "got 42" {
		t.Fatalf("history roles=%v last=%q", roles, a.history[len(a.history)-1].Content)
	}
	saved := fileMessages(t, a, a.sessionID)
	if len(saved) != 5 || len(saved[2].ToolCalls) != 1 || saved[3].ToolCallID != "c1" {
		t.Fatalf("saved=%v", saved)
	}
	if !strings.Contains(a.entries[2].text, "6 * 7") || a.entries[3].text != "result:\n42" || lastEntry(a).role != answer {
		t.Fatalf("entries=%v", a.entries)
	}
	if got := entriesFrom(saved); len(got) != len(a.entries) || got[2] != a.entries[2] || got[3] != a.entries[3] {
		t.Fatalf("reopened entries=%v", got)
	}
}

func TestToolRoundLimit(t *testing.T) {
	a := testApp(t, toolModel{loop: true})
	ask(a, "1")
	if a.busy || lastEntry(a).text != "error: too many tool calls" {
		t.Fatalf("busy=%v last=%v", a.busy, lastEntry(a))
	}
	if a.history[len(a.history)-1].Role != schema.Tool {
		t.Fatal("history must end with a tool result")
	}
	a.chatModel = toolModel{}
	ask(a, "2")
	if lastEntry(a).text != "got 2" {
		t.Fatalf("new message after the limit: %v", lastEntry(a))
	}
}

func TestStateFollowsSession(t *testing.T) {
	a := testApp(t, toolModel{})
	ask(a, "var x = 5")
	first := a.sessionID
	say(a, "/new")
	ask(a, "typeof x")
	if lastEntry(a).text != "got undefined" {
		t.Fatalf("new session sees old state: %v", lastEntry(a))
	}
	a.openSession(first)
	ask(a, "x")
	if lastEntry(a).text != "got 5" {
		t.Fatalf("restored: %v", lastEntry(a))
	}

	say(a, "/fork")
	fork := a.sessionID
	ask(a, "x = 9")
	a.openSession(first)
	ask(a, "x")
	if lastEntry(a).text != "got 5" {
		t.Fatalf("fork changed the original: %v", lastEntry(a))
	}

	a.store.Delete(fork)
	if _, err := os.Stat(a.store.statePath(fork, langJS)); !os.IsNotExist(err) {
		t.Fatalf("state file left: %v", err)
	}
}

func TestDeleteCurrentSessionKeepsNoState(t *testing.T) {
	a := testApp(t, toolModel{})
	ask(a, "var x = 1")
	id := a.sessionID
	a.saveState()
	say(a, "/sessions")
	pressText(a, "d")
	pressText(a, "y")
	if _, err := os.Stat(a.store.statePath(id, langJS)); !os.IsNotExist(err) {
		t.Fatalf("state file written again: %v", err)
	}
}

func TestWithToolsSkipsACP(t *testing.T) {
	var m model.BaseChatModel = &acpModel{}
	got, err := withTools(m)
	if err != nil || got != m {
		t.Fatal("acp model must stay as it is")
	}
}
