// Package tui owns interactive terminal input and renders safe application activity.
package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"
)

// Option is a human-readable choice with an application-owned value.
type Option struct{ Label, Description, Value string }

// Prompt describes a menu or a single-line input. Secret values never enter history.
type Prompt struct {
	Title, Description, Default string
	Options                     []Option
	Secret                      bool
}

// Activity contains only public metadata, never provider payloads or tool arguments.
type Activity struct {
	Summary, Detail, Budget, Key string
	Transient                    bool
}

type (
	activityMsg struct{ Activity }
	promptMsg   struct {
		Prompt
		reply chan string
	}
	doneMsg   struct{ err error }
	tickMsg   time.Time
	headerMsg string
)

// UI sends immutable messages to the sole terminal owner.
type UI struct {
	program *tea.Program
	ctx     context.Context
	prompts chan struct{}
}

// Enabled requires both input and diagnostic output to be terminals.
func Enabled(in io.Reader, out io.Writer) bool {
	input, ok := in.(*os.File)
	if !ok {
		return false
	}
	output, ok := out.(*os.File)
	return ok && os.Getenv("TERM") != "dumb" && term.IsTerminal(int(input.Fd())) && term.IsTerminal(int(output.Fd()))
}

// Run keeps the terminal alive until work has finished, including durable cancellation.
func Run(ctx context.Context, in io.Reader, out io.Writer, title, language string, work func(context.Context, *UI) error) error {
	worker, cancel := context.WithCancel(ctx)
	defer cancel()
	ui := &UI{ctx: worker, prompts: make(chan struct{}, 1)}
	ui.prompts <- struct{}{}
	m := newModel(worker, cancel, title, func() error { return work(worker, ui) })
	m.language = language
	ui.program = tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithoutSignalHandler())
	result, err := ui.program.Run()
	if err != nil {
		return err
	}
	finished, ok := result.(*model)
	if !ok {
		return errors.New("terminal returned an invalid state")
	}
	return finished.err
}

// Activity appends public progress to the timeline.
func (u *UI) Activity(event Activity) { u.program.Send(activityMsg{event}) }

// Header sets persistent session metadata.
func (u *UI) Header(text string) { u.program.Send(headerMsg(text)) }

// Ask serializes concurrent questions and unblocks every waiter on cancellation.
func (u *UI) Ask(ctx context.Context, prompt Prompt) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-u.ctx.Done():
		return "", u.ctx.Err()
	case <-u.prompts:
	}
	defer func() { u.prompts <- struct{}{} }()
	if err := u.ctx.Err(); err != nil {
		return "", err
	}
	reply := make(chan string, 1)
	u.program.Send(promptMsg{prompt, reply})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-u.ctx.Done():
		return "", u.ctx.Err()
	case value := <-reply:
		return value, nil
	}
}

type model struct {
	active                   string
	header, language         string
	promptScroll             int
	ctx                      context.Context
	cancel                   context.CancelFunc
	title                    string
	work                     func() error
	width, height            int
	started                  time.Time
	activities               []Activity
	budget                   string
	prompt                   *promptMsg
	input                    string
	selected, scroll         int
	expanded, stopping, done bool
	err                      error
}

func newModel(ctx context.Context, cancel context.CancelFunc, title string, work func() error) *model {
	return &model{ctx: ctx, cancel: cancel, title: title, work: work, width: 80, height: 24, started: time.Now()}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(now time.Time) tea.Msg { return tickMsg(now) })
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(tick(), func() tea.Msg { return doneMsg{m.work()} })
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch value := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = value.Width, value.Height
	case activityMsg:
		m.addActivity(value.Activity)
	case headerMsg:
		m.header = string(value)
	case promptMsg:
		m.prompt = &value
		m.input = value.Default
		m.selected = 0
		m.scroll = 0
		m.promptScroll = 0
	case doneMsg:
		m.done = true
		m.err = value.err
		m.prompt = nil
		return m, tea.Quit
	case tickMsg:
		if m.ctx.Err() != nil {
			m.stop()
		}
		return m, tick()
	case tea.KeyPressMsg:
		m.key(value)
	case tea.PasteMsg:
		m.paste(value.Content)
	}
	return m, nil
}

func (m *model) addActivity(event Activity) {
	if event.Budget != "" {
		m.budget = event.Budget
	}
	if event.Transient {
		m.active = event.Summary
		return
	}
	if event.Key != "" {
		for i := len(m.activities) - 1; i >= 0; i-- {
			if m.activities[i].Key == event.Key {
				m.activities[i] = event
				return
			}
		}
	}
	// Consecutive checkpoint updates refresh the current state instead of flooding history.
	if len(m.activities) > 0 && m.activities[len(m.activities)-1].Summary == event.Summary {
		m.activities[len(m.activities)-1] = event
		return
	}
	m.activities = append(m.activities, event)
}

func (m *model) stop() { m.stopping = true; m.prompt = nil; m.cancel() }

func (m *model) key(key tea.KeyPressMsg) {
	switch key.String() {
	case "ctrl+c", "esc":
		m.stop()
	case "ctrl+o":
		m.expanded = !m.expanded
		m.scroll = 0
	default:
		if m.prompt != nil {
			m.promptKey(key)
		} else {
			m.scrollKey(key.String())
		}
	}
}

func (m *model) scrollKey(key string) {
	switch key {
	case "up", "pgup":
		m.scroll = min(m.scroll+1, max(0, len(m.activities)-1))
	case "down", "pgdown":
		m.scroll = max(0, m.scroll-1)
	case "end":
		m.scroll = 0
	}
}

func (m *model) promptKey(key tea.KeyPressMsg) {
	p := m.prompt
	switch key.String() {
	case "pgup":
		m.promptScroll = max(0, m.promptScroll-4)
	case "pgdown":
		m.promptScroll += 4
	case "up":
		m.selected = max(0, m.selected-1)
	case "down":
		m.selected = min(len(p.Options)-1, m.selected+1)
	case "enter":
		value := strings.TrimSpace(m.input)
		if len(p.Options) > 0 {
			value = p.Options[m.selected].Value
		}
		p.reply <- value
		m.prompt = nil
		m.input = ""
	case "backspace":
		text := []rune(m.input)
		if len(text) > 0 {
			m.input = string(text[:len(text)-1])
		}
	default:
		if len(p.Options) == 0 {
			m.input += clean(key.Text)
		}
	}
}

func clean(text string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, text)
}

func (m *model) paste(value string) {
	if m.prompt != nil && len(m.prompt.Options) == 0 {
		m.input += clean(value)
	}
}
