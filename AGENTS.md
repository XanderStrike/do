# AGENTS.md

Guide for AI coding agents working on this repo.

## What this is

`do` is a minimal terminal coding agent — a tiny "pi clone" — built in Go
with [Bubble Tea](https://github.com/charmbracelet/bubbletea). It exposes four
tools (read_file, write_file, edit_file, shell) and drives any
OpenAI-compatible chat completions endpoint in an agentic loop.

## Build & run

```sh
go build -o do
./do
```

Config via environment (or `.env`, which is gitignored):

```
LLM_BASE_URL=https://api.openai.com/v1   # or http://localhost:11434/v1 (Ollama), etc.
LLM_API_KEY=sk-...
LLM_MODEL=gpt-4o
```

## Layout

| File      | Responsibility                                                       |
|-----------|----------------------------------------------------------------------|
| `main.go` | Bubble Tea TUI: viewport, textarea, spinner. The agent loop goroutine + system prompt. |
| `llm.go`  | HTTP client for `/chat/completions` with tool calling. `LLMClient`, `Message`, `ToolCall` types. |
| `tools.go`| Tool JSON-schema definitions + `runTool` dispatch + `editFile` helper. |

That's the whole codebase — three files, ~450 LOC. Keep it minimal.

## Architecture

- The TUI (`model`) holds conversation history as a shared `*[]Message` so the
  agent goroutine can mutate it.
- `runAgent` (main.go) is the loop: call `LLMClient.Complete` → if tool calls
  present, execute each via `runTool`, append tool results to history, repeat.
  When the LLM returns plain text with no tool calls, the turn is done.
- A package-level `prog *tea.Program` lets the goroutine send messages
  (`assistantMsg`, `toolStartMsg`, `toolResultMsg`, `errMsg`, `doneMsg`) back
  to the UI.
- `contextWithTimeout` in main.go is a thin wrapper so tools.go can use context
  without importing it directly. This is a stylistic preference, not a hard
  rule — feel free to import context in tools.go if that's cleaner.

## Conventions

- **Minimalism is the point.** Don't add dependencies or abstractions unless
  they're clearly worth it. Three files is the right number of files.
- **Error strings from tools are just strings.** `runTool` returns a string,
  not an error; error messages are prefixed with `"error:"` and fed back to
  the LLM as the tool result. Keep this pattern.
- **Tool results get truncated** before display and before going into the
  conversation (see `truncate` in tools.go and `truncateOneLine` in main.go).
- Styling uses lipgloss color codes: user=63, assistant=36, tool=220,
  result=245, dim=241, error=203.

## Known gotchas

- `truncate` is byte-based, not rune-based — it can split a multi-byte UTF-8
  character.
- No tests exist yet. If you change logic, consider adding a test.
- The agent loop has no turn/cost limit — a misbehaving model could loop
  indefinitely calling tools.
- `edit_file` rejects matches that aren't unique. This is intentional; don't
  relax it without reason.
- Esc/Ctrl+C is ignored while `busy` (mid-turn) so the user can't quit during
  a tool execution.

## Things you might be asked to do

- Fix bugs (check the gotchas above).
- Add a tool (new entry in `tools()`, new case in `runTool`).
- Support streaming responses (would touch `llm.go` and `main.go`).
- Add tests (currently none).
- Adjust the system prompt in `systemPrompt()` in main.go.

## When you make changes

1. `go build -o do` to confirm it compiles.
2. `go vet ./...` for static checks.
3. Summarize what you changed briefly — that matches the style of this project.
