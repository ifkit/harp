# Harp

Terminal AI chat (default API endpoint as for LM Studio:`localhost:1234`). Built on [eino](https://github.com/cloudwego/eino) and Bubble Tea v2.

```
go run . --openai https://api.anthropic.com/v1 --model claude-sonnet-5-5 --key <your_key>
```

Use an ACP agent instead (the agent must speak [ACP](https://agentclientprotocol.com) over stdio; tool permission requests are denied):

```
go run . --acp "npx @zed-industries/claude-code-acp"
```

## Sessions

Each session is saved as a JSONL file in `~/.local/state/harp` (`$XDG_STATE_HOME/harp` when set). Harp starts with no session; the first message creates one.

| Command | Action |
| --- | --- |
| `/sessions`    | Pick a saved session to open (`d` deletes it, `Esc` closes) |
| `/new`         | Return to an empty state; the next message starts a new session |
| `/fork`        | Copy the current history into a new session and switch to it |
| `/rewind`      | Pick a message; delete every message after it, after confirmation |
| `/fork-rewind` | Pick a message; copy the history up to it into a new session |

In the pickers, Up/Down or `j`/`k` select, Ctrl+N/Ctrl+P (3 lines) and the mouse wheel scroll the preview, like the chat, Enter or `o` selects, Esc closes.

With `--acp`, only `/new` and `/sessions` (list and delete) work: the agent keeps the history.

## Code mode

The model can run code with two tools: `REPLjs` (JavaScript, [goja](https://github.com/dop251/goja)) and `REPLlua` (Lua, [gopher-lua](https://github.com/yuin/gopher-lua)). Each session has its own runtimes. Their state survives a restart of Harp.

With `--acp`, there are no tools and REPL is not supported: the agent has its own.
