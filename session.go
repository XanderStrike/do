package main

// Session persistence: saves conversation to .do-session in cwd, resumes
// automatically if the file is present.

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const sessionFile = ".do-session"

// sessionData is the persisted session format: conversation messages (sans
// system prompt) plus the last-known token usage from the API.
type sessionData struct {
	Messages []Message `json:"messages"`
	Usage    *Usage    `json:"usage,omitempty"`
}

func sessionPath(cwd string) string {
	return filepath.Join(cwd, sessionFile)
}

// saveSession writes the conversation (excluding the system prompt at index 0)
// and the last-known usage to the .do-session file in cwd. Silently ignores
// errors — best-effort.
func saveSession(cwd string, conv *[]Message, usage *Usage) {
	if len(*conv) <= 1 {
		return
	}
	data, err := json.Marshal(sessionData{
		Messages: (*conv)[1:],
		Usage:    usage,
	})
	if err != nil {
		return
	}
	// Atomic write: write to a temp file then rename, so a crash mid-write
	// can't leave a truncated session that would nuke history on resume.
	path := sessionPath(cwd)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	os.Rename(tmp, path)
}

// loadSession reads .do-session from cwd. Returns messages (without system
// prompt) and last-known usage, or nil if no session exists or it can't be
// parsed. Handles both the current sessionData format and legacy bare []Message
// arrays from older versions.
func loadSession(cwd string) ([]Message, *Usage) {
	data, err := os.ReadFile(sessionPath(cwd))
	if err != nil {
		return nil, nil
	}

	// Try the current format first.
	var sd sessionData
	if err := json.Unmarshal(data, &sd); err == nil && sd.Messages != nil {
		return trimForResume(sd.Messages), sd.Usage
	}

	// Fall back to legacy bare []Message format.
	var msgs []Message
	if err := json.Unmarshal(data, &msgs); err != nil {
		return nil, nil
	}
	return trimForResume(msgs), nil
}

// trimForResume drops trailing messages that would leave the conversation in
// an unresumable state — specifically an assistant tool_calls message whose
// matching tool results are missing (e.g. the turn was cancelled mid-loop).
// The OpenAI API returns a 400 when tool_calls aren't followed by results.
func trimForResume(msgs []Message) []Message {
	for len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		switch last.Role {
		case "user":
			return msgs
		case "assistant":
			if len(last.ToolCalls) == 0 {
				return msgs
			}
			msgs = msgs[:len(msgs)-1]
		case "tool":
			// Count trailing tool results and the preceding assistant's calls.
			n := 1
			for len(msgs)-1-n >= 0 && msgs[len(msgs)-1-n].Role == "tool" {
				n++
			}
			a := len(msgs) - 1 - n
			if a < 0 || msgs[a].Role != "assistant" || len(msgs[a].ToolCalls) <= n {
				return msgs // complete (or can't tell) — leave as-is
			}
			msgs = msgs[:a] // incomplete — drop the partial sequence
		default:
			return msgs
		}
	}
	return msgs
}
