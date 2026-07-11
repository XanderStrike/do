package main

// A super-minimal terminal coding agent built with Bubble Tea.
//
// Four tools: read_file, write_file, edit_file, shell. Talks to any OpenAI-compatible
// chat completions endpoint. See README.md for config.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	userStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("63")).Bold(true)
	assistantStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("36")).Bold(true)
	toolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	resultStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
)

type model struct {
	llm       *LLMClient
	cwd       string
	conv      *[]Message // conversation history (shared with agent goroutine via pointer)
	blocks    []string  // rendered conversation lines for the viewport
	viewport  viewport.Model
	ta        textarea.Model
	spinner   spinner.Model
	busy      bool
	width     int
	height    int
	err       string
	cancel    context.CancelFunc // cancel the current agent turn (nil if idle)
}

// Messages exchanged between the agent goroutine and the TUI.
type assistantMsg struct{ text string }
type toolStartMsg struct{ name, args string }
type toolResultMsg struct{ name, args, result string }
type errMsg struct{ err error }
type doneMsg struct{}
type stopMsg struct{} // user pressed Esc to stop generation

func initialModel() model {
	cwd, _ := os.Getwd()

	vp := viewport.New(80, 20)
	vp.SetContent("")

	ta := textarea.New()
	ta.Placeholder = "Ask me to build something... (Enter to send, Esc to stop/quit, Ctrl+C to force quit)"
	ta.Focus()
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.SetHeight(3)

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	msgs := []Message{{Role: "system", Content: systemPrompt(cwd, loadAgentsContext(cwd))}}

	m := model{
		llm:      newLLMClient(),
		cwd:      cwd,
		conv:     &msgs,
		viewport: vp,
		ta:       ta,
		spinner:  sp,
		blocks:   []string{dimStyle.Render("working dir: " + cwd)},
	}

	// Resume session if .do-session exists.
	if prior := loadSession(cwd); len(prior) > 0 {
		msgs = append(msgs, prior...)
		for _, msg := range prior {
			m.blocks = append(m.blocks, renderHistoryBlock(msg))
		}
		m.refreshViewport()
	}

	return m
}

func systemPrompt(cwd, agentsContext string) string {
	prompt := fmt.Sprintf(`You are a minimal terminal coding agent. You operate inside the directory: %s

You have four tools:
- read_file(path): read a file's contents
- write_file(path, content): write a file (creates parents, overwrites)
- edit_file(path, old_string, new_string): surgical find-and-replace in a file (old_string must match uniquely)
- shell(command): run a shell command via bash -c

Use the tools to inspect, edit, and run code to fulfill the user's request. Be concise.
Prefer reading files before editing. Prefer shell commands like ls, rg, git to explore.
When you make changes, summarize what you did briefly.`, cwd)
	if agentsContext != "" {
		prompt += "\n\n" + agentsContext
	}
	return prompt
}

