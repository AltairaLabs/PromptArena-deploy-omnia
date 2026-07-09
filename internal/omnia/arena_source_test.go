package omnia

import (
	"encoding/json"
	"testing"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func TestParseArenaToolSources_LoadedToolsYAML(t *testing.T) {
	// A tool file as embedded in ArenaConfig.loaded_tools[].data (base64 of this YAML).
	toolYAML := `apiVersion: promptkit.altairalabs.ai/v1alpha1
kind: Tool
metadata:
  name: create-workout
spec:
  name: create_workout
  description: Create a workout
  input_schema:
    type: object
    properties:
      name: { type: string }
  mode: live
  http:
    url: https://api.splitpantz.com/api/v1/workouts
    method: POST
    timeout_ms: 15000
    headers_from_env:
      - "Authorization=SPLITZ_AUTH"
      - "X-Act-As-User=WORKOUT_ACT_AS_USER"
    redact:
      - user_id
    response:
      body_mapping: "{id: id, name: name}"
`
	// encoding/json marshals []byte as base64, which is exactly how the harness
	// serializes loaded_tools[].data; json.Marshal of the wrapper reproduces it.
	arenaJSON := mustMarshalArena(t, map[string]any{
		"loaded_tools": []map[string]any{{"file_path": "tools/create-workout.tool.yaml", "data": []byte(toolYAML)}},
	})

	got := parseArenaToolSources(arenaJSON)
	src, ok := got["create_workout"]
	if !ok {
		t.Fatalf("expected tool create_workout, got keys %v", keysOf(got))
	}
	if src.Method != "POST" || src.URL != "https://api.splitpantz.com/api/v1/workouts" {
		t.Errorf("method/url wrong: %+v", src)
	}
	if src.Mode != "live" {
		t.Errorf("mode = %q, want live", src.Mode)
	}
	if src.TimeoutMs != 15000 {
		t.Errorf("timeout_ms = %d, want 15000", src.TimeoutMs)
	}
	if src.ResponseBodyMapping != "{id: id, name: name}" {
		t.Errorf("responseBodyMapping = %q", src.ResponseBodyMapping)
	}
	if len(src.Redact) != 1 || src.Redact[0] != "user_id" {
		t.Errorf("redact = %v, want [user_id]", src.Redact)
	}
	if len(src.HeadersFromEnv) != 2 {
		t.Errorf("headersFromEnv = %v, want 2 entries", src.HeadersFromEnv)
	}
}

func TestParseArenaToolSources_Graceful(t *testing.T) {
	if got := parseArenaToolSources(""); got == nil || len(got) != 0 {
		t.Errorf("empty input: want non-nil empty map, got %v", got)
	}
	if got := parseArenaToolSources("{not json"); got == nil || len(got) != 0 {
		t.Errorf("bad json: want non-nil empty map, got %v", got)
	}
}

func TestParseArenaToolSources_ToolSpecsJSON(t *testing.T) {
	arenaJSON := mustMarshalArena(t, map[string]any{
		"tool_specs": map[string]any{
			"list_user_exercises": map[string]any{
				"name": "list_user_exercises", "mode": "live",
				"http": map[string]any{
					"url": "https://api.splitpantz.com/api/v1/exercises", "method": "GET",
					"response": map[string]any{"body_mapping": "[].{id: id}"},
				},
			},
		},
	})
	got := parseArenaToolSources(arenaJSON)
	src := got["list_user_exercises"]
	if src == nil || src.Method != "GET" || src.ResponseBodyMapping != "[].{id: id}" {
		t.Fatalf("tool_specs parse wrong: %+v", src)
	}
}

// test helpers (keep in this file):
func mustMarshalArena(t *testing.T, v any) string {
	t.Helper()
	b, err := jsonMarshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func keysOf(m map[string]*httpToolSource) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
