package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/dop251/goja"
	lua "github.com/yuin/gopher-lua"
)

const (
	langJS  = "js"
	langLua = "lua"

	runTimeout    = 10 * time.Second
	maxLuaDepth   = 32
	maxToolOutput = 16 << 10
)

var langs = []string{langJS, langLua}

// repl is one started language runtime. Globals persist between calls of run.
type repl interface {
	run(code string, timeout time.Duration) (string, error)
	save() map[string]json.RawMessage // data globals only
	close()
}

// runtimes holds the runtimes of one session. A runtime starts at its first call,
// or at load with the state from the state file.
type runtimes struct {
	mu      sync.Mutex // a tool call runs off the UI thread
	timeout time.Duration
	rjs     *jsRepl
	rlua    *luaRepl
}

func newRuntimes() *runtimes { return &runtimes{timeout: runTimeout} }

// load starts the runtime of lang with the state in data. Invalid data gives an empty runtime.
func (r *runtimes) load(lang string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var st map[string]json.RawMessage
	json.Unmarshal(data, &st)
	r.start(lang, st)
}

// start replaces the runtime of lang with a new one that has the globals of saved.
func (r *runtimes) start(lang string, saved map[string]json.RawMessage) (repl, error) {
	switch lang {
	case langJS:
		if r.rjs != nil {
			r.rjs.close()
		}
		r.rjs = startJS(saved)
		return r.rjs, nil
	case langLua:
		if r.rlua != nil {
			r.rlua.close()
		}
		r.rlua = startLua(saved)
		return r.rlua, nil
	}
	return nil, fmt.Errorf("unknown language %q", lang)
}

// run executes code and returns the printed output followed by the value of the last expression.
func (r *runtimes) run(lang, code string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var rp repl
	var err error
	switch {
	case lang == langJS && r.rjs != nil:
		rp = r.rjs
	case lang == langLua && r.rlua != nil:
		rp = r.rlua
	default:
		if rp, err = r.start(lang, nil); err != nil {
			return "", err
		}
	}
	return rp.run(code, r.timeout)
}

// save returns the state file content of each started runtime.
func (r *runtimes) save() (map[string][]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := map[string]map[string]json.RawMessage{}
	if r.rjs != nil {
		st[langJS] = r.rjs.save()
	}
	if r.rlua != nil {
		st[langLua] = r.rlua.save()
	}
	out := map[string][]byte{}
	for lang, v := range st {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out[lang] = b
	}
	return out, nil
}

func (r *runtimes) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rjs != nil {
		r.rjs.close()
	}
	if r.rlua != nil {
		r.rlua.close()
	}
	r.rjs, r.rlua = nil, nil
}

func names(keys []string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

type jsRepl struct {
	vm   *goja.Runtime
	base map[string]bool // global names of a fresh runtime: not saved
	out  strings.Builder
}

func startJS(saved map[string]json.RawMessage) *jsRepl {
	r := &jsRepl{vm: goja.New()}
	vm := r.vm
	console := vm.NewObject()
	console.Set("log", func(c goja.FunctionCall) goja.Value {
		parts := make([]string, len(c.Arguments))
		for i, a := range c.Arguments {
			parts[i] = a.String()
		}
		r.out.WriteString(strings.Join(parts, " "))
		r.out.WriteString("\n")
		return goja.Undefined()
	})
	vm.Set("console", console)
	r.base = names(vm.GlobalObject().Keys())
	parse, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("parse"))
	for name, raw := range saved {
		if v, err := parse(goja.Undefined(), vm.ToValue(string(raw))); err == nil {
			vm.Set(name, v)
		}
	}
	return r
}

func (r *jsRepl) run(code string, timeout time.Duration) (string, error) {
	r.out.Reset()
	t := time.AfterFunc(timeout, func() { r.vm.Interrupt("timeout") })
	v, err := r.vm.RunString(code)
	t.Stop()
	r.vm.ClearInterrupt()
	out := r.out.String()
	if _, ok := errors.AsType[*goja.InterruptedError](err); ok {
		return out, fmt.Errorf("timeout after %v", timeout)
	}
	if err != nil {
		return out, err
	}
	if v == nil || goja.IsUndefined(v) {
		return out, nil
	}
	if o, ok := v.(*goja.Object); ok {
		if _, isFn := goja.AssertFunction(o); !isFn {
			if b, err := json.Marshal(o.Export()); err == nil {
				return out + string(b), nil
			}
		}
	}
	return out + v.String(), nil
}

