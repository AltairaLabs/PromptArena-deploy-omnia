package omnia

import (
	"strings"
	"testing"
)

func TestParseConfig_Valid(t *testing.T) {
	raw := `{
		"api_endpoint": "https://omnia.example.com",
		"workspace": "test-ws",
		"api_token": "tok-123",
		"providers": {"openai": "openai-prod"},
		"runtime": {"replicas": 2, "cpu": "500m", "memory": "256Mi"},
		"labels": {"env": "staging"},
		"dry_run": true
	}`

	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}

	if cfg.APIEndpoint != "https://omnia.example.com" {
		t.Errorf("APIEndpoint = %q, want %q", cfg.APIEndpoint, "https://omnia.example.com")
	}
	if cfg.Workspace != "test-ws" {
		t.Errorf("Workspace = %q, want %q", cfg.Workspace, "test-ws")
	}
	if cfg.APIToken != "tok-123" {
		t.Errorf("APIToken = %q, want %q", cfg.APIToken, "tok-123")
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "openai" || cfg.Providers[0].Ref != "openai-prod" {
		t.Errorf("Providers = %v, want [{openai openai-prod llm}]", cfg.Providers)
	}
	if cfg.Providers[0].Role != roleLLM {
		t.Errorf("Providers[0].Role = %q, want %q (legacy map defaults to llm)", cfg.Providers[0].Role, roleLLM)
	}
	if cfg.Runtime == nil || cfg.Runtime.Replicas != 2 {
		t.Errorf("Runtime.Replicas = %v, want 2", cfg.Runtime)
	}
	if cfg.Runtime.CPU != "500m" {
		t.Errorf("Runtime.CPU = %q, want %q", cfg.Runtime.CPU, "500m")
	}
	if cfg.Runtime.Memory != "256Mi" {
		t.Errorf("Runtime.Memory = %q, want %q", cfg.Runtime.Memory, "256Mi")
	}
	if cfg.Labels["env"] != "staging" {
		t.Errorf("Labels[env] = %q, want %q", cfg.Labels["env"], "staging")
	}
	if !cfg.DryRun {
		t.Error("DryRun = false, want true")
	}
}

func TestParseConfig_LegacyMapForm(t *testing.T) {
	raw := `{
		"api_endpoint": "https://omnia.example.com",
		"workspace": "ws",
		"providers": {"default": "x"}
	}`

	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(cfg.Providers))
	}
	b := cfg.Providers[0]
	if b.Name != "default" || b.Ref != "x" || b.Role != roleLLM {
		t.Errorf("binding = %+v, want {default x llm}", b)
	}
}

func TestParseConfig_NewListForm(t *testing.T) {
	raw := `{
		"api_endpoint": "https://omnia.example.com",
		"workspace": "ws",
		"providers": [
			{"name": "default", "ref": "claude-prod", "role": "llm"},
			{"name": "embed", "ref": "openai-embed", "role": "embedding"},
			{"name": "infer", "ref": "vllm", "role": "inference"}
		]
	}`

	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if len(cfg.Providers) != 3 {
		t.Fatalf("expected 3 bindings, got %d", len(cfg.Providers))
	}
	want := Providers{
		{Name: "default", Ref: "claude-prod", Role: roleLLM},
		{Name: "embed", Ref: "openai-embed", Role: roleEmbedding},
		{Name: "infer", Ref: "vllm", Role: roleInference},
	}
	for i, b := range want {
		if cfg.Providers[i] != b {
			t.Errorf("binding[%d] = %+v, want %+v", i, cfg.Providers[i], b)
		}
	}
}

func TestParseConfig_InvalidProvidersShape(t *testing.T) {
	raw := `{
		"api_endpoint": "https://omnia.example.com",
		"workspace": "ws",
		"providers": "nope"
	}`
	_, err := parseConfig(raw)
	if err == nil {
		t.Fatal("expected error for scalar providers value, got nil")
	}
}

func TestValidateConfig_InvalidRole(t *testing.T) {
	t.Setenv("OMNIA_API_TOKEN", "tok")
	cfg := &Config{
		APIEndpoint: "https://omnia.example.com",
		Workspace:   "ws",
		Providers:   Providers{{Name: "default", Ref: "claude-prod", Role: "vision"}},
	}
	errs := cfg.validate()
	found := false
	for _, e := range errs {
		if strings.Contains(e, "invalid role") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an invalid role error, got %v", errs)
	}
}

