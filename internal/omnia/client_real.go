package omnia

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// httpClient is the real HTTP implementation of omniaClient.
type httpClient struct {
	baseURL    string // workspace-scoped: {endpoint}/api/workspaces/{ws}
	endpoint   string // API root: {endpoint} (for non-workspace routes like /api/health)
	token      string
	httpClient *http.Client
}

// newHTTPClient creates a real omniaClient backed by HTTP.
func newHTTPClient(cfg *Config) (omniaClient, error) {
	token := cfg.resolveToken()
	if token == "" {
		return nil, fmt.Errorf("no API token configured")
	}
	return &httpClient{
		baseURL:    cfg.baseURL(),
		endpoint:   cfg.endpointRoot(),
		token:      token,
		httpClient: &http.Client{},
	}, nil
}

func (c *httpClient) CreateResource( //nolint:revive // interface implementation
	ctx context.Context, resType, name string, body json.RawMessage,
) (*ResourceResponse, error) {
	url := fmt.Sprintf("%s/%s", c.baseURL, resourceTypePath(resType))
	return c.doJSON(ctx, http.MethodPost, url, body)
}

//nolint:revive // interface implementation
func (c *httpClient) GetResource(ctx context.Context, resType, name string) (*ResourceResponse, error) {
	url := fmt.Sprintf("%s/%s/%s", c.baseURL, resourceTypePath(resType), name)
	return c.doJSON(ctx, http.MethodGet, url, nil)
}

func (c *httpClient) UpdateResource( //nolint:revive // interface implementation
	ctx context.Context, resType, name string, body json.RawMessage,
) (*ResourceResponse, error) {
	url := fmt.Sprintf("%s/%s/%s", c.baseURL, resourceTypePath(resType), name)
	return c.doJSON(ctx, http.MethodPut, url, body)
}

//nolint:revive // interface implementation
func (c *httpClient) DeleteResource(ctx context.Context, resType, name string) error {
	url := fmt.Sprintf("%s/%s/%s", c.baseURL, resourceTypePath(resType), name)
	req, err := c.newRequest(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete %s/%s: %w", resType, name, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close
	if resp.StatusCode >= http.StatusBadRequest {
		return c.readError(resp)
	}
	return nil
}

func (c *httpClient) ListResources( //nolint:revive // interface implementation
	ctx context.Context, resType, labelSelector string,
) ([]ResourceResponse, error) {
	url := fmt.Sprintf("%s/%s?labelSelector=%s", c.baseURL, resourceTypePath(resType), labelSelector)
	req, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", resType, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, c.readError(resp)
	}
	var items []ResourceResponse
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode list response: %w", err)
	}
	return items, nil
}

//nolint:revive // interface implementation
func (c *httpClient) ValidateProvider(ctx context.Context, name string) error {
	url := fmt.Sprintf("%s/providers/%s", c.baseURL, name)
	req, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("validate provider %s: %w", name, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close
	if resp.StatusCode >= http.StatusBadRequest {
		// Return the typed *HTTPError so callers can tell a genuine 404
		// (provider absent) from a 401/403 (token lacks provider-read
		// permission) instead of reporting every failure as "not found".
		return c.readError(resp)
	}
	return nil
}

//nolint:revive // interface implementation
func (c *httpClient) ListProviders(ctx context.Context) ([]ProviderSummary, error) {
	url := fmt.Sprintf("%s/providers", c.baseURL)
	req, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, c.readError(resp)
	}
	var items []ResourceResponse
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode providers: %w", err)
	}
	out := make([]ProviderSummary, 0, len(items))
	for _, it := range items {
		var spec struct {
			Type  string `json:"type"`
			Model string `json:"model"`
			Role  string `json:"role"`
		}
		_ = json.Unmarshal(it.Spec, &spec) // spec fields are advisory; name is what matters
		out = append(out, ProviderSummary{
			Name: it.Metadata.Name, Type: spec.Type, Model: spec.Model, Role: spec.Role,
		})
	}
	return out, nil
}

// skillSourceReadyPhase is the SkillSource status.phase value meaning synced.
const skillSourceReadyPhase = "Ready"

//nolint:revive // interface implementation
func (c *httpClient) ValidateSkillSource(ctx context.Context, name string) error {
	url := fmt.Sprintf("%s/skills/%s", c.baseURL, name)
	req, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("validate skillsource %s: %w", name, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close
	if resp.StatusCode >= http.StatusBadRequest {
		// Typed *HTTPError so a 401/403 (token lacks skill-read permission)
		// is distinguishable from a genuine 404 (SkillSource absent).
		return c.readError(resp)
	}
	var result ResourceResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode skillsource response: %w", err)
	}
	if result.Status == nil || result.Status.Phase != skillSourceReadyPhase {
		phase := "unknown"
		if result.Status != nil {
			phase = result.Status.Phase
		}
		return fmt.Errorf("skillsource %q not synced (phase %q)", name, phase)
	}
	return nil
}

func (c *httpClient) Health(ctx context.Context) error { //nolint:revive // interface implementation
	url := fmt.Sprintf("%s/api/health", c.endpoint)
	req, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("health check: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("health check failed (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// doJSON performs an HTTP request with a JSON body and decodes the response.
func (c *httpClient) doJSON(
	ctx context.Context, method, url string, body json.RawMessage,
) (*ResourceResponse, error) {
	req, err := c.newRequest(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, c.readError(resp)
	}
	var result ResourceResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

// newRequest creates an HTTP request with auth headers.
func (c *httpClient) newRequest(
	ctx context.Context, method, url string, body json.RawMessage,
) (*http.Request, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// readError reads an error response body and returns a typed *HTTPError that
// carries the status-code-driven category and remediation hint, so downstream
// classification (newDeployError) does not have to re-guess from the message.
func (c *httpClient) readError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	// The dashboard re-wraps non-404 k8s errors (409/422) as a bodyless-looking
	// 500; recover the real code so the classification (and "retry" hint) is right.
	code := effectiveStatusCode(resp.StatusCode, string(body))
	category, hint := classifyHTTPError(code)
	return &HTTPError{
		StatusCode:  code,
		Body:        string(body),
		Category:    category,
		Remediation: hint,
	}
}
