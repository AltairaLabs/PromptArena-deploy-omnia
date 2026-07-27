package omnia

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	"sigs.k8s.io/yaml"
)

// The schema VALIDATION in crd_contract_test.go cannot catch a field the CRD no
// longer has. ValidateCustomResource only checks that what you emit is valid;
// unknown fields are not an error there. The apiserver removes them in a
// separate step — structural-schema pruning — and then returns success.
//
// That is the exact failure mode behind Omnia#1775: the adapter kept emitting
// externalAuth.sharedToken / apiKeys, the role claim mappings and handler
// selectors long after they were removed, every deploy reported 201, and the
// settings silently did nothing.
//
// These tests close that blind spot by running the real pruner over each
// builder's output and failing when anything is dropped. Combined with the
// version pin in testdata/crds/VERSION, a CRD that deletes a field the adapter
// still emits becomes a red build instead of a silent production no-op.

// loadStructural loads a vendored CRD as the structural schema the apiserver
// prunes against.
func loadStructural(t *testing.T, crdFile string) *structuralschema.Structural {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "crds", crdFile))
	if err != nil {
		t.Fatalf("read CRD %s: %v", crdFile, err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		t.Fatalf("unmarshal CRD %s: %v", crdFile, err)
	}
	for i := range crd.Spec.Versions {
		s := crd.Spec.Versions[i].Schema
		if s == nil || s.OpenAPIV3Schema == nil {
			continue
		}
		internal := &apiextensions.JSONSchemaProps{}
		if cerr := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(
			s.OpenAPIV3Schema, internal, nil,
		); cerr != nil {
			t.Fatalf("convert schema for %s: %v", crdFile, cerr)
		}
		structural, serr := structuralschema.NewStructural(internal)
		if serr != nil {
			t.Fatalf("build structural schema for %s: %v", crdFile, serr)
		}
		return structural
	}
	t.Fatalf("CRD %s has no openAPIV3Schema", crdFile)
	return nil
}

// assertNothingPruned runs the apiserver's pruner over a built body and fails
// with the exact dotted paths it removed. A pruned path means the adapter is
// emitting a field this CRD does not have: the deploy would succeed and the
// setting would vanish.
func assertNothingPruned(t *testing.T, crdFile, kind string, body []byte) {
	t.Helper()

	var obj map[string]interface{}
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	obj["apiVersion"] = crdAPIVersion
	obj["kind"] = kind

	before, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal before: %v", err)
	}

	pruning.Prune(obj, loadStructural(t, crdFile), true)

	after, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal after: %v", err)
	}
	if string(before) == string(after) {
		return
	}

	var b, a map[string]interface{}
	_ = json.Unmarshal(before, &b)
	_ = json.Unmarshal(after, &a)
	dropped := prunedPaths(b, a, "")
	sort.Strings(dropped)
	t.Errorf("%s: the CRD pruned %d field(s) the adapter emitted: %v\n"+
		"  These would be silently discarded by the apiserver — the deploy returns "+
		"success and the setting does nothing. Either stop emitting them or map them "+
		"onto the field that replaced them.", kind, len(dropped), dropped)
}

// prunedPaths walks two decoded objects and reports the dotted paths present in
// before but absent (or emptied) in after.
func prunedPaths(before, after map[string]interface{}, prefix string) []string {
	var dropped []string
	for key, bv := range before {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		av, present := after[key]
		if !present {
			dropped = append(dropped, path)
			continue
		}
		bMap, bOK := bv.(map[string]interface{})
		aMap, aOK := av.(map[string]interface{})
		if bOK && aOK {
			dropped = append(dropped, prunedPaths(bMap, aMap, path)...)
			continue
		}
		bArr, bArrOK := bv.([]interface{})
		aArr, aArrOK := av.([]interface{})
		if bArrOK && aArrOK && len(bArr) == len(aArr) {
			for i := range bArr {
				bItem, bItemOK := bArr[i].(map[string]interface{})
				aItem, aItemOK := aArr[i].(map[string]interface{})
				if bItemOK && aItemOK {
					dropped = append(dropped, prunedPaths(bItem, aItem, path)...)
				}
			}
			continue
		}
		if !reflect.DeepEqual(bv, av) {
			dropped = append(dropped, path)
		}
	}
	return dropped
}

