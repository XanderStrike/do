package main

// A super-minimal terminal coding agent built with Bubble Tea.
//
// Four tools: read_file, write_file, edit_file, shell. Talks to any OpenAI-compatible
// chat completions endpoint. See README.md for config.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
	usage     *Usage     // last-known token usage from the API
	blocks    []string   // rendered conversation lines for the viewport
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
type usageMsg struct{ usage *Usage }
type errMsg struct{ err error }
type doneMsg struct{}
type stopMsg struct{ note string } // user pressed Esc to stop generation

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
	if prior, usage := loadSession(cwd); len(prior) > 0 {
		msgs = append(msgs, prior...)
		m.usage = usage
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
- read_file(path, start_line?, end_line?): read a file's contents; start_line/end_line (1-based, inclusive) are optional
- write_file(path, content): write a file (creates parents, overwrites)
- edit_file(path, old_string, new_string): surgical find-and-replace in a file (old_string must match uniquely)
- shell(command): run a shell command via bash -c

Use the tools to inspect, edit, and run code to fulfill the user's request. Be concise.
Prefer reading files before editing. Prefer shell commands like ls, rg, git, jq to explore.
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

	case usageMsg:
		m.usage = msg.usage

	case toolStartMsg:
		m.appendBlock(toolStyle.Render("↳ " + msg.name + " ") + dimStyle.Render(truncateOneLine(msg.args)))
		m.refreshViewport()

	case toolResultMsg:
		m.appendBlock(resultStyle.Render(indent(truncateLines(msg.result, maxResultLines))))
		m.refreshViewport()

	case errMsg:
		m.err = msg.err.Error()
		m.appendBlock(errStyle.Render("error: " + msg.err.Error()))
		m.refreshViewport()
		m.idle()

	case doneMsg:
		m.idle()
		m.err = ""
	case stopMsg:
		if msg.note != "" {
			m.appendBlock(dimStyle.Render(msg.note))
			m.refreshViewport()
		}
		m.idle()
		m.err = ""
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

// idle returns the model to a non-busy state, clearing the cancel func for
// the finished turn. m.err is left untouched so View can show "last turn
// errored" until the next turn begins.
func (m *model) idle() {
	m.busy = false
	m.cancel = nil
}

func (m *model) refreshViewport() {
	// Pre-wrap content to the viewport width. The viewport's own rendering
	// applies lipgloss Width+MaxWidth, which wraps long lines for *height
	// counting* while truncating them for display — so its scroll math (based
	// on unwrapped logical lines) underestimates visible height and GotoBottom
	// leaves the last few lines clipped behind the status bar/textarea. Wrapping
	// first makes logical lines == visual lines, keeping the math honest.
	content := ansi.Wrap(strings.Join(m.blocks, "\n\n"), m.viewport.Width, "")
	m.viewport.SetContent(content)
	m.viewport.GotoBottom()
}

const inputHeight = 5 // status bar (1) + divider (1) + textarea (3)

// submit starts an agent turn. It's a pointer receiver so it can mutate m
// in place; this is safe because Update holds an addressable local m and
// returns it immediately after, so the mutations propagate back to the
// framework. The same goes for the other pointer-receiver helpers below.
func (m *model) submit(input string) {
	m.busy = true
	m.err = ""
	*m.conv = append(*m.conv, Message{Role: "user", Content: input})
	saveSession(m.cwd, m.conv, m.usage)
	m.appendBlock(userStyle.Render("● you") + "\n" + input)
	m.refreshViewport()

	// Launch the agent turn in a goroutine, streaming progress back via msgs.
	p := prog
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	go runAgent(p, m.llm, m.conv, m.cwd, ctx)
}

var prog *tea.Program

const maxTurns = 50
const maxResultLines = 10 // cap tool result display in the viewport

// runAgent loops: ask the LLM, execute any tool calls, repeat until the LLM
// replies with plain text and no tool calls. Caps at maxTurns iterations.
// Runs on a goroutine; safe because the `busy` flag serializes turns (the
// TUI won't submit another while one is in flight). The session is saved
// once on exit via defer — mid-turn state isn't worth persisting since a
// half-executed tool loop can't be resumed anyway (trimForResume drops it).
func runAgent(p *tea.Program, llm *LLMClient, conv *[]Message, cwd string, ctx context.Context) {
	var usage *Usage
	defer func() { saveSession(cwd, conv, usage) }()
	for i := 0; i < maxTurns; i++ {
		resp, u, err := llm.Complete(ctx, *conv)
		if err != nil {
			if ctx.Err() != nil {
				noteInterruption(conv)
				p.Send(stopMsg{note: "generation interrupted by user"})
				return
			}
			p.Send(errMsg{err})
			return
		}
		usage = u
		if usage != nil {
			p.Send(usageMsg{usage: usage})
		}
		*conv = append(*conv, resp)

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
				noteInterruption(conv)
				p.Send(stopMsg{note: "generation interrupted by user"})
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
		}
	}
	p.Send(errMsg{fmt.Errorf("turn limit (%d) reached", maxTurns)})
}

// noteInterruption trims any incomplete trailing tool-call sequence from the
// conversation and appends a note so the model understands why the turn was
// cut off when the session resumes.
func noteInterruption(conv *[]Message) {
	if len(*conv) > 1 {
		*conv = append((*conv)[:1:1], trimForResume((*conv)[1:])...)
	}
	*conv = append(*conv, Message{
		Role:    "user",
		Content: "[Generation was interrupted by the user. The previous turn was cut off before completion.]",
	})
}

func (m model) View() string {
	// "do" prefix is animated (spinner) and colored based on state.
	var prefix string
	switch {
	case m.busy:
		prefix = lipgloss.NewStyle().Foreground(lipgloss.Color("36")).Render(m.spinner.View() + "doing")
	case m.err != "":
		prefix = errStyle.Render("do")
	default:
		prefix = dimStyle.Render("do")
	}

	// Status bar across the top: [do] - path (git-branch) - model
	rest := " - " + m.cwd
	if br := gitBranch(m.cwd); br != "" {
		rest += " (" + br + ")"
	}
	rest += " - " + m.llm.Model
	if m.usage != nil {
		rest += fmt.Sprintf(" - %s↑ %s↓ tok",
			comma(m.usage.PromptTokens), comma(m.usage.CompletionTokens))
	}

	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Width(m.width).Render(prefix + dimStyle.Render(rest)))
	b.WriteString("\n")
	b.WriteString(m.viewport.View())
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", m.width)))
	b.WriteString("\n")
	b.WriteString(m.ta.View())
	return b.String()
}

// gitBranch returns the current git branch name, or "" if not in a repo.
func gitBranch(cwd string) string {
	cmd := exec.Command("git", "-C", cwd, "symbolic-ref", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

// comma formats an integer with comma separators (e.g. 21970 → "21,970").
func comma(n int) string {
	s := strconv.Itoa(n)
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(byte(c))
	}
	if neg {
		b.WriteByte('-')
	}
	return b.String()
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
