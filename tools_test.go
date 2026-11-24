package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func mustRun(t *testing.T, r *runtimes, lang, code string) string {
	t.Helper()
	out, err := r.run(lang, code)
	if err != nil {
		t.Fatalf("%s %q: %v", lang, code, err)
	}
	return out
}

func TestRunKeepsVariables(t *testing.T) {
	r := newRuntimes()
	defer r.close()
	mustRun(t, r, langJS, "var n = 2")
	if got := mustRun(t, r, langJS, "n * 21"); got != "42" {
		t.Fatalf("js: %q", got)
	}
	mustRun(t, r, langLua, "n = 2")
	if got := mustRun(t, r, langLua, "n * 21"); got != "42" {
		t.Fatalf("lua: %q", got)
	}
}

func TestRuntimesAreSeparate(t *testing.T) {
	r := newRuntimes()
	defer r.close()
	mustRun(t, r, langJS, "var only = 1")
	if got := mustRun(t, r, langLua, "only"); got != "nil" {
		t.Fatalf("lua: %q", got)
	}
}

func TestRunPrintsOutput(t *testing.T) {
	r := newRuntimes()
	defer r.close()
	if got := mustRun(t, r, langJS, `console.log("a", 1); 5`); got != "a 1\n5" {
		t.Fatalf("js: %q", got)
	}
	if got := mustRun(t, r, langLua, `print("a", 1)`); got != "a\t1\n" {
		t.Fatalf("lua: %q", got)
	}
}

func TestRunError(t *testing.T) {
	r := newRuntimes()
	defer r.close()
	if _, err := r.run(langJS, `throw new Error("boom")`); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("js: %v", err)
	}
	if _, err := r.run(langLua, `error("boom")`); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("lua: %v", err)
	}
}

func TestRunTimeoutThenReuse(t *testing.T) {
	r := newRuntimes()
	defer r.close()
	r.timeout = 50 * time.Millisecond
	for lang, loop := range map[string]string{langJS: "for(;;){}", langLua: "while true do end"} {
		if _, err := r.run(lang, loop); err == nil || !strings.Contains(err.Error(), "timeout") {
			t.Fatalf("%s: %v", lang, err)
		}
		if got := mustRun(t, r, lang, "1 + 1"); got != "2" {
			t.Fatalf("%s after timeout: %q", lang, got)
		}
	}
}

func TestNoHostAccess(t *testing.T) {
	r := newRuntimes()
	defer r.close()
	for _, g := range []string{"io", "os", "package", "dofile", "loadfile", "require"} {
		if got := mustRun(t, r, langLua, g); got != "nil" {
			t.Fatalf("lua %s: %q", g, got)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	r := newRuntimes()
	mustRun(t, r, langJS, `var data = {a: 1, b: [1, "x", {c: true}]}; var f = function() {}`)
	mustRun(t, r, langLua, `arr = {1, 2, 3}; obj = {k = "v", n = {m = 1}}; fn = function() end`)
	files, st := saveAll(t, r)
	r.close()
	if _, ok := st[langJS]["f"]; ok {
		t.Fatal("js function saved")
	}
	if _, ok := st[langLua]["fn"]; ok {
		t.Fatal("lua function saved")
	}
	if s := string(st[langLua]["arr"]); s != "[1,2,3]" {
		t.Fatalf("lua array: %s", s)
	}

	r2 := newRuntimes()
	defer r2.close()
	for lang, b := range files {
		r2.load(lang, b)
	}
	if got := mustRun(t, r2, langJS, "data.b[2].c && data.a"); got != "1" {
		t.Fatalf("js: %q", got)
	}
	if got := mustRun(t, r2, langLua, "obj.n.m + #arr"); got != "4" {
		t.Fatalf("lua: %q", got)
	}
}

func TestSaveSkipsCycleAndDeepValues(t *testing.T) {
	r := newRuntimes()
	defer r.close()
	mustRun(t, r, langJS, "var cyc = {}; cyc.self = cyc; var ok = 1")
	mustRun(t, r, langLua, "cyc = {}; cyc.self = cyc; deep = {}; local t = deep; for i = 1, 40 do t.x = {}; t = t.x end; ok = 1")
	_, st := saveAll(t, r)
	if _, ok := st[langJS]["cyc"]; ok {
		t.Fatal("js cycle saved")
	}
	if string(st[langJS]["ok"]) != "1" || string(st[langLua]["ok"]) != "1" {
		t.Fatalf("plain values lost: %v", st)
	}
	if s := string(st[langLua]["cyc"]); s != "{}" {
		t.Fatalf("lua cycle: %s", s)
	}
	if strings.Count(string(st[langLua]["deep"]), "{") > 33 {
		t.Fatal("lua depth limit not applied")
	}
}

func TestLoadInvalidGivesEmptyRuntime(t *testing.T) {
	r := newRuntimes()
	defer r.close()
	r.load(langJS, []byte("not json"))
	if got := mustRun(t, r, langJS, "typeof data"); got != "undefined" {
		t.Fatalf("js: %q", got)
	}
}

func TestRuntimeStartsOnFirstUse(t *testing.T) {
	r := newRuntimes()
	defer r.close()
	r.load(langJS, []byte(`{"a":1}`))
	r.load(langLua, []byte(`{"b":2}`))
	if r.js != nil || r.lua != nil {
		t.Fatal("load created a runtime")
	}
	if got := mustRun(t, r, langJS, "a"); got != "1" {
		t.Fatalf("js: %q", got)
	}
	if r.lua != nil {
		t.Fatal("lua runtime created by a js call")
	}
	if _, st := saveAll(t, r); string(st[langLua]["b"]) != "2" {
		t.Fatalf("saved lua state lost: %v", st)
	}
}

func TestSaveOnlyUsedLanguages(t *testing.T) {
	r := newRuntimes()
	defer r.close()
	if files, _ := saveAll(t, r); len(files) != 0 {
		t.Fatalf("unused runtimes saved: %v", files)
	}
	mustRun(t, r, langLua, "x = 1")
	if files, _ := saveAll(t, r); len(files) != 1 || files[langLua] == nil {
		t.Fatalf("files=%v", files)
	}
}

// saveAll returns the state file of each language and its parsed content.
func saveAll(t *testing.T, r *runtimes) (map[string][]byte, map[string]map[string]json.RawMessage) {
	t.Helper()
	files, err := r.save()
	if err != nil {
		t.Fatal(err)
	}
	st := map[string]map[string]json.RawMessage{}
	for lang, b := range files {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		st[lang] = m
	}
	return files, st
}
