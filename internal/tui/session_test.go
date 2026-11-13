package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPromptSelectionAndSecretNeverRendered(t *testing.T) {
	m := newModel(context.Background(), func() {}, "Welcome", nil)
	reply := make(chan string, 1)
	m.Update(promptMsg{Prompt: Prompt{Title: "Provider", Options: []Option{{Label: "OpenAI API", Value: "openai"}, {Label: "ChatGPT account", Value: "chatgpt"}}}, reply: reply})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := <-reply; got != "chatgpt" {
		t.Fatalf("selected %q", got)
	}
	m.Update(promptMsg{Prompt: Prompt{Title: "API key", Secret: true}, reply: reply})
	m.Update(tea.KeyPressMsg{Text: "fixture-secret", Code: 'f'})
	if strings.Contains(m.View().Content, "fixture-secret") {
		t.Fatal("secret exposed in view")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := <-reply; got != "fixture-secret" {
		t.Fatalf("secret input %q", got)
	}
}

func TestSessionDetailsResizeAndUnknownBudget(t *testing.T) {
	m := newModel(context.Background(), func() {}, "Review", nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(activityMsg{Activity{Summary: "reviewer · read_file completed", Detail: "model: fixture · result: 120 bytes", Budget: "Cost unknown · reserved $0.01"}})
	view := m.View().Content
	if !strings.Contains(view, "Cost unknown") || strings.Contains(view, "120 bytes") {
		t.Fatalf("compact view: %s", view)
	}
	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if !strings.Contains(m.View().Content, "120 bytes") {
		t.Fatal("details missing")
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if m.width != 120 {
		t.Fatal("resize ignored")
	}
}

func TestCancellationWaitsForWorkerAndUnblocksQuestion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newModel(ctx, cancel, "Review", nil)
	m.Update(promptMsg{Prompt: Prompt{Title: "Continue?"}, reply: make(chan string, 1)})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if ctx.Err() == nil {
		t.Fatal("worker not cancelled")
	}
	if cmd != nil {
		t.Fatal("UI quit before worker persisted cancellation")
	}
	if !strings.Contains(m.View().Content, "Stopping") {
		t.Fatal("missing cancellation status")
	}
}

func TestTransientCheckpointsDoNotFloodToolTimeline(t *testing.T) {
	m := newModel(context.Background(), func() {}, "Review", nil)
	m.addActivity(Activity{Summary: "reviewer investigating", Transient: true, Budget: "Cost unknown"})
	m.addActivity(Activity{Summary: "reviewer · read_file running", Key: "reviewer/read"})
	m.addActivity(Activity{Summary: "reviewer investigating", Transient: true})
	m.addActivity(Activity{Summary: "reviewer · read_file completed", Key: "reviewer/read"})
	if len(m.activities) != 1 || strings.Contains(m.View().Content, "running") {
		t.Fatalf("timeline flooded: %+v", m.activities)
	}
}
