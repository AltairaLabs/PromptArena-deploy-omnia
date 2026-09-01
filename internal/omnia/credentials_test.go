package omnia

import (
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/packspec"
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
	pack := &prompt.Pack{Pack: packspec.Pack{ID: "p", Tools: map[string]*prompt.PackTool{
		"a": {Name: "a"}, "b": {Name: "b"}, "c": {Name: "c"},
	}}}
	cfg := &Config{sourceTools: map[string]*httpToolSource{
		"a": {URL: "https://x/a", HeadersFromEnv: []string{"Authorization=SPLITZ_AUTH"}},
		"b": {URL: "https://x/b", HeadersFromEnv: []string{"Authorization=SPLITZ_AUTH"}}, // shared → one key
		"c": {URL: "https://x/c", HeadersFromEnv: []string{"X-Act-As-User=ACT_USER"}},    // non-auth → also a credential now
	}}
	name, envVars := collectToolCredentials(pack, cfg)
	if name != sanitizeName("p-tool-credentials") {
		t.Errorf("secret name = %q", name)
	}
	// EVERY env-sourced header is a credential, not just Authorization: the pack
	// gives no way to tell a secret header from a context one, so all of them get
	// a Secret key rather than a plaintext value in the CRD.
	if len(envVars) != 2 || envVars[0] != "ACT_USER" || envVars[1] != "SPLITZ_AUTH" {
		t.Errorf("envVars = %v, want [ACT_USER SPLITZ_AUTH] (deduped, sorted)", envVars)
	}
}

func TestBuildSecretHeaders(t *testing.T) {
	t.Setenv("ACT_USER", "u-123")
	src := &httpToolSource{HeadersFromEnv: []string{
		"Authorization=SPLITZ_AUTH", // auth → the auth stanza, NOT a secret header
		"X-Act-As-User=ACT_USER",    // set → key provisioned
		"X-Missing=NOT_SET_XYZ",     // unset → declared, but reported
	}}
	headers, unset := buildSecretHeaders(src, "p-tool-credentials")

	// The header points at a Secret key. Crucially the VALUE is absent: emitting
	// "u-123" here would write it into the ToolRegistry in plaintext.
	ref := headers["X-Act-As-User"]
	if ref["name"] != "p-tool-credentials" || ref["key"] != "ACT_USER" {
		t.Errorf("X-Act-As-User = %v, want a secretRef to key ACT_USER", ref)
	}
	for hdr, r := range headers {
		for _, v := range r {
			if v == "u-123" {
				t.Errorf("header %q leaks the resolved value into the CRD: %v", hdr, r)
			}
		}
	}
	if _, ok := headers["Authorization"]; ok {
		t.Errorf("Authorization is owned by the auth stanza, not headersFromSecret: %v", headers)
	}

	// An unset env var still declares the header — it just cannot be provisioned.
	if _, ok := headers["X-Missing"]; !ok {
		t.Errorf("X-Missing should still be declared, got %v", headers)
	}
	if len(unset) != 1 || !strings.Contains(unset[0], "X-Missing") {
		t.Errorf("unset = %v, want [X-Missing (env NOT_SET_XYZ)]", unset)
	}

	if h, u := buildSecretHeaders(nil, "s"); h != nil || u != nil {
		t.Errorf("nil src: got %v %v", h, u)
	}
}

// TestSecretHeadersNeverCollideWithAuth pins the invariant that makes an
// operator-side guard unnecessary here.
//
// Omnia#1996: headersFromSecret rejects only a literal Authorization key, and in
// buildHTTPHeaders the secret-header loop runs AFTER the auth block — so a secret
// header named the same as the handler's auth header silently overwrites the
// credential. The adapter cannot produce that: parseAuthEnv routes Authorization
// to the auth stanza and everything else to headersFromSecret, and the auth
// stanza is always bearer/Authorization, so the two sets are disjoint by
// construction. If the auth stanza ever gains a custom header name, this fails.
func TestSecretHeadersNeverCollideWithAuth(t *testing.T) {
	t.Setenv("ACT_AS", "u")
	t.Setenv("TOK", "t")
	src := &httpToolSource{URL: "https://x", HeadersFromEnv: []string{
		"Authorization=TOK", "X-Act-As-User=ACT_AS"}}

	headers, _ := buildSecretHeaders(src, "s")
	auth := buildAuthStanza(src, "s")
	if auth == nil {
		t.Fatal("expected an auth stanza for Authorization=TOK")
	}
	for hdr := range headers {
		if strings.EqualFold(hdr, authHeaderName) {
			t.Errorf("secret header %q collides with the auth header and would overwrite "+
				"the credential at call time (Omnia#1996)", hdr)
		}
	}
	if _, ok := headers["X-Act-As-User"]; !ok {
		t.Errorf("non-auth header must still be emitted, got %v", headers)
	}
}

func TestHeaderEnvWarnings(t *testing.T) {
	// NOT_SET_XYZ is unset → a warning naming the tool + header.
	pack := &prompt.Pack{Pack: packspec.Pack{ID: "p", Tools: map[string]*prompt.PackTool{"c": {Name: "c"}}}}
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
