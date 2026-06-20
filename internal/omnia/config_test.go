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
	if len(cfg.Providers) != 1 || cfg.Providers["openai"] != "openai-prod" {
		t.Errorf("Providers = %v, want map[openai:openai-prod]", cfg.Providers)
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
		Providers:   map[string]string{"openai": "openai-prod"},
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
			Providers:   map[string]string{"default": "claude-prod"},
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
