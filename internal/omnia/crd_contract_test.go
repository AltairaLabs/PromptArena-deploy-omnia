package omnia

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"
)

// These tests validate the JSON bodies the builders emit against Omnia's REAL
// CRD OpenAPI schemas, vendored under testdata/crds/ at a pinned Omnia version
// (see testdata/crds/VERSION). They run in the normal `go test` path — no
// cluster — and turn a CRD contract drift (e.g. the spec.facade -> spec.facades
// break, Omnia#1576) into a red build instead of a silent runtime failure.

const crdAPIVersion = "omnia.altairalabs.ai/v1alpha1"

// loadCRDValidator loads a vendored CRD and returns a validator for its served
// version's openAPIV3Schema — the same structural-schema validation the API
// server applies at admission.
func loadCRDValidator(t *testing.T, crdFile string) validation.SchemaValidator {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "crds", crdFile))
	if err != nil {
		t.Fatalf("read CRD %s: %v", crdFile, err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		t.Fatalf("unmarshal CRD %s: %v", crdFile, err)
	}
	var v1schema *apiextensionsv1.JSONSchemaProps
	for i := range crd.Spec.Versions {
		if s := crd.Spec.Versions[i].Schema; s != nil && s.OpenAPIV3Schema != nil {
			v1schema = s.OpenAPIV3Schema
			break
		}
	}
	if v1schema == nil {
		t.Fatalf("CRD %s has no openAPIV3Schema", crdFile)
	}
	internal := &apiextensions.JSONSchemaProps{}
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(v1schema, internal, nil); err != nil {
		t.Fatalf("convert schema for %s: %v", crdFile, err)
	}
	validator, _, err := validation.NewSchemaValidator(internal)
	if err != nil {
		t.Fatalf("build validator for %s: %v", crdFile, err)
	}
	return validator
}

// validateBody validates a built request body (metadata + spec) as a CR of the
// given kind, returning the admission-style error list.
func validateBody(t *testing.T, v validation.SchemaValidator, kind string, body []byte) field.ErrorList {
	t.Helper()
	var obj map[string]interface{}
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	obj["apiVersion"] = crdAPIVersion
	obj["kind"] = kind
	return validation.ValidateCustomResource(nil, obj, v)
}

// mustBuildAgentRuntime builds a representative AgentRuntime body.
func mustBuildAgentRuntime(t *testing.T) []byte {
	t.Helper()
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("parse pack: %v", err)
	}
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com",
		Workspace:   "test-ws",
		APIToken:    "test-token",
		Providers:   Providers{{Name: "default", Ref: "claude-prod", Role: "llm"}},
	}
	body, err := buildAgentRuntimeRequest(pack, "test-pack", "", cfg)
	if err != nil {
		t.Fatalf("build AgentRuntime: %v", err)
	}
	return body
}

// TestCRDContract_HarnessCatchesMissingFacades is the negative control: the
// pre-Omnia#1576 shape (singular spec.facade, no spec.facades) MUST fail
// validation, proving the harness actually catches the regression class.
func TestCRDContract_HarnessCatchesMissingFacades(t *testing.T) {
	v := loadCRDValidator(t, "agentruntimes.yaml")

	var obj map[string]interface{}
	if err := json.Unmarshal(mustBuildAgentRuntime(t), &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	spec := obj["spec"].(map[string]interface{})
	delete(spec, "facades")
	spec["facade"] = map[string]interface{}{"type": "websocket", "handler": "runtime"}
	obj["apiVersion"] = crdAPIVersion
	obj["kind"] = "AgentRuntime"

	if errs := validation.ValidateCustomResource(nil, obj, v); len(errs) == 0 {
		t.Fatal("expected validation errors for the missing required spec.facades, got none — harness would NOT catch the facades regression")
	}
}

// TestCRDContract_AgentRuntime asserts the real builder output is admissible.
func TestCRDContract_AgentRuntime(t *testing.T) {
	v := loadCRDValidator(t, "agentruntimes.yaml")
	if errs := validateBody(t, v, "AgentRuntime", mustBuildAgentRuntime(t)); len(errs) != 0 {
		t.Fatalf("AgentRuntime body failed CRD validation: %v", errs)
	}
}

// TestCRDContract_AgentRuntime_FanOut asserts a fanned-out agent (entry override
// -> spec.runtime.extraEnv) is admissible.
func TestCRDContract_AgentRuntime_FanOut(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("parse pack: %v", err)
	}
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com", Workspace: "test-ws", APIToken: "test-token",
		Providers: Providers{{Name: "default", Ref: "claude-prod", Role: "llm"}},
	}
	body, err := buildAgentRuntimeRequest(pack, "splitz-triage", "triage", cfg)
	if err != nil {
		t.Fatalf("build AgentRuntime: %v", err)
	}
	v := loadCRDValidator(t, "agentruntimes.yaml")
	if errs := validateBody(t, v, "AgentRuntime", body); len(errs) != 0 {
		t.Fatalf("fanned AgentRuntime body failed CRD validation: %v", errs)
	}
}

