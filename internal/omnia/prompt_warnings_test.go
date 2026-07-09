package omnia

import (
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
	"github.com/AltairaLabs/PromptKit/runtime/workflow"
)

func TestPromptWarnings_SinglePromptNoWarning(t *testing.T) {
	// The Omnia runtime resolves a sole prompt automatically (Omnia#1595/#1605),
	// so a single-prompt pack never needs a prompt named "default".
	pack := &prompt.Pack{Prompts: map[string]*prompt.PackPrompt{"greeting": {}}}
	if w := promptWarnings(pack); w != nil {
		t.Errorf("single-prompt packs resolve automatically; want no warning, got %v", w)
	}
}

func TestPromptWarnings_MultiPromptNoDefault(t *testing.T) {
	pack := &prompt.Pack{Prompts: map[string]*prompt.PackPrompt{
		"greeting": {}, "farewell": {},
	}}
	w := promptWarnings(pack)
	if len(w) != 1 {
		t.Fatalf("want exactly 1 warning, got %d: %v", len(w), w)
	}
	if !strings.Contains(w[0], "greeting") || !strings.Contains(w[0], "farewell") {
		t.Errorf("warning should name the actual prompts: %q", w[0])
	}
	if !strings.Contains(w[0], `"default"`) {
		t.Errorf("warning should name the %q fallback: %q", "default", w[0])
	}
	if strings.Contains(w[0], "1595") {
		t.Errorf("warning must not reference the closed Omnia#1595: %q", w[0])
	}
}

func TestPromptWarnings_MultiPromptHasDefault(t *testing.T) {
	// A "default" among many prompts gives the runtime a working fallback entry.
	pack := &prompt.Pack{Prompts: map[string]*prompt.PackPrompt{
		"default": {}, "greeting": {},
	}}
	if w := promptWarnings(pack); w != nil {
		t.Errorf("want no warning when a default prompt exists, got %v", w)
	}
}

func TestPromptWarnings_WorkflowPackExempt(t *testing.T) {
	// Workflow packs declare their own entry (workflow.entry), so they're exempt.
	pack := &prompt.Pack{
		Workflow: &workflow.Spec{},
		Prompts:  map[string]*prompt.PackPrompt{"greeting": {}, "farewell": {}},
	}
	if w := promptWarnings(pack); w != nil {
		t.Errorf("workflow packs are exempt; want no warning, got %v", w)
	}
}

func TestPromptWarnings_MultiAgentPackExempt(t *testing.T) {
	// Multi-agent packs declare their entry (agents.entry), so they're exempt.
	pack := &prompt.Pack{
		Agents:  &prompt.AgentsConfig{Members: map[string]*prompt.AgentDef{"a": {}}},
		Prompts: map[string]*prompt.PackPrompt{"greeting": {}, "farewell": {}},
	}
	if w := promptWarnings(pack); w != nil {
		t.Errorf("multi-agent packs are exempt; want no warning, got %v", w)
	}
}

func TestPromptWarnings_NoPromptsNoWarning(t *testing.T) {
	if w := promptWarnings(&prompt.Pack{}); w != nil {
		t.Errorf("want no warning for a pack with no prompts, got %v", w)
	}
}