func (r *jsRepl) save() map[string]json.RawMessage {
	st := map[string]json.RawMessage{}
	g := r.vm.GlobalObject()
	for _, k := range g.Keys() {
		v := g.Get(k)
		if r.base[k] || v == nil {
			continue
		}
		if _, isFn := goja.AssertFunction(v); isFn {
			continue
		}
		if b, err := json.Marshal(v.Export()); err == nil {
			st[k] = b
		}
	}
	return st
}

func (r *jsRepl) close() {}

type luaRepl struct {
	L    *lua.LState
	base map[string]bool
	out  strings.Builder
}

func startLua(saved map[string]json.RawMessage) *luaRepl {
	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	r := &luaRepl{L: L}
	for name, open := range map[string]lua.LGFunction{
		lua.BaseLibName: lua.OpenBase, lua.TabLibName: lua.OpenTable,
		lua.StringLibName: lua.OpenString, lua.MathLibName: lua.OpenMath,
	} {
		L.Push(L.NewFunction(open))
		L.Push(lua.LString(name))
		L.Call(1, 0)
	}
	for _, g := range []string{"dofile", "loadfile", "require", "module"} {
		L.SetGlobal(g, lua.LNil)
	}
	L.SetGlobal("print", L.NewFunction(func(L *lua.LState) int {
		parts := make([]string, L.GetTop())
		for i := range parts {
			parts[i] = L.ToStringMeta(L.Get(i + 1)).String()
		}
		r.out.WriteString(strings.Join(parts, "\t"))
		r.out.WriteString("\n")
		return 0
	}))
	r.base = map[string]bool{}
	L.G.Global.ForEach(func(k, _ lua.LValue) { r.base[k.String()] = true })
	for name, raw := range saved {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			L.SetGlobal(name, toLua(L, v))
		}
	}
	return r
}

func (r *luaRepl) run(code string, timeout time.Duration) (string, error) {
	L := r.L
	r.out.Reset()
	fn, err := L.LoadString("return " + code)
	if err != nil {
		if fn, err = L.LoadString(code); err != nil {
			return "", err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	L.SetContext(ctx)
	defer L.RemoveContext()
	L.Push(fn)
	err = L.PCall(0, lua.MultRet, nil)
	out := r.out.String()
	if ctx.Err() != nil {
		L.SetTop(0)
		return out, fmt.Errorf("timeout after %v", timeout)
	}
	if err != nil {
		L.SetTop(0)
		return out, err
	}
	vals := make([]string, L.GetTop())
	for i := range vals {
		vals[i] = L.ToStringMeta(L.Get(i + 1)).String()
	}
	L.SetTop(0)
	return out + strings.Join(vals, "\t"), nil
}

func (r *luaRepl) save() map[string]json.RawMessage {
	st := map[string]json.RawMessage{}
	r.L.G.Global.ForEach(func(k, v lua.LValue) {
		name, ok := k.(lua.LString)
		if !ok || r.base[string(name)] {
			return
		}
		if x, ok := fromLua(v, 0, map[*lua.LTable]bool{}); ok {
			if b, err := json.Marshal(x); err == nil {
				st[string(name)] = b
			}
		}
	})
	return st
}

func (r *luaRepl) close() { r.L.Close() }

// fromLua converts data values; it reports false for functions, userdata, threads, cycles and too deep tables.
func fromLua(v lua.LValue, depth int, seen map[*lua.LTable]bool) (any, bool) {
	switch v := v.(type) {
	case lua.LBool:
		return bool(v), true
	case lua.LNumber:
		return float64(v), true
	case lua.LString:
		return string(v), true
	case *lua.LTable:
		if seen[v] || depth >= maxLuaDepth {
			return nil, false
		}
		seen[v] = true
		defer delete(seen, v)
		n, count := v.MaxN(), 0
		v.ForEach(func(lua.LValue, lua.LValue) { count++ })
		if n > 0 && n == count {
			arr := []any{}
			for i := 1; i <= n; i++ {
				if x, ok := fromLua(v.RawGetInt(i), depth+1, seen); ok {
					arr = append(arr, x)
				} else {
					arr = append(arr, nil)
				}
			}
			return arr, true
		}
		obj := map[string]any{}
		v.ForEach(func(k, e lua.LValue) {
			if x, ok := fromLua(e, depth+1, seen); ok {
				obj[k.String()] = x
			}
		})
		return obj, true
	}
	return nil, false
}

func toLua(L *lua.LState, v any) lua.LValue {
	switch v := v.(type) {
	case bool:
		return lua.LBool(v)
	case float64:
		return lua.LNumber(v)
	case string:
		return lua.LString(v)
	case []any:
		t := L.NewTable()
		for _, e := range v {
			t.Append(toLua(L, e))
		}
		return t
	case map[string]any:
		t := L.NewTable()
		for k, e := range v {
			if n, err := strconv.Atoi(k); err == nil {
				t.RawSetInt(n, toLua(L, e))
			} else {
				t.RawSetString(k, toLua(L, e))
			}
		}
		return t
	}
	return lua.LNil
}

const maxToolRounds = 20

var errTooManyToolCalls = errors.New("too many tool calls")

var toolLang = map[string]string{"REPLjs": langJS, "REPLlua": langLua}

func toolInfos() []*schema.ToolInfo {
	limits := "Globals persist between calls in this session and are saved with it; only data (numbers, strings, booleans, null, arrays, objects) survives a restart, functions do not. Each call stops after 10 seconds. No file, network, or process access."
	code := func(desc string) *schema.ParamsOneOf {
		return schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"code": {Type: schema.String, Desc: desc, Required: true},
		})
	}
	return []*schema.ToolInfo{
		{
			Name: "REPLjs", Desc: "Run JavaScript code. Returns console.log output and the value of the last expression. Use var for globals. " +
				"MANDATORY: You MUST use var for all variable declarations that need to persist between tool calls, instead they cannot be recovered. " + limits,
			ParamsOneOf: code("JavaScript code"),
		},
		{
			Name:        "REPLlua",
			Desc:        "Run Lua code. Returns print output and the value of the last expression. Libraries: base, table, string, math. " + limits,
			ParamsOneOf: code("Lua code"),
		},
	}
}

