package omnia

import (
	"strings"
	"testing"
)

func TestAgentConsoleURL(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{
			name: "defaults to the api endpoint",
			cfg:  &Config{APIEndpoint: "https://omnia.example.com", Workspace: "ws"},
			want: "https://omnia.example.com/agents/my-agent?workspace=ws",
		},
		{
			name: "trailing slash on the endpoint does not double up",
			cfg:  &Config{APIEndpoint: "https://omnia.example.com/", Workspace: "ws"},
			want: "https://omnia.example.com/agents/my-agent?workspace=ws",
		},
		{
			name: "explicit console endpoint wins",
			cfg: &Config{
				APIEndpoint:     "https://api.omnia.example.com",
				ConsoleEndpoint: "https://console.omnia.example.com",
				Workspace:       "ws",
			},
			want: "https://console.omnia.example.com/agents/my-agent?workspace=ws",
		},
		{
			// "Never guess" — no base means no URL, not a relative or partial one.
			name: "no endpoint at all yields nothing",
			cfg:  &Config{Workspace: "ws"},
		},
		{
			// Without the workspace the page resolves against whichever workspace
			// the dashboard last had selected, which is a wrong link, not a partial one.
			name: "no workspace yields nothing",
			cfg:  &Config{APIEndpoint: "https://omnia.example.com"},
		},
		{
			name: "nil config yields nothing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildLegacyConsoleURL(tt.cfg, "my-agent"); got != tt.want {
				t.Errorf("buildLegacyConsoleURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAgentConsoleURL_SanitizesAndEscapes(t *testing.T) {
	cfg := &Config{APIEndpoint: "https://omnia.example.com", Workspace: "my team"}

	got := buildLegacyConsoleURL(cfg, "My_Agent")
	// The agent name goes through the same sanitizer used to name the resource,
	// so the link points at the object that actually exists.
	if !strings.Contains(got, "/agents/my-agent") {
		t.Errorf("URL = %q, want the sanitized agent name", got)
	}
	// A workspace with a space must not produce a broken URL.
	if strings.Contains(got, "workspace=my team") {
		t.Errorf("URL = %q, want the workspace query-escaped", got)
	}
	if !strings.Contains(got, "workspace=my+team") && !strings.Contains(got, "workspace=my%20team") {
		t.Errorf("URL = %q, want an escaped workspace", got)
	}
}

func TestConsoleLinks(t *testing.T) {
	cfg := &Config{APIEndpoint: "https://omnia.example.com", Workspace: "ws"}

	links := legacyConsoleLinks(cfg, "my-agent")
	if len(links) != 1 {
		t.Fatalf("links = %+v, want exactly one", links)
	}
	if links[0].Label != "Console" || links[0].Rel != "console" {
		t.Errorf("link = %+v, want the conventional Console label and rel", links[0])
	}
	if links[0].URL != "https://omnia.example.com/agents/my-agent?workspace=ws" {
		t.Errorf("link URL = %q", links[0].URL)
	}
}

func TestConsoleLinks_NilWhenUnknown(t *testing.T) {
	// The protocol treats an absent Links field as "no links", and a client must
	// not synthesize one — so nil is the correct, fully-supported answer.
	if got := legacyConsoleLinks(&Config{Workspace: "ws"}, "my-agent"); got != nil {
		t.Errorf("links = %+v, want nil when the console base is unknown", got)
	}
	if got := legacyConsoleLinks(nil, "my-agent"); got != nil {
		t.Errorf("links = %+v, want nil for a nil config", got)
	}
}

func TestConsoleRoot_PrefersExplicitEndpoint(t *testing.T) {
	cfg := &Config{APIEndpoint: "https://api.example.com", ConsoleEndpoint: "https://ui.example.com/"}
	if got := cfg.consoleRoot(); got != "https://ui.example.com" {
		t.Errorf("consoleRoot() = %q, want the trimmed console endpoint", got)
	}
	bare := &Config{APIEndpoint: "https://api.example.com"}
	if got := bare.consoleRoot(); got != "https://api.example.com" {
		t.Errorf("consoleRoot() = %q, want the api endpoint fallback", got)
	}
}
