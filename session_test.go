package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestStoreAppendLoad(t *testing.T) {
	s := &store{t.TempDir() + "/sub"}
	if err := s.Append("a", schema.UserMessage("one")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("a", schema.AssistantMessage("two", nil)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(s.path("a"))
	if n := len(splitLines(b)); n != 2 {
		t.Fatalf("lines=%d", n)
	}
	msgs, err := s.Load("a")
	if err != nil || len(msgs) != 2 || msgs[0].Content != "one" || msgs[1].Content != "two" {
		t.Fatalf("msgs=%v err=%v", msgs, err)
	}
}

func splitLines(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, string(b[start:i]))
			start = i + 1
		}
	}
	return out
}

func TestStoreLoadSkipsCorruptLine(t *testing.T) {
	s := &store{t.TempDir()}
	s.Append("a", schema.UserMessage("one"))
	f, _ := os.OpenFile(s.path("a"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("not json\n")
	f.Close()
	s.Append("a", schema.UserMessage("two"))
	msgs, err := s.Load("a")
	if err != nil || len(msgs) != 2 {
		t.Fatalf("msgs=%v err=%v", msgs, err)
	}
}

func TestStoreListDelete(t *testing.T) {
	s := &store{t.TempDir()}
	now := time.Now()
	for i, id := range []string{"old", "mid", "new"} {
		s.Append(id, schema.SystemMessage("sys"), schema.UserMessage("title "+id))
		ts := now.Add(time.Duration(i) * time.Hour)
		os.Chtimes(s.path(id), ts, ts)
	}
	list, err := s.List()
	if err != nil || len(list) != 3 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	if list[0].ID != "new" || list[2].ID != "old" || list[0].Title != "title new" {
		t.Fatalf("list=%v", list)
	}
	if err := s.Delete("new"); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.List(); len(list) != 2 || list[0].ID != "mid" {
		t.Fatalf("list=%v", list)
	}
}

func TestStoreRewrite(t *testing.T) {
	s := &store{t.TempDir()}
	s.Append("a", schema.UserMessage("1"), schema.UserMessage("2"), schema.UserMessage("3"))
	msgs, _ := s.Load("a")
	if err := s.Rewrite("a", msgs[:2]); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Load("a")
	if len(got) != 2 || got[1].Content != "2" {
		t.Fatalf("got=%v", got)
	}
	if files, _ := os.ReadDir(s.dir); len(files) != 1 {
		t.Fatalf("leftover files: %v", files)
	}
}

func TestStoreState(t *testing.T) {
	s := &store{t.TempDir()}
	s.Append("a", schema.UserMessage("1"))
	if _, err := s.LoadState("a", langJS); !os.IsNotExist(err) {
		t.Fatalf("missing state: %v", err)
	}
	for _, lang := range langs {
		if err := s.SaveState("a", lang, []byte(lang)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(s.dir, "a.repl.lua.json")); err != nil {
		t.Fatal(err)
	}
	if b, err := s.LoadState("a", langJS); err != nil || string(b) != langJS {
		t.Fatalf("b=%s err=%v", b, err)
	}
	if list, _ := s.List(); len(list) != 1 {
		t.Fatalf("list=%v", list)
	}
	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if files, _ := os.ReadDir(s.dir); len(files) != 0 {
		t.Fatalf("leftover files: %v", files)
	}
}