// withTools binds the REPL tools to a model that supports tools; other models stay as they are.
func withTools(cm model.BaseChatModel) (model.BaseChatModel, error) {
	tcm, ok := cm.(model.ToolCallingChatModel)
	if !ok {
		return cm, nil
	}
	return tcm.WithTools(toolInfos())
}

// toolTurn joins the streamed chunks. It returns the answer text, and the assistant message
// with tool calls or nil when the model gave a final answer. rounds counts the tool rounds
// of the current user message.
func toolTurn(chunks []*schema.Message, rounds *int) (string, *schema.Message, error) {
	if len(chunks) == 0 {
		return "", nil, nil
	}
	msg, err := schema.ConcatMessages(chunks)
	if err != nil {
		return "", nil, err
	}
	if len(msg.ToolCalls) == 0 {
		return msg.Content, nil, nil
	}
	if *rounds++; *rounds >= maxToolRounds {
		return "", nil, errTooManyToolCalls
	}
	return msg.Content, &schema.Message{Role: schema.Assistant, Content: msg.Content, ReasoningContent: msg.ReasoningContent, ToolCalls: msg.ToolCalls}, nil
}

type toolDoneMsg struct{ results []*schema.Message } // one tool message for each call, in order

// runTools runs the calls of msg in order, off the UI thread.
func runTools(rt *runtimes, msg *schema.Message) tea.Cmd {
	return func() tea.Msg {
		var out []*schema.Message
		for _, c := range msg.ToolCalls {
			out = append(out, schema.ToolMessage(callTool(rt, c), c.ID, schema.WithToolName(c.Function.Name)))
		}
		return toolDoneMsg{out}
	}
}

func callTool(rt *runtimes, c schema.ToolCall) string {
	lang, ok := toolLang[c.Function.Name]
	if !ok {
		return "error: unknown tool " + c.Function.Name
	}
	var args struct{ Code string }
	if err := json.Unmarshal([]byte(c.Function.Arguments), &args); err != nil {
		return "error: invalid arguments: " + err.Error()
	}
	out, err := rt.run(lang, args.Code)
	if err != nil {
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += "error: " + err.Error()
	}
	if len(out) > maxToolOutput {
		out = out[:maxToolOutput] + "\n[output cut at 16 KiB]"
	}
	return out
}

// toolCode returns the code of a call, or the raw arguments if they are not valid.
func toolCode(c schema.ToolCall) string {
	var args struct{ Code string }
	if json.Unmarshal([]byte(c.Function.Arguments), &args) != nil {
		return c.Function.Arguments
	}
	return args.Code
}