// TestCRDPruning_HarnessCatchesRemovedField is the negative control: a body
// carrying a field the CRD does not have MUST be reported, proving the harness
// actually detects the removal class rather than passing vacuously.
func TestCRDPruning_HarnessCatchesRemovedField(t *testing.T) {
	body := []byte(`{
		"metadata": {"name": "probe"},
		"spec": {
			"promptPackRef": {"name": "p"},
			"facades": [{"type": "websocket", "handler": "runtime"}],
			"externalAuth": {"totallyNotACRDField": {"secretRef": {"name": "s"}}}
		}
	}`)

	fake := &testing.T{}
	assertNothingPruned(fake, "agentruntimes.yaml", "AgentRuntime", body)
	if !fake.Failed() {
		t.Error("the pruning harness did not flag an unknown field; it would not catch " +
			"a CRD field removal either")
	}
}

func TestCRDPruning_AgentRuntime(t *testing.T) {
	assertNothingPruned(t, "agentruntimes.yaml", "AgentRuntime", mustBuildAgentRuntime(t))
}

// TestCRDPruning_AgentRuntime_FullConfigSurface prunes an AgentRuntime built
// from every optional block the adapter exposes. This is the case that matters:
// the minimal body exercises almost none of the fields Omnia has been changing.
func TestCRDPruning_AgentRuntime_FullConfigSurface(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("parse pack: %v", err)
	}
	limit := int32(5)
	allowMP := false
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com",
		Workspace:   "test-ws",
		APIToken:    "test-token",
		Providers:   Providers{{Name: "default", Ref: "claude-prod", Role: roleLLM}},
		Runtime:     &RuntimeConfig{Replicas: 2, CPU: "100m", Memory: "128Mi"},
		ExternalAuth: &ExternalAuthConfig{
			AllowManagementPlane: &allowMP,
			APIKeys:              &APIKeysAuthConfig{DefaultRole: "editor", TrustEndUserHeader: true},
			OIDC: &OIDCAuthConfig{
				Issuer: "https://issuer.example.com", Audience: "omnia",
				ClaimMapping: &OIDCClaimMappingConfig{Subject: "sub", EndUser: "actor"},
			},
			EdgeTrust: &EdgeTrustAuthConfig{
				HeaderMapping:     &EdgeTrustHeaderMappingConfig{Subject: "x-sub", Email: "x-mail"},
				ClaimsFromHeaders: map[string]string{"x-groups": "groups"},
			},
		},
		Memory: &MemoryConfig{
			Enabled: true,
			Retrieval: &MemoryRetrievalConfig{
				Strategy: "keyword", Limit: &limit,
				AccessFilter: &MemoryAccessFilterConfig{DenyCEL: "identity.role == 'guest'"},
			},
		},
		Evals: &EvalsConfig{
			Enabled: true,
			Inline:  &EvalPathConfig{Groups: []string{"fast-running"}},
			Worker:  &EvalPathConfig{Groups: []string{"slow-running"}},
		},
	}

	body, err := buildAgentRuntimeRequest(pack, "test-pack", "", cfg)
	if err != nil {
		t.Fatalf("build AgentRuntime: %v", err)
	}
	assertNothingPruned(t, "agentruntimes.yaml", "AgentRuntime", body)
}

func TestCRDPruning_ToolRegistry(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("parse pack: %v", err)
	}
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com",
		Workspace:   "test-ws",
		APIToken:    "test-token",
		Tools: []ToolHandler{{
			Name: "search",
			Type: "http",
			Tool: &HandlerTool{
				Name: "search", Description: "search",
				InputSchema: map[string]interface{}{"type": "object"},
			},
			HTTPConfig: map[string]interface{}{"endpoint": "https://api.example.com", "method": "POST"},
		}},
	}
	body, err := buildToolRegistryRequest(pack, cfg)
	if err != nil {
		t.Fatalf("build ToolRegistry: %v", err)
	}
	assertNothingPruned(t, "toolregistries.yaml", "ToolRegistry", body)
}

func TestCRDPruning_AgentPolicy(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(policyPackJSONForPruning))
	if err != nil {
		t.Fatalf("parse pack: %v", err)
	}
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com", Workspace: "test-ws", APIToken: "t",
		resolvedRegistryName: "prune-policy-pack-tools",
	}
	body, err := buildAgentPolicyRequest(pack, cfg)
	if err != nil {
		t.Fatalf("build AgentPolicy: %v", err)
	}
	assertNothingPruned(t, "agentpolicies.yaml", "AgentPolicy", body)
}

// policyPackJSONForPruning is a pack whose prompt declares a tool blocklist.
const policyPackJSONForPruning = `{
	"id": "prune-policy-pack",
	"version": "1.0.0",
	"prompts": {
		"main": {
			"system": "hi",
			"description": "main",
			"tool_policy": {"blocklist": ["danger"]}
		}
	}
}`