func TestValidateConfig_EmptyRef(t *testing.T) {
	t.Setenv("OMNIA_API_TOKEN", "tok")
	cfg := &Config{
		APIEndpoint: "https://omnia.example.com",
		Workspace:   "ws",
		Providers:   Providers{{Name: "default", Ref: ""}},
	}
	errs := cfg.validate()
	found := false
	for _, e := range errs {
		if strings.Contains(e, "ref is required") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a ref-required error, got %v", errs)
	}
}

func TestValidateConfig_DuplicateNames(t *testing.T) {
	t.Setenv("OMNIA_API_TOKEN", "tok")
	cfg := &Config{
		APIEndpoint: "https://omnia.example.com",
		Workspace:   "ws",
		Providers: Providers{
			{Name: "default", Ref: "a", Role: "llm"},
			{Name: "default", Ref: "b", Role: "embedding"},
		},
	}
	errs := cfg.validate()
	found := false
	for _, e := range errs {
		if strings.Contains(e, "duplicated") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a duplicate-name error, got %v", errs)
	}
}

func TestParseConfig_InvalidJSON(t *testing.T) {
	_, err := parseConfig("{bad json")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestParseConfig_Minimal(t *testing.T) {
	raw := `{
		"api_endpoint": "https://omnia.example.com",
		"workspace": "ws",
		"providers": {"default": "default-provider"}
	}`

	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if cfg.APIEndpoint != "https://omnia.example.com" {
		t.Errorf("APIEndpoint = %q, want %q", cfg.APIEndpoint, "https://omnia.example.com")
	}
	if cfg.Runtime != nil {
		t.Errorf("Runtime = %v, want nil", cfg.Runtime)
	}
	if cfg.DryRun {
		t.Error("DryRun = true, want false")
	}
}

func TestValidateConfig_AllFieldsMissing(t *testing.T) {
	t.Setenv("OMNIA_API_TOKEN", "")

	cfg := &Config{}
	errs := cfg.validate()

	required := []string{"api_endpoint", "workspace", "providers", "api_token"}
	for _, keyword := range required {
		found := false
		for _, e := range errs {
			if strings.Contains(e, keyword) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected validation error mentioning %q, got %v", keyword, errs)
		}
	}
}

func TestValidateConfig_Valid(t *testing.T) {
	t.Setenv("OMNIA_API_TOKEN", "env-token")

	cfg := &Config{
		APIEndpoint: "https://omnia.example.com",
		Workspace:   "test-ws",
		Providers:   Providers{{Name: "openai", Ref: "openai-prod"}},
	}
	errs := cfg.validate()
	if len(errs) != 0 {
		t.Errorf("expected no validation errors, got %v", errs)
	}
}

func intPtr(v int) *int { return &v }

func TestValidateConfig_Autoscaling(t *testing.T) {
	base := func(a *AutoscalingConfig) *Config {
		return &Config{
			APIEndpoint: "https://omnia.example.com",
			Workspace:   "test-ws",
			APIToken:    "tok",
			Providers:   Providers{{Name: "default", Ref: "claude-prod", Role: "llm"}},
			Runtime:     &RuntimeConfig{Autoscaling: a},
		}
	}

	tests := []struct {
		name      string
		as        *AutoscalingConfig
		wantError string // substring expected in the first error, "" = no error
	}{
		{"valid hpa", &AutoscalingConfig{Enabled: true, Type: "hpa", MinReplicas: intPtr(1), MaxReplicas: intPtr(10)}, ""},
		{"valid keda scale-to-zero", &AutoscalingConfig{Enabled: true, Type: "keda", MinReplicas: intPtr(0), MaxReplicas: intPtr(5)}, ""},
		{"valid empty block", &AutoscalingConfig{}, ""},
		{"bad type", &AutoscalingConfig{Type: "vpa"}, "must be"},
		{"negative min", &AutoscalingConfig{MinReplicas: intPtr(-1)}, "min_replicas must be >= 0"},
		{"zero max", &AutoscalingConfig{MaxReplicas: intPtr(0)}, "max_replicas must be >= 1"},
		{"min exceeds max", &AutoscalingConfig{MinReplicas: intPtr(8), MaxReplicas: intPtr(3)}, "must not exceed max_replicas"},
		{"cpu over 100", &AutoscalingConfig{TargetCPUUtilization: intPtr(150)}, "target_cpu_utilization must be between 1 and 100"},
		{"memory under 1", &AutoscalingConfig{TargetMemoryUtilization: intPtr(0)}, "target_memory_utilization must be between 1 and 100"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := base(tt.as).validate()
			if tt.wantError == "" {
				if len(errs) != 0 {
					t.Errorf("expected no errors, got %v", errs)
				}
				return
			}
			found := false
			for _, e := range errs {
				if strings.Contains(e, tt.wantError) {
					found = true
				}
			}
			if !found {
				t.Errorf("expected an error containing %q, got %v", tt.wantError, errs)
			}
		})
	}
}

