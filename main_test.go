package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/cloudwego/eino/schema"
)

// fakeAgent is a minimal ACP agent. The test binary runs it when FAKE_ACP is set.
func fakeAgent() {
	sc := bufio.NewScanner(os.Stdin)
	sessions := 0
	for sc.Scan() {
		var m struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
			} `json:"params"`
		}
		json.Unmarshal(sc.Bytes(), &m)
		switch m.Method {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":1}}`+"\n", m.ID)
		case "session/new":
			sessions++
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"sessionId":"s%d"}}`+"\n", m.ID, sessions)
		case "session/prompt":
			if log := os.Getenv("FAKE_ACP_LOG"); log != "" {
				f, _ := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
				fmt.Fprintln(f, m.Params.SessionID)
				f.Close()
			}
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hm"}}}}`)
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"he"}}}}`)
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"llo"}}}}`)
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"stopReason":"end_turn"}}`+"\n", m.ID)
		}
	}
}

func TestMain(m *testing.M) {
	if os.Getenv("FAKE_ACP") != "" {
		fakeAgent()
		return
	}
	os.Exit(m.Run())
}

func TestACPStream(t *testing.T) {
	t.Setenv("FAKE_ACP", "1")
	m, err := newACP(context.Background(), []string{os.Args[0]}, ".")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	for range 2 { // second turn proves the session survives
		sr, err := m.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		if err != nil {
			t.Fatal(err)
		}
		var think, text string
		for {
			c, err := sr.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			think += c.ReasoningContent
			text += c.Content
		}
		if think != "hm" || text != "hello" {
			t.Fatalf("think=%q text=%q", think, text)
		}
	}
}

func TestACPSessionCommands(t *testing.T) {
	t.Setenv("FAKE_ACP", "1")
	log := t.TempDir() + "/prompts"
	t.Setenv("FAKE_ACP_LOG", log)
	m, err := newACP(context.Background(), []string{os.Args[0]}, ".")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	a := testApp(t, m)

	say(a, "hi")
	id := a.session.ID
	for _, cmd := range []string{"/fork", "/rewind", "/fork-rewind"} {
		say(a, cmd)
		if e := lastEntry(a); e.role != failure || !strings.Contains(e.text, "--acp") {
			t.Fatalf("%s: %v", cmd, e)
		}
	}
	if a.pick != nil || a.session.ID != id || len(fileMessages(t, a, id)) != 3 || len(files(t, a)) != 1 {
		t.Fatal("refused commands changed something")
	}

	say(a, "/new")
	say(a, "hi")
	if a.session.ID == id {
		t.Fatal("/new must start a new saved session")
	}
	b, _ := os.ReadFile(log)
	if string(b) != "s1\ns2\n" {
		t.Fatalf("prompt sessions=%q", b)
	}

	say(a, "/sessions")
	press(a, tea.Key{Code: tea.KeyEnter})
	if e := lastEntry(a); e.role != failure || !strings.Contains(e.text, "--acp") {
		t.Fatalf("open session: %v", e)
	}
}