func TestCRDContract_ToolRegistry(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("parse pack: %v", err)
	}
	// Valid handler config: an http handler and an mcp handler with the
	// CRD-required mcpConfig.transport. (The adapter passes mcpConfig through
	// verbatim, so the caller is responsible for supplying transport.)
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com", Workspace: "test-ws", APIToken: "test-token",
		Providers: Providers{{Name: "default", Ref: "claude-prod", Role: "llm"}},
		Tools: []ToolHandler{
			{
				Name: "search", Type: handlerTypeHTTP,
				Tool: &HandlerTool{
					Name: "search", Description: "Search tool",
					InputSchema: map[string]interface{}{"type": "object"},
				},
				HTTPConfig: map[string]interface{}{"endpoint": "https://api.example.com/search"},
			},
			{
				Name: "knowledge", Type: handlerTypeMCP,
				MCPConfig: map[string]interface{}{"server": "knowledge-mcp", "transport": "streamable-http"},
			},
		},
	}
	body, err := buildToolRegistryRequest(pack, cfg)
	if err != nil {
		t.Fatalf("build ToolRegistry: %v", err)
	}
	v := loadCRDValidator(t, "toolregistries.yaml")
	if errs := validateBody(t, v, "ToolRegistry", body); len(errs) != 0 {
		t.Fatalf("ToolRegistry body failed CRD validation: %v", errs)
	}
}

func TestCRDContract_AgentPolicy(t *testing.T) {
	const packWithPolicy = `{
		"id": "policy-pack",
		"version": "1.0.0",
		"prompts": {
			"main": {"system": "You are helpful", "description": "Main",
				"tool_policy": {"blocklist": ["dangerous_tool", "risky_tool"]}}
		}
	}`
	pack, err := adaptersdk.ParsePack([]byte(packWithPolicy))
	if err != nil {
		t.Fatalf("parse pack: %v", err)
	}
	cfg := &Config{APIEndpoint: "https://omnia.test.com", Workspace: "test-ws", APIToken: "test-token"}
	body, err := buildAgentPolicyRequest(pack, cfg)
	if err != nil {
		t.Fatalf("build AgentPolicy: %v", err)
	}
	v := loadCRDValidator(t, "agentpolicies.yaml")
	if errs := validateBody(t, v, "AgentPolicy", body); len(errs) != 0 {
		t.Fatalf("AgentPolicy body failed CRD validation: %v", errs)
	}
}

// TestCRDContract_PromptPack validates the PromptPack body. The dashboard's
// promptpacks route transforms the body before it becomes a CR — it folds
// body.content into a managed ConfigMap and sets spec.source itself. This test
// reconciles those two known transforms (drop content, inject the
// dashboard-set source) so it validates the fields the adapter actually
// contributes (version, skills) against the real CRD schema.
func TestCRDContract_PromptPack(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("parse pack: %v", err)
	}
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com", Workspace: "test-ws", APIToken: "test-token",
		PackJSON: testPackJSON,
	}
	body, err := buildPromptPackRequest(pack, cfg)
	if err != nil {
		t.Fatalf("build PromptPack: %v", err)
	}

	var obj map[string]interface{}
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	delete(obj, "content") // dashboard folds this into a ConfigMap
	spec := obj["spec"].(map[string]interface{})
	spec["source"] = map[string]interface{}{"type": "configmap"} // dashboard sets this
	obj["apiVersion"] = crdAPIVersion
	obj["kind"] = "PromptPack"

	v := loadCRDValidator(t, "promptpacks.yaml")
	if errs := validation.ValidateCustomResource(nil, obj, v); len(errs) != 0 {
		t.Fatalf("PromptPack body failed CRD validation: %v", errs)
	}
}

// TestCRDContract_ToolRegistry_EnrichedSynthesizedHandler validates that a handler
// synthesized from a pack tool's rich arena source (responseMapping, redact,
// handler-level timeout, and inferred GET queryParams) is admissible against the
// real ToolRegistry CRD schema — locking the field NAMES to the schema so a future
// drift (e.g. responseMapping -> some renamed field) becomes a red build.
func TestCRDContract_ToolRegistry_EnrichedSynthesizedHandler(t *testing.T) {
	pack := &prompt.Pack{
		ID:      "rich-pack",
		Version: "1.0.0",
		Tools: map[string]*prompt.PackTool{
			"list_user_exercises": {
				Name:        "list_user_exercises",
				Description: "List the user's exercises",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{"search": map[string]interface{}{"type": "string"}},
				},
			},
		},
	}
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com", Workspace: "test-ws", APIToken: "test-token",
		sourceTools: map[string]*httpToolSource{
			"list_user_exercises": {
				Mode: "live", Method: "GET",
				URL:                 "https://api.example.com/exercises",
				TimeoutMs:           15000,
				Redact:              []string{"user_id"},
				ResponseBodyMapping: "[].{id: id, name: name}",
			},
		},
	}
	body, err := buildToolRegistryRequest(pack, cfg)
	if err != nil {
		t.Fatalf("build ToolRegistry: %v", err)
	}
	// Sanity: the enriched fields actually made it into the emitted handler, so a
	// silent regression that drops them can't make this test pass vacuously.
	if !bytes.Contains(body, []byte("responseMapping")) ||
		!bytes.Contains(body, []byte("queryParams")) ||
		!bytes.Contains(body, []byte(`"redact"`)) {
		t.Fatalf("enriched fields missing from emitted handler: %s", body)
	}
	v := loadCRDValidator(t, "toolregistries.yaml")
	if errs := validateBody(t, v, "ToolRegistry", body); len(errs) != 0 {
		t.Fatalf("enriched ToolRegistry body failed CRD validation: %v", errs)
	}
}
