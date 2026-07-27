package omnia

import (
	"strings"
	"testing"
)

// warningContaining returns the first warning containing sub, or "".
func warningContaining(warnings []string, sub string) string {
	for _, w := range warnings {
		if strings.Contains(w, sub) {
			return w
		}
	}
	return ""
}

func TestRemovedFieldWarnings_Empty(t *testing.T) {
	if got := removedFieldWarnings(&Config{}); len(got) != 0 {
		t.Errorf("a config using no removed field must warn about nothing, got %v", got)
	}
	if got := removedFieldWarnings(&Config{
		ExternalAuth: &ExternalAuthConfig{
			APIKeys: &APIKeysAuthConfig{DefaultRole: "viewer"},
			OIDC:    &OIDCAuthConfig{Issuer: "i", Audience: "a"},
		},
		Tools: []ToolHandler{{Name: "search"}},
	}); len(got) != 0 {
		t.Errorf("current-vocabulary config must warn about nothing, got %v", got)
	}
}

func TestRemovedFieldWarnings_SharedTokenMigrated(t *testing.T) {
	got := removedFieldWarnings(&Config{ExternalAuth: &ExternalAuthConfig{
		SharedToken: &SharedTokenAuthConfig{SecretRef: "agent-token", TrustEndUserHeader: true},
	}})

	w := warningContaining(got, "sharedToken")
	if w == "" {
		t.Fatalf("want a sharedToken warning, got %v", got)
	}
	// The operator must learn three things: it was removed, what it became, and
	// that their Secret is now inert — the last is the actionable part.
	for _, want := range []string{"agent-token", "migrated", "SECRET IS NO LONGER READ", "dashboard"} {
		if !strings.Contains(w, want) {
			t.Errorf("sharedToken warning must mention %q, got %q", want, w)
		}
	}
}

func TestRemovedFieldWarnings_SharedTokenIgnoredWhenAPIKeysSet(t *testing.T) {
	got := removedFieldWarnings(&Config{ExternalAuth: &ExternalAuthConfig{
		SharedToken: &SharedTokenAuthConfig{SecretRef: "agent-token"},
		APIKeys:     &APIKeysAuthConfig{DefaultRole: "editor"},
	}})

	w := warningContaining(got, "sharedToken")
	if w == "" {
		t.Fatalf("want a sharedToken warning, got %v", got)
	}
	if !strings.Contains(w, "takes precedence") {
		t.Errorf("when apiKeys is also set the warning must say it wins, got %q", w)
	}
	if strings.Contains(w, "migrated") {
		t.Errorf("nothing is migrated when apiKeys is explicit, got %q", w)
	}
}

func TestRemovedFieldWarnings_RoleMappings(t *testing.T) {
	got := removedFieldWarnings(&Config{ExternalAuth: &ExternalAuthConfig{
		OIDC: &OIDCAuthConfig{
			Issuer: "i", Audience: "a",
			ClaimMapping: &OIDCClaimMappingConfig{Subject: "sub", Role: "roles"},
		},
		EdgeTrust: &EdgeTrustAuthConfig{
			HeaderMapping: &EdgeTrustHeaderMappingConfig{Subject: "x-sub", Role: "x-role"},
		},
	}})

	oidc := warningContaining(got, "oidc.claimMapping.role")
	if oidc == "" || !strings.Contains(oidc, "identity.claims") {
		t.Errorf("oidc role warning must name the replacement, got %q", oidc)
	}
	edge := warningContaining(got, "edgeTrust.headerMapping.role")
	if edge == "" || !strings.Contains(edge, "identity.claims.role") {
		t.Errorf("edge-trust role warning must name the replacement, got %q", edge)
	}

	// A mapping that sets only the fields Omnia kept must stay silent.
	if quiet := removedFieldWarnings(&Config{ExternalAuth: &ExternalAuthConfig{
		OIDC: &OIDCAuthConfig{
			Issuer: "i", Audience: "a",
			ClaimMapping: &OIDCClaimMappingConfig{Subject: "sub", EndUser: "eu"},
		},
	}}); len(quiet) != 0 {
		t.Errorf("subject/endUser are still supported and must not warn, got %v", quiet)
	}
}

func TestRemovedFieldWarnings_HandlerSelector(t *testing.T) {
	got := removedFieldWarnings(&Config{Tools: []ToolHandler{
		{Name: "plain"},
		{Name: "discovered", Selector: map[string]interface{}{"matchLabels": map[string]string{"a": "b"}}},
	}})

	w := warningContaining(got, "selector")
	if w == "" {
		t.Fatalf("want a selector warning, got %v", got)
	}
	if !strings.Contains(w, "discovered") {
		t.Errorf("the warning must name the offending handler, got %q", w)
	}
	if strings.Contains(w, `"plain"`) {
		t.Errorf("a handler with no selector must not be named, got %q", w)
	}
	if len(got) != 1 {
		t.Errorf("want exactly one selector warning, got %v", got)
	}
}

func TestIntentClientKeys_MigratesSharedToken(t *testing.T) {
	// sharedToken alone migrates; trustEndUserHeader is the only field with a
	// counterpart, and secretRef is deliberately not represented.
	got := intentClientKeys(&ExternalAuthConfig{
		SharedToken: &SharedTokenAuthConfig{SecretRef: "s", TrustEndUserHeader: true},
	})
	if got == nil || !got.TrustEndUserHeader {
		t.Fatalf("clientKeys = %+v, want the migrated trustEndUserHeader", got)
	}
	if got.DefaultRole != "" {
		t.Errorf("a migrated sharedToken invents no defaultRole, got %q", got.DefaultRole)
	}

	// An explicit apiKeys block is the current vocabulary and must win.
	both := intentClientKeys(&ExternalAuthConfig{
		SharedToken: &SharedTokenAuthConfig{SecretRef: "s", TrustEndUserHeader: true},
		APIKeys:     &APIKeysAuthConfig{DefaultRole: "editor"},
	})
	if both == nil || both.DefaultRole != "editor" || both.TrustEndUserHeader {
		t.Errorf("explicit apiKeys must win over a sharedToken migration, got %+v", both)
	}

	if intentClientKeys(&ExternalAuthConfig{}) != nil {
		t.Error("neither block set must yield no clientKeys")
	}
}

func TestIntentExternalAuth_SharedTokenAloneStillEmitsBlock(t *testing.T) {
	// Before the migration this returned nil, so a sharedToken-only config
	// deployed an agent with no external auth at all.
	got := intentExternalAuth(&ExternalAuthConfig{
		SharedToken: &SharedTokenAuthConfig{SecretRef: "s"},
	})
	if got == nil || got.ClientKeys == nil {
		t.Fatalf("externalAuth = %+v, want a migrated clientKeys block", got)
	}
}