func TestResolveToken_ConfigTakesPrecedence(t *testing.T) {
	t.Setenv("OMNIA_API_TOKEN", "env-token")

	cfg := &Config{APIToken: "config-token"}
	got := cfg.resolveToken()
	if got != "config-token" {
		t.Errorf("resolveToken() = %q, want %q", got, "config-token")
	}
}

func TestResolveToken_FallsBackToEnv(t *testing.T) {
	t.Setenv("OMNIA_API_TOKEN", "env-token")

	cfg := &Config{}
	got := cfg.resolveToken()
	if got != "env-token" {
		t.Errorf("resolveToken() = %q, want %q", got, "env-token")
	}
}

func TestParseConfig_ToolsBlock(t *testing.T) {
	raw := `{
		"api_endpoint": "https://omnia.example.com",
		"workspace": "ws",
		"providers": {"default": "x"},
		"tools": [
			{
				"name": "search",
				"type": "http",
				"tool": {
					"name": "search",
					"description": "Search the web",
					"inputSchema": {"type": "object"}
				},
				"httpConfig": {"endpoint": "https://api.example.com/search"},
				"timeout": "30s"
			},
			{
				"name": "knowledge",
				"type": "mcp",
				"mcpConfig": {"server": "knowledge-mcp"}
			}
		]
	}`

	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if len(cfg.Tools) != 2 {
		t.Fatalf("expected 2 tool handlers, got %d", len(cfg.Tools))
	}

	h0 := cfg.Tools[0]
	if h0.Name != "search" || h0.Type != "http" {
		t.Errorf("handler[0] = %+v, want name=search type=http", h0)
	}
	if h0.Tool == nil || h0.Tool.Name != "search" || h0.Tool.Description != "Search the web" {
		t.Errorf("handler[0].Tool = %+v", h0.Tool)
	}
	if h0.Tool.InputSchema == nil {
		t.Error("handler[0].Tool.InputSchema should be populated")
	}
	if h0.HTTPConfig["endpoint"] != "https://api.example.com/search" {
		t.Errorf("handler[0].HTTPConfig = %v", h0.HTTPConfig)
	}
	if h0.Timeout != "30s" {
		t.Errorf("handler[0].Timeout = %q, want 30s", h0.Timeout)
	}

	h1 := cfg.Tools[1]
	if h1.Name != "knowledge" || h1.Type != "mcp" {
		t.Errorf("handler[1] = %+v, want name=knowledge type=mcp", h1)
	}
	if h1.Tool != nil {
		t.Errorf("handler[1].Tool = %+v, want nil", h1.Tool)
	}
	if h1.MCPConfig["server"] != "knowledge-mcp" {
		t.Errorf("handler[1].MCPConfig = %v", h1.MCPConfig)
	}
}

func toolValidationBaseConfig(handlers []ToolHandler) *Config {
	return &Config{
		APIEndpoint: "https://omnia.example.com",
		Workspace:   "ws",
		APIToken:    "tok",
		Providers:   Providers{{Name: "default", Ref: "claude-prod", Role: "llm"}},
		Tools:       handlers,
	}
}

