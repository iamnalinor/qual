package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func accent(text string) string {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return text
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#A7C9A5")).Bold(true).Render(text)
}

func (m *model) View() tea.View {
	width := max(12, m.width-4)
	header := m.headerView(width)
	prompt := m.promptView(width, max(5, m.height-strings.Count(header, "\n")-10))
	var b strings.Builder
	b.WriteString("\n  " + accent("QUAL") + "  " + ansi.Truncate(clean(m.title), max(1, width-6), "…") + "\n")
	b.WriteString("  " + strings.Repeat("─", width) + "\n")
	b.WriteString(header)
	available := max(0, m.height-strings.Count(header, "\n")-strings.Count(prompt, "\n")-8)
	if m.active != "" {
		b.WriteString("  " + ansi.Truncate(clean(m.active), width, "…") + "\n")
		available = max(0, available-1)
	}
	b.WriteString(m.timeline(width, available))
	b.WriteString(prompt)
	if m.stopping {
		b.WriteString("\n  " + m.text("Stopping · saving session…") + "\n")
	}
	elapsed := time.Since(m.started).Truncate(time.Second)
	b.WriteString("\n  " + strings.ReplaceAll(ansi.Wrap(m.status(elapsed), width, ""), "\n", "\n  ") + "\n")
	b.WriteString("  " + m.footer() + "\n")
	return tea.NewView(b.String())
}

func (m *model) headerView(width int) string {
	if m.header == "" {
		return ""
	}
	lines := strings.Split(m.header, "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(clean(line), width, "…")
	}
	return "  " + strings.Join(lines, "\n  ") + "\n\n"
}

func (m *model) footer() string {
	if m.stopping {
		return m.text("Waiting for work to stop safely")
	}
	if m.prompt == nil {
		return m.text("ctrl+o details · ↑/↓ scroll · esc stop")
	}
	footer := m.text("enter confirm · esc cancel")
	if len(m.prompt.Options) > 1 {
		footer = m.text("↑/↓ choose · ") + footer
	}
	if strings.Count(m.prompt.Description, "\n") > 5 {
		footer = m.text("pgup/pgdn preview · ") + footer
	}
	return footer
}

func (m *model) timeline(width, available int) string {
	if available == 0 {
		return ""
	}
	var lines []string
	end := max(0, len(m.activities)-m.scroll)
	for _, event := range m.activities[:end] {
		lines = append(lines, strings.Split(ansi.Wrap(clean(event.Summary), width, ""), "\n")...)
		if m.expanded && event.Detail != "" {
			lines = append(lines, strings.Split(ansi.Wrap("  "+clean(event.Detail), width, ""), "\n")...)
		}
	}
	if len(lines) > available {
		lines = lines[len(lines)-available:]
	}
	if len(lines) == 0 {
		lines = []string{m.text("Ready when you are.")}
	}
	return "  " + strings.Join(lines, "\n  ") + "\n"
}

func (m *model) promptView(width, available int) string {
	if m.prompt == nil {
		return ""
	}
	p := m.prompt
	var b strings.Builder
	b.WriteString("\n  " + accent(clean(p.Title)) + "\n")
	descMax := max(1, available-4-min(5, len(p.Options)))
	b.WriteString(m.description(width, descMax))
	if len(p.Options) == 0 {
		value := m.input
		if p.Secret {
			value = strings.Repeat("•", len([]rune(value)))
		}
		b.WriteString("  › " + ansi.Truncate(value, max(1, width-2), "…") + "█\n")
		return b.String()
	}
	slots := min(5, max(2, available-strings.Count(b.String(), "\n")-2))
	start := max(0, m.selected-slots+1)
	end := min(len(p.Options), start+slots)
	for i := start; i < end; i++ {
		b.WriteString(m.optionView(p.Options[i], i, width))
	}
	if start > 0 || end < len(p.Options) {
		fmt.Fprintf(&b, "    %d / %d\n", m.selected+1, len(p.Options))
	}
	return b.String()
}

func (m *model) description(width, available int) string {
	if m.prompt.Description == "" {
		return ""
	}
	text := strings.Join(strings.Split(m.prompt.Description, "\n"), "\n")
	text = strings.Map(func(r rune) rune {
		if r != '\n' && (r < 32 || r == 127) {
			return -1
		}
		return r
	}, text)
	lines := strings.Split(ansi.Wrap(text, width, ""), "\n")
	start := min(m.promptScroll, max(0, len(lines)-available))
	end := min(len(lines), start+available)
	return "  " + strings.Join(lines[start:end], "\n  ") + "\n"
}

func (m *model) optionView(option Option, index, width int) string {
	marker := "  "
	if index == m.selected {
		marker = "› "
	}
	label := marker + clean(option.Label)
	if index == m.selected {
		label = accent(label)
	}
	rendered := "  " + ansi.Truncate(label, width, "…") + "\n"
	if index == m.selected && option.Description != "" {
		lines := strings.Split(ansi.Wrap(clean(option.Description), max(1, width-2), ""), "\n")
		if len(lines) > 2 {
			lines = append(lines[:1], ansi.Truncate(strings.Join(lines[1:], " "), max(1, width-2), "…"))
		}
		rendered += "    " + strings.Join(lines, "\n    ") + "\n"
	}
	return rendered
}

func (m *model) text(english string) string {
	if m.language == "ru" {
		if translated, ok := russian[english]; ok {
			return translated
		}
	}
	return english
}

var russian = map[string]string{
	"Ready when you are.":                    "Готов к работе.",
	"Stopping · saving session…":             "Останавливаем работу · сохраняем сессию…",
	"Waiting for work to stop safely":        "Ожидаем безопасной остановки",
	"ctrl+o details · ↑/↓ scroll · esc stop": "ctrl+o подробнее · ↑/↓ прокрутка · esc остановить",
	"enter confirm · esc cancel":             "enter подтвердить · esc отменить",
	"pgup/pgdn preview · ":                   "pgup/pgdn просмотр · ",
	"↑/↓ choose · ":                          "↑/↓ выбрать · ",
}

func (m *model) status(elapsed time.Duration) string {
	if m.budget == "" {
		return elapsed.String()
	}
	return fmt.Sprintf("%s · %s", elapsed, m.budget)
}
