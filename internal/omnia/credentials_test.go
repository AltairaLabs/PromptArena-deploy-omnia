package omnia

import (
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

func TestParseAuthEnv(t *testing.T) {
	authEnv, others := parseAuthEnv([]string{"Authorization=GITHUB_TOKEN", "X-Act-As-User=ACT_USER"})
	if authEnv != "GITHUB_TOKEN" {
		t.Errorf("authEnv = %q, want GITHUB_TOKEN", authEnv)
	}
	if len(others) != 1 || others[0] != "X-Act-As-User" {
		t.Errorf("others = %v, want [X-Act-As-User]", others)
	}
	if a, o := parseAuthEnv(nil); a != "" || len(o) != 0 {
		t.Errorf("empty input: got %q %v", a, o)
	}
}

func TestCollectToolCredentials(t *testing.T) {
	pack := &prompt.Pack{ID: "p", Tools: map[string]*prompt.PackTool{
		"a": {Name: "a"}, "b": {Name: "b"}, "c": {Name: "c"},
	}}
	cfg := &Config{sourceTools: map[string]*httpToolSource{
		"a": {URL: "https://x/a", HeadersFromEnv: []string{"Authorization=SPLITZ_AUTH"}},
		"b": {URL: "https://x/b", HeadersFromEnv: []string{"Authorization=SPLITZ_AUTH"}}, // shared → one key
		"c": {URL: "https://x/c", HeadersFromEnv: []string{"X-Act-As-User=ACT_USER"}},    // non-auth → warning
	}}
	name, envVars, warnings := collectToolCredentials(pack, cfg)
	if name != sanitizeName("p-tool-credentials") {
		t.Errorf("secret name = %q", name)
	}
	if len(envVars) != 1 || envVars[0] != "SPLITZ_AUTH" {
		t.Errorf("envVars = %v, want [SPLITZ_AUTH] (deduped)", envVars)
	}
	if !hasSubstr(warnings, "X-Act-As-User") {
		t.Errorf("expected a non-auth-header warning, got %v", warnings)
	}
}

func hasSubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
