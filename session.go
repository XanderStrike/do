package main

// Session persistence: saves conversation to .do-session in cwd, resumes
// automatically if the file is present.

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const sessionFile = ".do-session"

func sessionPath(cwd string) string {
	return filepath.Join(cwd, sessionFile)
}

// saveSession writes the conversation (excluding the system prompt at index 0)
// to the .do-session file in cwd. Silently ignores errors — best-effort.
func saveSession(cwd string, conv *[]Message) {
	if len(*conv) <= 1 {
		return
	}
	data, err := json.Marshal((*conv)[1:])
	if err != nil {
		return
	}
	os.WriteFile(sessionPath(cwd), data, 0o644)
}

// loadSession reads .do-session from cwd. Returns messages (without system
// prompt) or nil if no session exists or it can't be parsed.
func loadSession(cwd string) []Message {
	data, err := os.ReadFile(sessionPath(cwd))
	if err != nil {
		return nil
	}
	var msgs []Message
	if err := json.Unmarshal(data, &msgs); err != nil {
		return nil
	}
	return msgs
}
