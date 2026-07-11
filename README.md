# do

A super-minimal terminal coding agent (a tiny pi clone) built with Go + [Bubble Tea](https://github.com/charmbracelet/bubbletea).

Four tools, that's it:

- `read_file(path)`
- `edit_file(path, old_string, new_string)`
- `write_file(path, content)`
- `shell(command)`

Talks to any OpenAI-compatible chat completions endpoint (OpenAI, OpenRouter, Ollama, LM Studio, ...).

## Build

```sh
go build -o do
```

## Configure

```sh
export LLM_BASE_URL=https://api.openai.com/v1   # default
export LLM_API_KEY=sk-...
export LLM_MODEL=gpt-4o                          # default
```

Point `LLM_BASE_URL` at a local server (e.g. `http://localhost:11434/v1` for Ollama, `http://localhost:1234/v1` for LM Studio) and drop the key.

## Run

```sh
./do
```

Type a request, hit Enter. The agent loops over tool calls until it's done. Esc stops generation mid-turn, or quits when idle. Ctrl+C always force-quits.

Sessions auto-save to `.do-session` in the working directory and resume on next launch. Delete `.do-session` to start fresh.

## Layout

```
┌─ viewport (conversation: you / assistant / tool calls / results)
│
├─ status line (ready / working spinner)
└─ textarea input
```