// loadAgentsContext reads AGENTS.md files from cwd up to the filesystem root
// and concatenates them root-first so the nearest (cwd) file is last (most
// specific). Returns an empty string if none are found.
func loadAgentsContext(cwd string) string {
	var paths []string
	dir := cwd
	for {
		paths = append(paths, filepath.Join(dir, "AGENTS.md"))
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	var parts []string
	// Walk root-first so cwd's AGENTS.md appears last (most specific wins).
	for i := len(paths) - 1; i >= 0; i-- {
		data, err := os.ReadFile(paths[i])
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(data))
		if s == "" {
			continue
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return ""
	}
	return "# AGENTS.md context\n\n" + strings.Join(parts, "\n\n---\n\n")
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyEsc:
			if m.busy {
				if m.cancel != nil {
					m.cancel()
				}
				return m, nil // stop generation, don't quit
			}
			return m, tea.Quit
		case tea.KeyEnter:
			if !m.busy && m.ta.Value() != "" {
				input := strings.TrimSpace(m.ta.Value())
				m.ta.Reset()
				m.submit(input)
				return m, nil
			}
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)

	case assistantMsg:
		m.appendBlock(assistantStyle.Render("● assistant") + "\n" + msg.text)
		m.refreshViewport()

	case toolStartMsg:
		m.appendBlock(toolStyle.Render("↳ " + msg.name + " ") + dimStyle.Render(truncateOneLine(msg.args)))

	case toolResultMsg:
		m.appendBlock(resultStyle.Render(indent(truncateLines(msg.result, maxResultLines))))
		m.refreshViewport()

	case errMsg:
		m.err = msg.err.Error()
		m.appendBlock(errStyle.Render("error: " + msg.err.Error()))
		m.refreshViewport()
		m.busy = false

	case doneMsg:
		m.busy = false
		m.err = ""
		m.cancel = nil

	case stopMsg:
		m.busy = false
		m.err = ""
		m.cancel = nil
	}

	// Forward to textarea. Only forward non-key messages to the viewport —
	// otherwise arrow keys (Up/Down) used for cursor movement in the textarea
	// also scroll the viewport, causing it to jump around while typing. Mouse
	// events (scroll) go to the viewport so the user can scroll history.
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	cmds = append(cmds, cmd)
	if _, ok := msg.(tea.KeyMsg); !ok {
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

func (m *model) resize() {
	m.viewport.Width = m.width
	m.viewport.Height = m.height - inputHeight
	m.ta.SetWidth(m.width)
	m.refreshViewport()
}

func (m *model) appendBlock(s string) {
	m.blocks = append(m.blocks, s)
}

func (m *model) refreshViewport() {
	// Pad with blank lines at the bottom so the last output isn't covered
	// by the status bar and textarea below the viewport.
	padding := strings.Repeat("\n", inputHeight)
	m.viewport.SetContent(strings.Join(m.blocks, "\n\n") + padding)
	m.viewport.GotoBottom()
}

const inputHeight = 5 // status line + textarea

func (m *model) submit(input string) {
	m.busy = true
	m.err = ""
	*m.conv = append(*m.conv, Message{Role: "user", Content: input})
	saveSession(m.cwd, m.conv)
	m.appendBlock(userStyle.Render("● you") + "\n" + input)
	m.refreshViewport()

	// Launch the agent turn in a goroutine, streaming progress back via msgs.
	p := m.program()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	go runAgent(p, m.llm, m.conv, m.cwd, ctx)
}

// program returns the current tea.Program. We grab it via a package-level
// pointer set in main so the goroutine can send messages.
func (m *model) program() *tea.Program { return prog }

var prog *tea.Program

const maxTurns = 50
const maxResultLines = 10 // cap tool result display in the viewport

// runAgent loops: ask the LLM, execute any tool calls, repeat until the LLM
// replies with plain text and no tool calls. Caps at maxTurns iterations.
func runAgent(p *tea.Program, llm *LLMClient, conv *[]Message, cwd string, ctx context.Context) {
	saveSession(cwd, conv)
	for i := 0; i < maxTurns; i++ {
		resp, err := llm.Complete(ctx, *conv)
		if err != nil {
			if ctx.Err() != nil {
				p.Send(stopMsg{})
				return
			}
			p.Send(errMsg{err})
			return
		}
		*conv = append(*conv, resp)
		saveSession(cwd, conv)

		if resp.Content != "" {
			p.Send(assistantMsg{resp.Content})
		}
		if len(resp.ToolCalls) == 0 {
			p.Send(doneMsg{})
			return
		}

		for _, tc := range resp.ToolCalls {
			// Stop before running more tools if the user cancelled.
			if ctx.Err() != nil {
				p.Send(stopMsg{})
				return
			}
			p.Send(toolStartMsg{name: tc.Function.Name, args: tc.Function.Arguments})
			result := runTool(ctx, tc.Function.Name, tc.Function.Arguments)
			p.Send(toolResultMsg{name: tc.Function.Name, args: tc.Function.Arguments, result: result})
			*conv = append(*conv, Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    result,
			})
			saveSession(cwd, conv)
		}
	}
	p.Send(errMsg{fmt.Errorf("turn limit (%d) reached", maxTurns)})
}

func (m model) View() string {
	status := ""
	if m.busy {
		status = m.spinner.View() + dimStyle.Render(" working...")
	} else if m.err != "" {
		status = errStyle.Render("ready (last turn errored)")
	} else {
		status = dimStyle.Render("ready")
	}

	var b strings.Builder
	b.WriteString(m.viewport.View())
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Width(m.width).Render(status))
	b.WriteString("\n")
	b.WriteString(m.ta.View())
	return b.String()
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

func truncateOneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if utf8.RuneCountInString(s) > 200 {
		return string([]rune(s)[:200]) + "..."
	}
	return s
}

// truncateLines truncates s to at most n lines, appending a truncation
// indicator if lines were cut. Used for display only (e.g. read_file output).
func truncateLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + "\n...[truncated]"
}

// renderHistoryBlock renders a stored message for display when resuming a
// session. Tool messages are shown as results; assistant messages with tool
// calls show the call and its result (if any follows).
func renderHistoryBlock(msg Message) string {
	switch msg.Role {
	case "user":
		return userStyle.Render("● you") + "\n" + msg.Content
	case "assistant":
		s := assistantStyle.Render("● assistant")
		if msg.Content != "" {
			s += "\n" + msg.Content
		}
		for _, tc := range msg.ToolCalls {
			s += "\n" + toolStyle.Render("↳ "+tc.Function.Name+" ") + dimStyle.Render(truncateOneLine(tc.Function.Arguments))
		}
		return s
	case "tool":
		return resultStyle.Render(indent(truncateLines(msg.Content, maxResultLines)))
	default:
		return ""
	}
}

func main() {
	prog = tea.NewProgram(initialModel(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
