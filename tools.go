package main

// Tool definitions and dispatch for the three tools the agent can use:
// read_file, write_file, and shell.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Tool is an OpenAI-style function tool definition.
type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type Function struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

func tools() []Tool {
	return []Tool{
		{
			Type: "function",
			Function: Function{
				Name:        "read_file",
				Description: "Read the full contents of a file at the given path. Returns the file contents as text.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Path to the file to read. Relative paths are resolved against the working directory.",
						},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			Type: "function",
			Function: Function{
				Name:        "write_file",
				Description: "Write content to a file at the given path, creating it if it doesn't exist or overwriting it if it does. Creates parent directories as needed.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Path to the file to write.",
						},
						"content": map[string]any{
							"type":        "string",
							"description": "The full content to write to the file.",
						},
					},
					"required": []string{"path", "content"},
				},
			},
		},
		{
			Type: "function",
			Function: Function{
				Name:        "shell",
				Description: "Run a shell command via `bash -c`. Returns combined stdout and stderr. Use for running programs, git, listing files, etc. Commands run in the working directory.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"description": "The shell command to execute.",
						},
					},
					"required": []string{"command"},
				},
			},
		},
	}
}

// runTool executes a tool call by name with raw JSON arguments and returns a
// string result (or an error message, which is also just a string for the LLM).
func runTool(name, argsJSON string) string {
	switch name {
	case "read_file":
		var a struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
			return "error parsing arguments: " + err.Error()
		}
		data, err := os.ReadFile(a.Path)
		if err != nil {
			return "error: " + err.Error()
		}
		return string(data)

	case "write_file":
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
			return "error parsing arguments: " + err.Error()
		}
		if err := os.MkdirAll(parentDir(a.Path), 0o755); err != nil {
			return "error creating dirs: " + err.Error()
		}
		if err := os.WriteFile(a.Path, []byte(a.Content), 0o644); err != nil {
			return "error: " + err.Error()
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path)

	case "shell":
		var a struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
			return "error parsing arguments: " + err.Error()
		}
		ctx, cancel := contextWithTimeout(60 * time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "-c", a.Command)
		out, err := cmd.CombinedOutput()
		s := strings.TrimSpace(string(out))
		if err != nil {
			if s == "" {
				return "error: " + err.Error()
			}
			return s + "\n[error: " + err.Error() + "]"
		}
		if s == "" {
			return "(no output)"
		}
		return truncate(s, 20000)

	default:
		return "unknown tool: " + name
	}
}

func parentDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		if i == 0 {
			return "/"
		}
		return p[:i]
	}
	// No slash: treat as a file in cwd. MkdirAll("") is a no-op anyway.
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n...[truncated]"
}
