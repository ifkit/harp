package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// store keeps one JSONL file per session: one schema.Message per line.
type store struct{ dir string }

type sessionInfo struct {
	ID       string
	Modified time.Time
	Title    string // first user message
}

func defaultStore() (*store, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return &store{filepath.Join(dir, "harp")}, nil
}

func newSessionID() string {
	var b [2]byte
	rand.Read(b[:])
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

func (s *store) path(id string) string { return filepath.Join(s.dir, id+".jsonl") }

func encodeLines(msgs []*schema.Message) ([]byte, error) {
	var buf []byte
	for _, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		buf = append(append(buf, b...), '\n')
	}
	return buf, nil
}

// Append adds messages to the session file and creates the file and the directory when needed.
func (s *store) Append(id string, msgs ...*schema.Message) error {
	buf, err := encodeLines(msgs)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path(id), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Load reads the session and skips lines that are not valid messages.
func (s *store) Load(id string) ([]*schema.Message, error) {
	f, err := os.Open(s.path(id))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var msgs []*schema.Message
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var m schema.Message
		if json.Unmarshal(sc.Bytes(), &m) == nil && m.Role != "" {
			msgs = append(msgs, &m)
		}
	}
	return msgs, sc.Err()
}

// Rewrite replaces the session file with msgs. A temporary file keeps the old file whole on a crash.
func (s *store) Rewrite(id string, msgs []*schema.Message) error {
	buf, err := encodeLines(msgs)
	if err != nil {
		return err
	}
	return s.replace(s.path(id), buf)
}

func (s *store) statePath(id, lang string) string {
	return filepath.Join(s.dir, id+".repl."+lang+".json")
}

// SaveState replaces the state file of one runtime of the session.
func (s *store) SaveState(id, lang string, data []byte) error {
	return s.replace(s.statePath(id, lang), data)
}

func (s *store) LoadState(id, lang string) ([]byte, error) { return os.ReadFile(s.statePath(id, lang)) }

// replace writes buf to path through a temporary file, so a crash keeps the old file whole.
func (s *store) replace(path string, buf []byte) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".rewrite-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(buf)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

func (s *store) Delete(id string) error {
	if err := os.Remove(s.path(id)); err != nil {
		return err
	}
	for _, lang := range langs {
		if err := os.Remove(s.statePath(id, lang)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// List returns the sessions, newest first.
func (s *store) List() ([]sessionInfo, error) {
	files, err := filepath.Glob(filepath.Join(s.dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	var out []sessionInfo
	for _, name := range files {
		st, err := os.Stat(name)
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(name), ".jsonl")
		info := sessionInfo{ID: id, Modified: st.ModTime()}
		if msgs, err := s.Load(id); err == nil {
			for _, m := range msgs {
				if m.Role == schema.User {
					info.Title = m.Content
					break
				}
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}
