package omnia

import (
	"context"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

func TestGetProviderInfo(t *testing.T) {
	p := NewProvider()
	info, err := p.GetProviderInfo(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Name != "omnia" {
		t.Errorf("expected name %q, got %q", "omnia", info.Name)
	}
	if info.Version != Version {
		t.Errorf("expected version %q, got %q", Version, info.Version)
	}
	expectedCaps := map[string]bool{
		"plan": true, "apply": true, "destroy": true, "status": true,
	}
	for _, cap := range info.Capabilities {
		if !expectedCaps[cap] {
			t.Errorf("unexpected capability: %q", cap)
		}
		delete(expectedCaps, cap)
	}
	if len(expectedCaps) > 0 {
		t.Errorf("missing capabilities: %v", expectedCaps)
	}
	if info.ConfigSchema == "" {
		t.Error("expected non-empty config schema")
	}
}

func TestProviderValidateConfig_Valid(t *testing.T) {
	t.Setenv(envAPIToken, "test-token")

	p := NewProvider()
	resp, err := p.ValidateConfig(context.Background(), &deploy.ValidateRequest{
		Config: `{
			"api_endpoint": "https://omnia.test.com",
			"workspace": "test-ws",
			"providers": {"default": "claude-prod"}
		}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Valid {
		t.Errorf("expected valid config, got errors: %v", resp.Errors)
	}
}

func TestProviderValidateConfig_Invalid(t *testing.T) {
	p := NewProvider()
	resp, err := p.ValidateConfig(context.Background(), &deploy.ValidateRequest{
		Config: `{}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Valid {
		t.Error("expected invalid config for empty object")
	}
	if len(resp.Errors) == 0 {
		t.Error("expected validation errors for empty config")
	}
}

func TestImport_NotSupported(t *testing.T) {
	p := NewProvider()
	_, err := p.Import(context.Background(), &deploy.ImportRequest{})
	if err == nil {
		t.Fatal("expected error for unsupported import")
	}
	if got := err.Error(); got != "omnia: import is not yet supported" {
		t.Errorf("unexpected error message: %q", got)
	}
}
