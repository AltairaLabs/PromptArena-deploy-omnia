package omnia

import (
	"encoding/json"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/packspec"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// TestNormalizeSchema_DropsEmptiesThroughArrays states the canonical form the
// drift comparison works in: keys carrying no information disappear at every
// depth, including inside arrays, so two spellings of the same schema compare
// equal.
func TestNormalizeSchema_DropsEmptiesThroughArrays(t *testing.T) {
	verbose := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
		"anyOf": []any{
			map[string]any{"type": "string", "enum": []any{}},
			map[string]any{"type": "number", "examples": nil},
		},
	}
	terse := map[string]any{
		"type": "object",
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "number"},
		},
	}

	got, err := normalizeSchema(verbose)
	if err != nil {
		t.Fatalf("normalizeSchema: %v", err)
	}
	want, err := normalizeSchema(terse)
	if err != nil {
		t.Fatalf("normalizeSchema: %v", err)
	}
	if got != want {
		t.Errorf("normalized forms differ:\n got %s\nwant %s", got, want)
	}
}

// A schema that cannot be marshalled or parsed is reported, not silently
// treated as equal — schemaDrifts turns an error into "no drift", so the error
// has to actually arrive.
func TestNormalizeSchema_ErrorPaths(t *testing.T) {
	if _, err := normalizeSchema(make(chan int)); err == nil {
		t.Error("a value JSON cannot marshal must report an error")
	}
	if _, err := normalizeRawSchema(json.RawMessage(`{"type":`)); err == nil {
		t.Error("malformed registry JSON must report an error")
	}
}

// Absence is not a mismatch: with nothing to compare, there is no drift to act
// on.
func TestSchemaDrifts_MissingInputsAreNotDrift(t *testing.T) {
	cases := map[string]struct {
		tool     *prompt.PackTool
		registry json.RawMessage
	}{
		"no tool":            {nil, json.RawMessage(`{"type":"object"}`)},
		"no pack schema":     {&prompt.PackTool{}, json.RawMessage(`{"type":"object"}`)},
		"no registry schema": {&prompt.PackTool{Parameters: &packspec.ToolParameters{Type: "object"}}, nil},
		"unparseable pack":   {&prompt.PackTool{Parameters: &packspec.ToolParameters{Type: "object"}}, json.RawMessage(`{`)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if schemaDrifts(tc.tool, tc.registry) {
				t.Error("want no drift")
			}
		})
	}
}
