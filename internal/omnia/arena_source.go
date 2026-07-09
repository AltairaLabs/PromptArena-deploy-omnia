package omnia

import (
	"encoding/json"
	"strings"

	sigsyaml "sigs.k8s.io/yaml"
)

// httpToolSource is the full HTTP wiring the adapter parses from the arena tool
// source (req.ArenaConfig). It supersedes the method+URL-only sourceTool. A
// zero-value URL marks a tool with no live endpoint (mock / no http block).
type httpToolSource struct {
	Mode                string
	Method              string // upper-cased
	URL                 string
	TimeoutMs           int
	HeadersFromEnv      []string
	Redact              []string
	QueryParams         []string
	ResponseBodyMapping string
	RequestBodyMapping  string
	HeaderParams        map[string]string
	StaticQuery         map[string]string
	StaticHeaders       map[string]string
}

// arenaHTTP mirrors tools.HTTPConfig with json tags (sigs.k8s.io/yaml converts
// YAML→JSON, so this one struct parses both loaded_tools YAML and tool_specs JSON).
type arenaHTTP struct {
	URL            string             `json:"url"`
	Method         string             `json:"method"`
	TimeoutMs      int                `json:"timeout_ms"`
	HeadersFromEnv []string           `json:"headers_from_env"`
	Redact         []string           `json:"redact"`
	Headers        map[string]string  `json:"headers"`
	Request        *arenaHTTPRequest  `json:"request"`
	Response       *arenaHTTPResponse `json:"response"`
}

type arenaHTTPRequest struct {
	QueryParams   []string          `json:"query_params"`
	BodyMapping   string            `json:"body_mapping"`
	HeaderParams  map[string]string `json:"header_params"`
	StaticQuery   map[string]string `json:"static_query"`
	StaticHeaders map[string]string `json:"static_headers"`
}

type arenaHTTPResponse struct {
	BodyMapping string `json:"body_mapping"`
}

type arenaToolSpec struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Mode        string     `json:"mode"`
	HTTP        *arenaHTTP `json:"http"`
}

type arenaToolManifest struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec arenaToolSpec `json:"spec"`
}

type arenaSourceConfig struct {
	LoadedTools []struct {
		FilePath string `json:"file_path"`
		Data     []byte `json:"data"`
	} `json:"loaded_tools"`
	ToolSpecs map[string]*arenaToolSpec `json:"tool_specs"`
}

// parseArenaToolSources returns the full HTTP wiring for every tool the arena
// config declares, keyed by LLM-facing tool name. It reads structured tool_specs
// first, then any loaded_tools YAML not already covered. It never fails: a parse
// error or empty input yields a non-nil empty map (graceful degradation — the
// rich wiring is an enhancement over the placeholder default, never a hard
// requirement).
func parseArenaToolSources(arenaConfigJSON string) map[string]*httpToolSource {
	out := make(map[string]*httpToolSource)
	if arenaConfigJSON == "" {
		return out
	}
	var cfg arenaSourceConfig
	if err := json.Unmarshal([]byte(arenaConfigJSON), &cfg); err != nil {
		return out
	}
	parseToolSpecs(&cfg, out)
	parseLoadedTools(&cfg, out)
	return out
}

// parseToolSpecs processes structured tool_specs from arena config.
func parseToolSpecs(cfg *arenaSourceConfig, out map[string]*httpToolSource) {
	for name, spec := range cfg.ToolSpecs {
		if spec == nil {
			continue
		}
		toolName := spec.Name
		if toolName == "" {
			toolName = name
		}
		out[toolName] = sourceFromSpec(spec)
	}
}

// parseLoadedTools processes YAML tool manifests from loaded_tools.
func parseLoadedTools(cfg *arenaSourceConfig, out map[string]*httpToolSource) {
	for _, td := range cfg.LoadedTools {
		if len(td.Data) == 0 {
			continue
		}
		var m arenaToolManifest
		if err := sigsyaml.Unmarshal(td.Data, &m); err != nil {
			continue
		}
		toolName := m.Spec.Name
		if toolName == "" {
			toolName = m.Metadata.Name
		}
		if toolName == "" {
			continue
		}
		if _, exists := out[toolName]; !exists {
			out[toolName] = sourceFromSpec(&m.Spec)
		}
	}
}

// sourceFromSpec flattens an arena tool spec into httpToolSource.
func sourceFromSpec(spec *arenaToolSpec) *httpToolSource {
	src := &httpToolSource{Mode: spec.Mode}
	h := spec.HTTP
	if h == nil {
		return src
	}
	src.Method = strings.ToUpper(h.Method)
	src.URL = h.URL
	src.TimeoutMs = h.TimeoutMs
	src.HeadersFromEnv = h.HeadersFromEnv
	src.Redact = h.Redact
	src.StaticHeaders = h.Headers
	if h.Response != nil {
		src.ResponseBodyMapping = h.Response.BodyMapping
	}
	if r := h.Request; r != nil {
		src.QueryParams = r.QueryParams
		src.RequestBodyMapping = r.BodyMapping
		src.HeaderParams = r.HeaderParams
		if r.StaticQuery != nil {
			src.StaticQuery = r.StaticQuery
		}
		if r.StaticHeaders != nil {
			src.StaticHeaders = r.StaticHeaders
		}
	}
	return src
}
