package omnia

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

func TestGetLoginURL(t *testing.T) {
	p := &Provider{}
	resp, err := p.GetLoginURL(context.Background(), &deploy.LoginURLRequest{
		CallbackURL: "http://127.0.0.1:5000/cb",
		State:       "st8",
		Config:      `{"api_endpoint":"https://omnia.example.com"}`,
	})
	if err != nil {
		t.Fatalf("GetLoginURL: %v", err)
	}
	if !strings.HasPrefix(resp.AuthorizeURL, "https://omnia.example.com"+cliAuthorizePath) {
		t.Errorf("authorize URL host/path wrong: %q", resp.AuthorizeURL)
	}
	if !strings.Contains(resp.AuthorizeURL, "state=st8") ||
		!strings.Contains(resp.AuthorizeURL, url.QueryEscape("http://127.0.0.1:5000/cb")) {
		t.Errorf("authorize URL missing state/callback: %q", resp.AuthorizeURL)
	}

	if _, err := p.GetLoginURL(context.Background(),
		&deploy.LoginURLRequest{Config: ""}); err == nil {
		t.Error("expected error when api_endpoint is missing")
	}
}

func TestLoginEndpoint_EnvOverride(t *testing.T) {
	t.Setenv(envDashboardURL, "http://localhost:3000/")
	p := &Provider{}
	// With the override set, the base comes from the env var even when the
	// config carries no api_endpoint.
	resp, err := p.GetLoginURL(context.Background(), &deploy.LoginURLRequest{
		CallbackURL: "http://127.0.0.1:5000/cb", State: "s", Config: "",
	})
	if err != nil {
		t.Fatalf("GetLoginURL with env override: %v", err)
	}
	if !strings.HasPrefix(resp.AuthorizeURL, "http://localhost:3000"+cliAuthorizePath) {
		t.Errorf("env override not used (and trailing slash not trimmed): %q", resp.AuthorizeURL)
	}
}

func TestCompleteLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != cliTokenPath {
			http.NotFound(w, r)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["code"] != "democode" {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"token": "omnia_sk_x",
			"profile": map[string]interface{}{
				"api_endpoint": "https://omnia.example.com",
				"workspace":    "demo",
				"providers": []interface{}{
					map[string]interface{}{"name": "ollama", "ref": "ollama", "role": "llm"},
				},
			},
		})
	}))
	defer srv.Close()

	p := &Provider{}
	cfg := `{"api_endpoint":"` + srv.URL + `"}`

	resp, err := p.CompleteLogin(context.Background(), &deploy.CompleteLoginRequest{
		Params: map[string]string{"code": "democode"},
		Config: cfg,
	})
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	if resp.Token != "omnia_sk_x" || resp.Profile["workspace"] != "demo" {
		t.Errorf("unexpected result: token=%q profile=%v", resp.Token, resp.Profile)
	}
	// The single provider had no "default" name → promoted.
	first := resp.Profile["providers"].([]interface{})[0].(map[string]interface{})
	if first["name"] != defaultProviderName {
		t.Errorf("primary not promoted to default: %v", first)
	}

	// Missing code → error before any HTTP call.
	if _, err := p.CompleteLogin(context.Background(), &deploy.CompleteLoginRequest{
		Params: map[string]string{}, Config: cfg,
	}); err == nil {
		t.Error("expected error for missing code")
	}

	// Missing endpoint → error.
	if _, err := p.CompleteLogin(context.Background(), &deploy.CompleteLoginRequest{
		Params: map[string]string{"code": "democode"}, Config: "",
	}); err == nil {
		t.Error("expected error for missing endpoint")
	}

	// Server-side failure → error.
	bad := `{"api_endpoint":"` + srv.URL + `"}`
	if _, err := p.CompleteLogin(context.Background(), &deploy.CompleteLoginRequest{
		Params: map[string]string{"code": "wrong"}, Config: bad,
	}); err == nil {
		t.Error("expected error on non-2xx exchange")
	}
}

func TestEnsureDefaultProviderName(t *testing.T) {
	// None named default → first is promoted.
	p := map[string]interface{}{"providers": []interface{}{
		map[string]interface{}{"name": "a"},
		map[string]interface{}{"name": "b"},
	}}
	ensureDefaultProviderName(p)
	if p["providers"].([]interface{})[0].(map[string]interface{})["name"] != defaultProviderName {
		t.Error("first provider should be promoted to default")
	}

	// Already has a default → unchanged.
	p2 := map[string]interface{}{"providers": []interface{}{
		map[string]interface{}{"name": "a"},
		map[string]interface{}{"name": "default"},
	}}
	ensureDefaultProviderName(p2)
	if p2["providers"].([]interface{})[0].(map[string]interface{})["name"] != "a" {
		t.Error("should not change a profile that already has a default")
	}

	// No providers / empty → no panic.
	ensureDefaultProviderName(map[string]interface{}{})
	ensureDefaultProviderName(map[string]interface{}{"providers": []interface{}{}})
}