func TestValidateToolHandlers(t *testing.T) {
	goodTool := &HandlerTool{Name: "t", Description: "d", InputSchema: map[string]interface{}{"type": "object"}}
	httpCfg := map[string]interface{}{"endpoint": "https://x"}

	tests := []struct {
		name      string
		handlers  []ToolHandler
		wantError string // substring; "" = expect no error
	}{
		{
			name:      "empty tools is valid",
			handlers:  nil,
			wantError: "",
		},
		{
			name: "valid http handler",
			handlers: []ToolHandler{
				{Name: "search", Type: "http", Tool: goodTool, HTTPConfig: httpCfg},
			},
			wantError: "",
		},
		{
			name: "valid client handler with no config",
			handlers: []ToolHandler{
				{Name: "browser", Type: "client"},
			},
			wantError: "",
		},
		{
			name: "valid mcp via selector",
			handlers: []ToolHandler{
				{Name: "kb", Type: "mcp", Selector: map[string]interface{}{"app": "kb"}},
			},
			wantError: "",
		},
		{
			name: "invalid type",
			handlers: []ToolHandler{
				{Name: "search", Type: "rest", Tool: goodTool, HTTPConfig: httpCfg},
			},
			wantError: `invalid type "rest"`,
		},
		{
			name: "duplicate handler names",
			handlers: []ToolHandler{
				{Name: "search", Type: "http", Tool: goodTool, HTTPConfig: httpCfg},
				{Name: "search", Type: "mcp", MCPConfig: map[string]interface{}{"server": "s"}},
			},
			wantError: "duplicated",
		},
		{
			name: "http missing tool",
			handlers: []ToolHandler{
				{Name: "search", Type: "http", HTTPConfig: httpCfg},
			},
			wantError: "tool is required",
		},
		{
			name: "http with neither config nor selector",
			handlers: []ToolHandler{
				{Name: "search", Type: "http", Tool: goodTool},
			},
			wantError: "httpConfig or selector is required",
		},
		{
			name: "bad name pattern",
			handlers: []ToolHandler{
				{Name: "Bad_Name", Type: "http", Tool: goodTool, HTTPConfig: httpCfg},
			},
			wantError: "name must match",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := toolValidationBaseConfig(tt.handlers).validate()
			if tt.wantError == "" {
				if len(errs) != 0 {
					t.Errorf("expected no errors, got %v", errs)
				}
				return
			}
			found := false
			for _, e := range errs {
				if strings.Contains(e, tt.wantError) {
					found = true
				}
			}
			if !found {
				t.Errorf("expected an error containing %q, got %v", tt.wantError, errs)
			}
		})
	}
}

func TestValidateToolHandlers_GRPCAndOpenAPI(t *testing.T) {
	goodTool := &HandlerTool{Name: "t", Description: "d", InputSchema: map[string]interface{}{"type": "object"}}

	tests := []struct {
		name      string
		handlers  []ToolHandler
		wantError string
	}{
		{
			name: "valid grpc handler",
			handlers: []ToolHandler{
				{Name: "rpc", Type: "grpc", Tool: goodTool, GRPCConfig: map[string]interface{}{"service": "S"}},
			},
			wantError: "",
		},
		{
			name: "grpc missing config and selector",
			handlers: []ToolHandler{
				{Name: "rpc", Type: "grpc", Tool: goodTool},
			},
			wantError: "grpcConfig or selector is required",
		},
		{
			name: "valid openapi via config",
			handlers: []ToolHandler{
				{Name: "spec", Type: "openapi", OpenAPIConfig: map[string]interface{}{"url": "u"}},
			},
			wantError: "",
		},
		{
			name: "openapi missing config and selector",
			handlers: []ToolHandler{
				{Name: "spec", Type: "openapi"},
			},
			wantError: "openAPIConfig or selector is required",
		},
		{
			name: "http tool missing sub-fields",
			handlers: []ToolHandler{
				{Name: "x", Type: "http", Tool: &HandlerTool{}, HTTPConfig: map[string]interface{}{"endpoint": "e"}},
			},
			wantError: "tool.name is required",
		},
		{
			name: "empty handler name",
			handlers: []ToolHandler{
				{Name: "", Type: "http", Tool: goodTool, HTTPConfig: map[string]interface{}{"endpoint": "e"}},
			},
			wantError: "name is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := toolValidationBaseConfig(tt.handlers).validate()
			if tt.wantError == "" {
				if len(errs) != 0 {
					t.Errorf("expected no errors, got %v", errs)
				}
				return
			}
			found := false
			for _, e := range errs {
				if strings.Contains(e, tt.wantError) {
					found = true
				}
			}
			if !found {
				t.Errorf("expected an error containing %q, got %v", tt.wantError, errs)
			}
		})
	}
}

func TestBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		ws       string
		want     string
	}{
		{
			name:     "no trailing slash",
			endpoint: "https://omnia.example.com",
			ws:       "ws1",
			want:     "https://omnia.example.com/api/workspaces/ws1",
		},
		{
			name:     "trailing slash",
			endpoint: "https://omnia.example.com/",
			ws:       "ws2",
			want:     "https://omnia.example.com/api/workspaces/ws2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{APIEndpoint: tt.endpoint, Workspace: tt.ws}
			got := cfg.baseURL()
			if got != tt.want {
				t.Errorf("baseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
