package main

// Tool definitions and dispatch for the four tools the agent can use:
// read_file, write_file, edit_file, and shell.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
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
				Description: "Read the contents of a file at the given path. Returns the file contents as text. Optionally pass start_line and/or end_line (1-based, inclusive) to read a specific line range; without them the entire file is returned.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Path to the file to read. Relative paths are resolved against the working directory.",
						},
						"start_line": map[string]any{
							"type":        "integer",
							"description": "1-based line number to start reading from (inclusive). Omit to start at the beginning.",
						},
						"end_line": map[string]any{
							"type":        "integer",
							"description": "1-based line number to stop reading at (inclusive). Omit to read to the end of the file.",
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
				Name:        "edit_file",
				Description: "Edit a file by replacing an exact string. The old_string must appear exactly once in the file (unique match) so the edit is unambiguous. The new_string replaces it. Useful for small surgical edits without rewriting the whole file. Prefer read_file before editing.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Path to the file to edit. Relative paths are resolved against the working directory.",
						},
						"old_string": map[string]any{
							"type":        "string",
							"description": "The exact text to find in the file. Must match uniquely (exactly one occurrence).",
						},
						"new_string": map[string]any{
							"type":        "string",
							"description": "The replacement text that replaces old_string.",
						},
					},
					"required": []string{"path", "old_string", "new_string"},
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
func runTool(ctx context.Context, name, argsJSON string) string {
	switch name {
	case "read_file":
		var a struct {
			Path      string `json:"path"`
			StartLine *int   `json:"start_line"`
			EndLine   *int   `json:"end_line"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
			return "error parsing arguments: " + err.Error()
		}
		data, err := os.ReadFile(a.Path)
		if err != nil {
			return "error: " + err.Error()
		}
		if a.StartLine == nil && a.EndLine == nil {
			return string(data)
		}
		lines := strings.Split(string(data), "\n")
		start, end := 1, len(lines)
		if a.StartLine != nil {
			start = *a.StartLine
		}
		if a.EndLine != nil {
			end = *a.EndLine
		}
		if start < 1 {
			start = 1
		}
		if end > len(lines) {
			end = len(lines)
		}
		if start > end {
			return "error: start_line is after end_line"
		}
		return strings.Join(lines[start-1:end], "\n")

	case "write_file":
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
			return "error parsing arguments: " + err.Error()
		}
		if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
			return "error creating dirs: " + err.Error()
		}
		if err := os.WriteFile(a.Path, []byte(a.Content), 0o644); err != nil {
			return "error: " + err.Error()
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path)

	case "edit_file":
		var a struct {
			Path      string `json:"path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
			return "error parsing arguments: " + err.Error()
		}
		return editFile(a.Path, a.OldString, a.NewString)

	case "shell":
		var a struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
			return "error parsing arguments: " + err.Error()
		}
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "-c", a.Command)
		out, err := cmd.CombinedOutput()
		s := strings.TrimSpace(string(out))
		if err != nil {
			msg := err.Error()
			if ctx.Err() == context.DeadlineExceeded {
				msg = "command timed out after 60s"
			}
			if s == "" {
				return "error: " + msg
			}
			return s + "\n[error: " + msg + "]"
		}
		if s == "" {
			return "(no output)"
		}
		return truncate(s, 20000)

	default:
		return "unknown tool: " + name
	}
}

// editFile performs an exact find-and-replace in a file. The old_string must
// appear exactly once so the edit is unambiguous. Returns a short summary on
// success or an error message string.
func editFile(path, oldStr, newStr string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "error: " + err.Error()
	}
	content := string(data)

	count := strings.Count(content, oldStr)
	if count == 0 {
		return "error: old_string not found in " + path + ". Make sure it matches the file exactly (whitespace, indentation, etc.)."
	}
	if count > 1 {
		return fmt.Sprintf("error: old_string appears %d times in %s. Provide more context so it matches uniquely.", count, path)
	}

	updated := strings.Replace(content, oldStr, newStr, 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return "error: " + err.Error()
	}
	return fmt.Sprintf("edited %s (replaced %d bytes with %d bytes)", path, len(oldStr), len(newStr))
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "\n...[truncated]"
}
