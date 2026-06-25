package omnia

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// cliAuthorizePath is the dashboard route that runs the workspace's OIDC login
// and redirects back to the CLI loopback with a one-time code.
const cliAuthorizePath = "/api/cli/authorize"

// cliTokenPath is the back-channel route that exchanges the one-time code for a
// scoped token and the assembled deploy profile.
const cliTokenPath = "/api/cli/token" //nolint:gosec // URL path, not a credential

// envDashboardURL overrides the base used for the login routes (/api/cli/*).
// The /api/cli/authorize + /api/cli/token routes live in the dashboard; this lets
// the dashboard be a different origin than the deploy config's api_endpoint
// (e.g. http://localhost:3000 in local dev). When unset, api_endpoint is used.
const envDashboardURL = "OMNIA_DASHBOARD_URL"

// loginHTTPClient performs the back-channel code exchange. A package var so
// tests can point it at a mock server.
var loginHTTPClient = &http.Client{}

// Compile-time check that the Omnia provider implements the login capability.
var _ deploy.LoginProvider = (*Provider)(nil)

// GetLoginURL builds the Omnia authorize URL the CLI opens in the browser. The
// dashboard handles OIDC (any IdP) and redirects to the loopback callback.
func (p *Provider) GetLoginURL(
	_ context.Context, req *deploy.LoginURLRequest,
) (*deploy.LoginURLResponse, error) {
	endpoint, err := loginEndpoint(req.Config)
	if err != nil {
		return nil, err
	}
	authorizeURL := fmt.Sprintf("%s%s?callback=%s&state=%s",
		endpoint, cliAuthorizePath,
		url.QueryEscape(req.CallbackURL), url.QueryEscape(req.State))
	return &deploy.LoginURLResponse{AuthorizeURL: authorizeURL}, nil
}

// CompleteLogin exchanges the one-time callback code (back-channel) for a scoped
// token and the deploy profile, then ensures a primary provider is named.
func (p *Provider) CompleteLogin(
	ctx context.Context, req *deploy.CompleteLoginRequest,
) (*deploy.CompleteLoginResponse, error) {
	endpoint, err := loginEndpoint(req.Config)
	if err != nil {
		return nil, err
	}
	code := req.Params["code"]
	if code == "" {
		return nil, fmt.Errorf("login callback is missing the authorization code")
	}

	body, _ := json.Marshal(map[string]string{"code": code})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+cliTokenPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := loginHTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("exchange login code: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("login code exchange failed (HTTP %d)", resp.StatusCode)
	}

	var out struct {
		Token   string                 `json:"token"`
		Profile map[string]interface{} `json:"profile"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode login response: %w", err)
	}
	ensureDefaultProviderName(out.Profile)
	return &deploy.CompleteLoginResponse{Profile: out.Profile, Token: out.Token}, nil
}

// loginEndpoint extracts the Omnia api_endpoint from the (possibly partial)
// deploy config the CLI passed. The endpoint is non-secret and identifies which
// Omnia to authenticate against, so it must be set before login.
func loginEndpoint(configJSON string) (string, error) {
	// An explicit dashboard URL wins — it may differ from the management-API
	// api_endpoint (or be a local dev server).
	if override := os.Getenv(envDashboardURL); override != "" {
		return strings.TrimRight(override, "/"), nil
	}
	if configJSON == "" {
		return "", fmt.Errorf("set api_endpoint in deploy.config (or %s) before running login", envDashboardURL)
	}
	cfg, err := parseConfig(configJSON)
	if err != nil {
		return "", fmt.Errorf("invalid deploy config: %w", err)
	}
	ep := cfg.endpointRoot()
	if ep == "" {
		return "", fmt.Errorf("set api_endpoint in deploy.config (or %s) before running login", envDashboardURL)
	}
	return ep, nil
}

// ensureDefaultProviderName guards the no-primary trap at login time: if the
// returned profile lists providers but none is named "default", the first is
// promoted so the runtime has a deliberate primary (see defaultProviderIndex).
func ensureDefaultProviderName(profile map[string]interface{}) {
	raw, ok := profile["providers"].([]interface{})
	if !ok || len(raw) == 0 {
		return
	}
	for _, item := range raw {
		if m, ok := item.(map[string]interface{}); ok {
			if name, _ := m["name"].(string); name == defaultProviderName {
				return // a primary is already named
			}
		}
	}
	if m, ok := raw[0].(map[string]interface{}); ok {
		m["name"] = defaultProviderName
	}
}
