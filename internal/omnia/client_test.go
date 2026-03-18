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
		if !strings.HasSuffix(r.URL.Path, "/agentruntimes/test-runtime") {
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
		if !strings.HasSuffix(r.URL.Path, "/configmaps/test-cm") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})

	client := newTestHTTPClient(t, handler)

	err := client.DeleteResource(context.Background(), ResTypeConfigMap, "test-cm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPClient_Health(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/health") {
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
