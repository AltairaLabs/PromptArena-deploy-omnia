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

// PostDeployment submits a DeployIntent to the workspace's deploy-intent
// endpoint. The dashboard proxies it to the operator, which owns the
// translation into CRDs. A non-2xx returns the typed *HTTPError so the caller
// can tell "this server has no deploy-intent API" (404/405/400-version) from a
// genuine failure.
func (c *httpClient) PostDeployment(ctx context.Context, intent json.RawMessage) (*DeployResult, error) {
	url := fmt.Sprintf("%s/deployments", c.baseURL)
	req, err := c.newRequest(ctx, http.MethodPost, url, intent)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post deployment: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, c.readError(resp)
	}
	var result DeployResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode deployment response: %w", err)
	}
	return &result, nil
}

//nolint:revive // interface implementation
func (c *httpClient) GetDeployProfile(ctx context.Context) (*DeployProfile, error) {
	url := fmt.Sprintf("%s/deploy-profile", c.baseURL)
	req, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get deploy profile: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, c.readError(resp)
	}
	var profile DeployProfile
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return nil, fmt.Errorf("decode deploy profile: %w", err)
	}
	return &profile, nil
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
		phase := ""
		if it.Status != nil {
			phase = it.Status.Phase
		}
		out = append(out, ProviderSummary{
			Name: it.Metadata.Name, Type: spec.Type, Model: spec.Model, Role: spec.Role, Phase: phase,
		})
	}
	return out, nil
}

//nolint:revive // interface implementation
func (c *httpClient) ListToolRegistries(ctx context.Context) ([]ToolRegistrySummary, error) {
	url := fmt.Sprintf("%s/toolregistries", c.baseURL)
	req, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list toolregistries: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, c.readError(resp)
	}
	var items []ResourceResponse
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode toolregistries: %w", err)
	}
	out := make([]ToolRegistrySummary, 0, len(items))
	for _, it := range items {
		tools, dynamic := extractRegistryTools(it.Spec)
		out = append(out, ToolRegistrySummary{
			Name:    it.Metadata.Name,
			Tools:   tools,
			Dynamic: dynamic,
		})
	}
	return out, nil
}

// extractRegistryTools pulls the LLM-facing tool name + input schema from each
// of a ToolRegistry's spec.handlers[] that declares an inline tool. It also
// reports whether the registry is "dynamic": a handler with no inline tool
// (e.g. an openapi handler with a specURL, or an mcp handler) resolves its tools
// externally, so its tool set can't be enumerated or schema-checked here.
func extractRegistryTools(spec json.RawMessage) (tools []RegistryTool, dynamic bool) {
	var s struct {
		Handlers []struct {
			Tool *struct {
				Name        string          `json:"name"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tool"`
		} `json:"handlers"`
	}
	_ = json.Unmarshal(spec, &s) // a malformed/empty spec simply yields no tools
	tools = make([]RegistryTool, 0, len(s.Handlers))
	for _, h := range s.Handlers {
		if h.Tool == nil || h.Tool.Name == "" {
			dynamic = true // tools resolved externally (openapi/mcp) — not enumerable here
			continue
		}
		tools = append(tools, RegistryTool{Name: h.Tool.Name, InputSchema: h.Tool.InputSchema})
	}
	return tools, dynamic
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

// workspaceNamespaceRef is the {"name": ...} namespace reference carried by
// both workspace response shapes.
type workspaceNamespaceRef struct {
	Name string `json:"name"`
}

// workspaceNamespaceHolder is the object that wraps that reference — the
// projection nests it under "workspace", the raw CRD under "spec".
type workspaceNamespaceHolder struct {
	Namespace workspaceNamespaceRef `json:"namespace"`
}

// workspaceEnvelope decodes both shapes at once so the caller can take
// whichever one the API actually sent.
type workspaceEnvelope struct {
	Workspace workspaceNamespaceHolder `json:"workspace"`
	Spec      workspaceNamespaceHolder `json:"spec"`
}

//nolint:revive // interface implementation
func (c *httpClient) GetWorkspace(ctx context.Context, name string) (*WorkspaceInfo, error) {
	url := fmt.Sprintf("%s/api/workspaces/%s", c.endpoint, name)
	req, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get workspace %s: %w", name, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, c.readError(resp)
	}
	// Two shapes, both real. GET /api/workspaces/{name} normally returns a
	// projection — {workspace: {namespace: {...}}, access: {...}} — and only
	// returns the raw CRD (with spec.namespace) for an owner asking ?view=full.
	// Decoding only the CRD shape silently yielded an empty namespace on every
	// ordinary call, which made tool-credential provisioning degrade 100% of the
	// time with a message blaming permissions.
	var ws workspaceEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&ws); err != nil {
		return nil, fmt.Errorf("decode workspace: %w", err)
	}
	ns := ws.Workspace.Namespace.Name
	if ns == "" {
		ns = ws.Spec.Namespace.Name
	}
	return &WorkspaceInfo{Namespace: ns}, nil
}

//nolint:revive // interface implementation
func (c *httpClient) CreateSecret(ctx context.Context, namespace, name string, data map[string]string) error {
	body, err := json.Marshal(map[string]interface{}{
		"namespace": namespace, "name": name, "data": data,
	})
	if err != nil {
		return fmt.Errorf("marshal secret: %w", err)
	}
	url := fmt.Sprintf("%s/api/secrets", c.endpoint)
	req, err := c.newRequest(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("create secret %s/%s: %w", namespace, name, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close
	if resp.StatusCode >= http.StatusBadRequest {
		return c.readError(resp)
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
