package components

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/sleuth-io/sx/v2/internal/ui"
	"github.com/sleuth-io/sx/v2/internal/ui/theme"
)

// statusUpdateMsg updates the status message.
type statusUpdateMsg struct {
	message string
}

// statusDoneMsg signals status is complete.
type statusDoneMsg struct {
	success bool
	message string
}

// statusModel is the bubbletea model for the status line.
type statusModel struct {
	spinner spinner.Model
	message string
	done    bool
	success bool
	final   string
	theme   theme.Theme
}

func newStatusModel(message string) statusModel {
	th := theme.Current()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = th.Styles().Spinner

	return statusModel{
		spinner: s,
		message: message,
		theme:   th,
	}
}

func (m statusModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m statusModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case statusUpdateMsg:
		m.message = msg.message
		return m, nil

	case statusDoneMsg:
		m.done = true
		m.success = msg.success
		m.final = msg.message
		return m, tea.Quit

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.done = true
			return m, tea.Quit
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m statusModel) View() string {
	if m.done {
		if m.final != "" {
			styles := m.theme.Styles()
			sym := m.theme.Symbols()
			if m.success {
				return styles.Success.Render(sym.Success+" "+m.final) + "\n"
			}
			return styles.Error.Render(sym.Error+" "+m.final) + "\n"
		}
		return ""
	}

	styles := m.theme.Styles()
	return m.spinner.View() + " " + styles.Muted.Render(m.message)
}

// Status provides a transient status line that updates in place.
// Use for operations where you want to show progress without cluttering output.
type Status struct {
	program *tea.Program
	out     io.Writer
	noTTY   bool
	mu      sync.Mutex
	message string
	silent  bool
}

// NewStatus creates a new status line.
func NewStatus(out io.Writer) *Status {
	return &Status{
		out:   out,
		noTTY: !ui.IsTTY(out),
	}
}

// SetSilent enables silent mode (no output).
func (s *Status) SetSilent(silent bool) {
	s.silent = silent
}

// Start begins showing a status with spinner.
func (s *Status) Start(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.silent {
		return
	}

	s.message = message

	if s.noTTY {
		fmt.Fprintf(s.out, "%s...", message)
		return
	}

	m := newStatusModel(message)
	s.program = tea.NewProgram(m, tea.WithOutput(s.out))

	go func() {
		_, _ = s.program.Run()
	}()

	time.Sleep(10 * time.Millisecond)
}

// Update changes the status message.
func (s *Status) Update(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.silent {
		return
	}

	s.message = message

	if s.noTTY {
		fmt.Fprintf(s.out, " %s...", message)
		return
	}

	if s.program != nil {
		s.program.Send(statusUpdateMsg{message: message})
	}
}

// Done completes the status with an optional final message.
// If finalMessage is empty, the status line is cleared.
func (s *Status) Done(finalMessage string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.silent {
		return
	}

	if s.noTTY {
		if finalMessage != "" {
			fmt.Fprintf(s.out, " %s\n", finalMessage)
		} else {
			fmt.Fprintln(s.out, " done")
		}
		return
	}

	if s.program != nil {
		s.program.Send(statusDoneMsg{success: true, message: finalMessage})
		time.Sleep(20 * time.Millisecond)
	}
}

// Fail completes the status with an error message.
func (s *Status) Fail(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.silent {
		return
	}

	if s.noTTY {
		fmt.Fprintf(s.out, " %s\n", message)
		return
	}

	if s.program != nil {
		s.program.Send(statusDoneMsg{success: false, message: message})
		time.Sleep(20 * time.Millisecond)
	}
}

// Clear clears the status line without showing a final message.
func (s *Status) Clear() {
	s.Done("")
}

// RunStatus runs a function while showing a status spinner.
// Shows a success/fail message based on the result.
func RunStatus[T any](out io.Writer, message string, fn func() (T, error)) (T, error) {
	var result T

	noTTY := !ui.IsTTY(out)

	if noTTY {
		fmt.Fprintf(out, "%s... ", message)
		result, err := fn()
		if err != nil {
			fmt.Fprintln(out, "failed")
		} else {
			fmt.Fprintln(out, "done")
		}
		return result, err
	}

	m := newStatusModel(message)
	p := tea.NewProgram(m, tea.WithOutput(out))

	var fnErr error

	go func() {
		result, fnErr = fn()
		if fnErr != nil {
			p.Send(statusDoneMsg{success: false, message: ""})
		} else {
			p.Send(statusDoneMsg{success: true, message: ""})
		}
	}()

	if _, err := p.Run(); err != nil {
		return result, fmt.Errorf("status failed: %w", err)
	}

	return result, fnErr
}

// StatusLine is a simpler non-animated status that updates in place.
// Better for rapid updates where animation would be distracting.
type StatusLine struct {
	out     io.Writer
	noTTY   bool
	silent  bool
	lastLen int
	mu      sync.Mutex
}

// NewStatusLine creates a simple status line that updates in place.
func NewStatusLine(out io.Writer) *StatusLine {
	return &StatusLine{
		out:   out,
		noTTY: !ui.IsTTY(out),
	}
}

// SetSilent enables silent mode.
func (s *StatusLine) SetSilent(silent bool) {
	s.silent = silent
}

// Set updates the status line text.
func (s *StatusLine) Set(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.silent {
		return
	}

	if s.noTTY {
		fmt.Fprintln(s.out, message)
		return
	}

	// Clear previous line and write new
	clear := strings.Repeat(" ", s.lastLen)
	fmt.Fprintf(s.out, "\r%s\r%s", clear, message)
	s.lastLen = len(message)
}

// Clear clears the status line.
func (s *StatusLine) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.silent || s.noTTY {
		return
	}

	clear := strings.Repeat(" ", s.lastLen)
	fmt.Fprintf(s.out, "\r%s\r", clear)
	s.lastLen = 0
}

// Done clears the line and optionally prints a final message on a new line.
func (s *StatusLine) Done(message string) {
	s.Clear()
	if message != "" && !s.silent {
		fmt.Fprintln(s.out, message)
	}
}
