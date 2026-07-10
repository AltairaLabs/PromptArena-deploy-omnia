package omnia

import (
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

func TestParseAuthEnv(t *testing.T) {
	authEnv, headers := parseAuthEnv([]string{"Authorization=GITHUB_TOKEN", "X-Act-As-User=ACT_USER"})
	if authEnv != "GITHUB_TOKEN" {
		t.Errorf("authEnv = %q, want GITHUB_TOKEN", authEnv)
	}
	if headers["X-Act-As-User"] != "ACT_USER" {
		t.Errorf("headers = %v, want X-Act-As-User=ACT_USER", headers)
	}
	if a, h := parseAuthEnv(nil); a != "" || len(h) != 0 {
		t.Errorf("empty input: got %q %v", a, h)
	}
}

func TestCollectToolCredentials(t *testing.T) {
	pack := &prompt.Pack{ID: "p", Tools: map[string]*prompt.PackTool{
		"a": {Name: "a"}, "b": {Name: "b"}, "c": {Name: "c"},
	}}
	cfg := &Config{sourceTools: map[string]*httpToolSource{
		"a": {URL: "https://x/a", HeadersFromEnv: []string{"Authorization=SPLITZ_AUTH"}},
		"b": {URL: "https://x/b", HeadersFromEnv: []string{"Authorization=SPLITZ_AUTH"}}, // shared → one key
		"c": {URL: "https://x/c", HeadersFromEnv: []string{"X-Act-As-User=ACT_USER"}},    // non-auth → static header, not a credential
	}}
	name, envVars := collectToolCredentials(pack, cfg)
	if name != sanitizeName("p-tool-credentials") {
		t.Errorf("secret name = %q", name)
	}
	if len(envVars) != 1 || envVars[0] != "SPLITZ_AUTH" {
		t.Errorf("envVars = %v, want [SPLITZ_AUTH] (deduped; X-Act-As-User excluded)", envVars)
	}
}

func TestBuildStaticHeaders(t *testing.T) {
	t.Setenv("ACT_USER", "u-123")
	src := &httpToolSource{HeadersFromEnv: []string{
		"Authorization=SPLITZ_AUTH", // auth → NOT a static header
		"X-Act-As-User=ACT_USER",    // set → resolved
		"X-Missing=NOT_SET_XYZ",     // unset → reported
	}}
	headers, unset := buildStaticHeaders(src)
	if headers["X-Act-As-User"] != "u-123" {
		t.Errorf("headers = %v, want X-Act-As-User=u-123", headers)
	}
	if _, ok := headers["Authorization"]; ok {
		t.Errorf("Authorization must not be a static header: %v", headers)
	}
	if len(unset) != 1 || !strings.Contains(unset[0], "X-Missing") {
		t.Errorf("unset = %v, want [X-Missing (env NOT_SET_XYZ)]", unset)
	}
	if h, u := buildStaticHeaders(nil); h != nil || u != nil {
		t.Errorf("nil src: got %v %v", h, u)
	}
}

func TestHeaderEnvWarnings(t *testing.T) {
	// NOT_SET_XYZ is unset → a warning naming the tool + header.
	pack := &prompt.Pack{ID: "p", Tools: map[string]*prompt.PackTool{"c": {Name: "c"}}}
	cfg := &Config{sourceTools: map[string]*httpToolSource{
		"c": {URL: "https://x", HeadersFromEnv: []string{"X-Act-As-User=NOT_SET_XYZ"}}}}
	warnings := headerEnvWarnings(pack, cfg)
	if !hasSubstr(warnings, "X-Act-As-User") || !hasSubstr(warnings, "c") {
		t.Errorf("expected an unset-header warning naming the tool + header, got %v", warnings)
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
