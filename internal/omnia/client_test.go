package omnia

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestHTTPClient(t *testing.T, handler http.Handler) *httpClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &httpClient{
		baseURL:    server.URL,
		endpoint:   server.URL,
		token:      "test-token",
		httpClient: server.Client(),
	}
}

func TestHTTPClient_CreateResource(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/promptpacks") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("unexpected auth header: %q", auth)
		}

		w.WriteHeader(http.StatusCreated)
		resp := ResourceResponse{
			Kind: "PromptPack",
			Metadata: ResourceMetadata{
				Name:            "test-pack",
				UID:             "uid-123",
				ResourceVersion: "1",
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	client := newTestHTTPClient(t, handler)
	body := json.RawMessage(`{"kind":"PromptPack","metadata":{"name":"test-pack"}}`)

	resp, err := client.CreateResource(context.Background(), ResTypePromptPack, "test-pack", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Kind != "PromptPack" {
		t.Errorf("expected kind %q, got %q", "PromptPack", resp.Kind)
	}
	if resp.Metadata.UID != "uid-123" {
		t.Errorf("expected UID %q, got %q", "uid-123", resp.Metadata.UID)
	}
}

func TestHTTPClient_GetResource(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/agents/test-runtime") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		resp := ResourceResponse{
			Kind: "AgentRuntime",
			Metadata: ResourceMetadata{
				Name: "test-runtime",
				UID:  "uid-456",
			},
			Status: &ResourceStatus{Phase: "Running"},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	client := newTestHTTPClient(t, handler)

	resp, err := client.GetResource(context.Background(), ResTypeAgentRuntime, "test-runtime")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Metadata.Name != "test-runtime" {
		t.Errorf("expected name %q, got %q", "test-runtime", resp.Metadata.Name)
	}
	if resp.Status == nil || resp.Status.Phase != "Running" {
		t.Error("expected status phase Running")
	}
}

func TestHTTPClient_DeleteResource(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/toolregistries/test-tools") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})

	client := newTestHTTPClient(t, handler)

	err := client.DeleteResource(context.Background(), ResTypeToolRegistry, "test-tools")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPClient_Health(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/api/health") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	client := newTestHTTPClient(t, handler)

	err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPClient_ErrorResponse(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"access denied"}`))
	})

	client := newTestHTTPClient(t, handler)

	_, err := client.GetResource(context.Background(), ResTypePromptPack, "test-pack")
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("expected error to contain status code 403, got: %q", err.Error())
	}
}

func TestHTTPClient_UpdateResource(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/agents/test-agent") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ResourceResponse{
			Kind:     "AgentRuntime",
			Metadata: ResourceMetadata{Name: "test-agent", ResourceVersion: "2"},
		})
	})

	client := newTestHTTPClient(t, handler)
	resp, err := client.UpdateResource(
		context.Background(), ResTypeAgentRuntime, "test-agent",
		json.RawMessage(`{"spec":{}}`),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Metadata.ResourceVersion != "2" {
		t.Errorf("expected resourceVersion 2, got %q", resp.Metadata.ResourceVersion)
	}
}

func TestHTTPClient_ListResources(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/promptpacks") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("labelSelector"); got != "app=x" {
			t.Errorf("expected labelSelector app=x, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]ResourceResponse{
			{Kind: "PromptPack", Metadata: ResourceMetadata{Name: "p1"}},
		})
	})

	client := newTestHTTPClient(t, handler)
	items, err := client.ListResources(context.Background(), ResTypePromptPack, "app=x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 || items[0].Metadata.Name != "p1" {
		t.Errorf("unexpected list result: %v", items)
	}
}

func TestHTTPClient_ValidateProvider(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/providers/claude-prod") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})

	client := newTestHTTPClient(t, handler)
	if err := client.ValidateProvider(context.Background(), "claude-prod"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPClient_ValidateProvider_NotFound(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	client := newTestHTTPClient(t, handler)
	if err := client.ValidateProvider(context.Background(), "missing"); err == nil {
		t.Fatal("expected error for missing provider")
	}
}

func TestHTTPClient_ValidateSkillSource_Ready(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/skills/shared-skills") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ResourceResponse{
			Kind:     "SkillSource",
			Metadata: ResourceMetadata{Name: "shared-skills"},
			Status:   &ResourceStatus{Phase: "Ready"},
		})
	})

	client := newTestHTTPClient(t, handler)
	if err := client.ValidateSkillSource(context.Background(), "shared-skills"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPClient_ValidateSkillSource_NotFound(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	client := newTestHTTPClient(t, handler)
	err := client.ValidateSkillSource(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error for missing skill source")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got: %v", err)
	}
}

func TestHTTPClient_ValidateSkillSource_NotReady(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ResourceResponse{
			Kind:     "SkillSource",
			Metadata: ResourceMetadata{Name: "syncing-skills"},
			Status:   &ResourceStatus{Phase: "Syncing"},
		})
	})

	client := newTestHTTPClient(t, handler)
	err := client.ValidateSkillSource(context.Background(), "syncing-skills")
	if err == nil {
		t.Fatal("expected error for not-synced skill source")
	}
	if !strings.Contains(err.Error(), "not synced") || !strings.Contains(err.Error(), "Syncing") {
		t.Errorf("expected not-synced error mentioning phase, got: %v", err)
	}
}

func TestHTTPClient_ValidateSkillSource_NoStatus(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ResourceResponse{
			Kind:     "SkillSource",
			Metadata: ResourceMetadata{Name: "no-status"},
		})
	})

	client := newTestHTTPClient(t, handler)
	err := client.ValidateSkillSource(context.Background(), "no-status")
	if err == nil {
		t.Fatal("expected error when status is nil")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("expected phase \"unknown\" when status nil, got: %v", err)
	}
}

func TestNewHTTPClient(t *testing.T) {
	t.Setenv("OMNIA_API_TOKEN", "")
	cfg := &Config{
		APIEndpoint: "https://omnia.example.com/",
		Workspace:   "ws1",
		APIToken:    "tok",
	}
	c, err := newHTTPClient(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hc, ok := c.(*httpClient)
	if !ok {
		t.Fatal("expected *httpClient")
	}
	if hc.baseURL != "https://omnia.example.com/api/workspaces/ws1" {
		t.Errorf("unexpected baseURL: %q", hc.baseURL)
	}
	if hc.endpoint != "https://omnia.example.com" {
		t.Errorf("unexpected endpoint: %q", hc.endpoint)
	}

	// Missing token is an error.
	if _, err := newHTTPClient(&Config{APIEndpoint: "https://x", Workspace: "w"}); err == nil {
		t.Fatal("expected error when no token configured")
	}
}
