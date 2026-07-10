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

func TestParseArenaToolSources_RequestBlockLowercaseMethod(t *testing.T) {
	arenaJSON := mustMarshalArena(t, map[string]any{
		"tool_specs": map[string]any{
			"q": map[string]any{
				"name": "q", "mode": "live",
				"http": map[string]any{
					"url": "https://x/y", "method": "get", // lowercase -> must upper-case
					"headers": map[string]any{"X-Static": "hdr"},
					"request": map[string]any{
						"query_params":   []string{"search"},
						"body_mapping":   "{a: a}",
						"header_params":  map[string]any{"X-H": "{{.a}}"},
						"static_query":   map[string]any{"k": "v"},
						"static_headers": map[string]any{"X-S": "s"},
					},
				},
			},
		},
	})
	src := parseArenaToolSources(arenaJSON)["q"]
	if src == nil {
		t.Fatal("nil src")
	}
	if src.Method != "GET" {
		t.Errorf("method = %q, want GET (upper-cased)", src.Method)
	}
	if len(src.QueryParams) != 1 || src.QueryParams[0] != "search" {
		t.Errorf("queryParams = %v", src.QueryParams)
	}
	if src.RequestBodyMapping != "{a: a}" {
		t.Errorf("requestBodyMapping = %q", src.RequestBodyMapping)
	}
	if src.HeaderParams["X-H"] != "{{.a}}" {
		t.Errorf("headerParams = %v", src.HeaderParams)
	}
	if src.StaticQuery["k"] != "v" {
		t.Errorf("staticQuery = %v", src.StaticQuery)
	}
	if src.StaticHeaders["X-S"] != "s" { // request.static_headers overrides http.headers
		t.Errorf("staticHeaders = %v", src.StaticHeaders)
	}
}

func TestParseArenaToolSources_MockNoHTTPBlock(t *testing.T) {
	toolYAML := "metadata:\n  name: search-shared\nspec:\n  name: search_shared_workouts\n  description: d\n  mode: mock\n"
	arenaJSON := mustMarshalArena(t, map[string]any{
		"loaded_tools": []map[string]any{{"data": []byte(toolYAML)}},
	})
	src := parseArenaToolSources(arenaJSON)["search_shared_workouts"]
	if src == nil || src.Mode != "mock" || src.URL != "" || src.Method != "" {
		t.Fatalf("mock/no-http src wrong: %+v", src)
	}
}

func TestParseArenaToolSources_SkipsMalformedYAML(t *testing.T) {
	good := "metadata:\n  name: g\nspec:\n  name: good_tool\n  mode: live\n  http:\n    url: https://x\n    method: POST\n"
	arenaJSON := mustMarshalArena(t, map[string]any{
		"loaded_tools": []map[string]any{
			{"data": []byte("a:\n\tb: c")}, // tab indentation is invalid YAML
			{"data": []byte(good)},
		},
	})
	got := parseArenaToolSources(arenaJSON)
	if _, ok := got["good_tool"]; !ok {
		t.Errorf("good tool must survive a malformed sibling, got %v", keysOf(got))
	}
}

func TestParseArenaToolSources_ToolSpecsWinsOverLoadedTools(t *testing.T) {
	loaded := "metadata:\n  name: dup\nspec:\n  name: dup\n  mode: live\n  http:\n    url: https://from-loaded\n    method: POST\n"
	arenaJSON := mustMarshalArena(t, map[string]any{
		"tool_specs": map[string]any{
			"dup": map[string]any{"name": "dup", "mode": "live",
				"http": map[string]any{"url": "https://from-spec", "method": "POST"}},
		},
		"loaded_tools": []map[string]any{{"data": []byte(loaded)}},
	})
	src := parseArenaToolSources(arenaJSON)["dup"]
	if src == nil || src.URL != "https://from-spec" {
		t.Errorf("tool_specs must win over loaded_tools: got %+v", src)
	}
}

func TestParseArenaToolSources_NameFallbacks(t *testing.T) {
	loaded := "metadata:\n  name: meta_name\nspec:\n  description: d\n  mode: live\n  http:\n    url: https://l\n    method: GET\n"
	arenaJSON := mustMarshalArena(t, map[string]any{
		"tool_specs": map[string]any{
			"key_name": map[string]any{"mode": "live",
				"http": map[string]any{"url": "https://s", "method": "GET"}},
		},
		"loaded_tools": []map[string]any{{"data": []byte(loaded)}},
	})
	got := parseArenaToolSources(arenaJSON)
	if _, ok := got["key_name"]; !ok {
		t.Errorf("tool_specs empty name should fall back to map key, got %v", keysOf(got))
	}
	if _, ok := got["meta_name"]; !ok {
		t.Errorf("loaded_tools empty spec.name should fall back to metadata.name, got %v", keysOf(got))
	}
}
