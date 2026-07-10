package omnia

import (
	"context"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

func ghAuthPack() (*prompt.Pack, *Config) {
	pack := &prompt.Pack{ID: "p", Tools: map[string]*prompt.PackTool{"a": {Name: "a"}}}
	cfg := &Config{Workspace: "ws", sourceTools: map[string]*httpToolSource{
		"a": {URL: "https://x", HeadersFromEnv: []string{"Authorization=GITHUB_TOKEN"}}}}
	return pack, cfg
}

func TestProvisionToolCredentials_Success(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghs_live")
	pack, cfg := ghAuthPack()
	sim := newSimulatedClient()
	sim.workspaces = map[string]*WorkspaceInfo{"ws": {Namespace: "ns"}}
	ok, warnings := provisionToolCredentials(context.Background(), sim, pack, cfg)
	if !ok {
		t.Fatalf("expected provisioned=true; warnings=%v", warnings)
	}
	if sim.createdSecrets["ns/"+credentialSecretName("p")]["GITHUB_TOKEN"] != "ghs_live" {
		t.Errorf("secret not created with raw token: %v", sim.createdSecrets)
	}
}

func TestProvisionToolCredentials_DegradesOnCreateFailure(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghs_live")
	pack, cfg := ghAuthPack()
	sim := newSimulatedClient()
	sim.workspaces = map[string]*WorkspaceInfo{"ws": {Namespace: "ns"}}
	sim.createSecretErr = &HTTPError{StatusCode: httpStatusForbidden, Category: ErrCategoryPermission}
	ok, warnings := provisionToolCredentials(context.Background(), sim, pack, cfg)
	if ok {
		t.Error("expected provisioned=false on create failure")
	}
	if !hasSubstr(warnings, credentialSecretName("p")) || !hasSubstr(warnings, "ns") {
		t.Errorf("degrade warning must name the secret + namespace; got %v", warnings)
	}
}

func TestProvisionToolCredentials_MissingEnvValue(t *testing.T) {
	// GITHUB_TOKEN unset → cannot provision → warning, provisioned=false.
	pack, cfg := ghAuthPack()
	sim := newSimulatedClient()
	sim.workspaces = map[string]*WorkspaceInfo{"ws": {Namespace: "ns"}}
	ok, warnings := provisionToolCredentials(context.Background(), sim, pack, cfg)
	if ok || !hasSubstr(warnings, "GITHUB_TOKEN") {
		t.Errorf("missing env value must degrade with a warning naming the var; ok=%v warnings=%v", ok, warnings)
	}
}

func TestProvisionToolCredentials_NamespaceUnresolvable(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghs_live")
	pack, cfg := ghAuthPack()
	sim := newSimulatedClient() // no workspaces → GetWorkspace 404
	ok, warnings := provisionToolCredentials(context.Background(), sim, pack, cfg)
	if ok || !hasSubstr(warnings, credentialSecretName("p")) {
		t.Errorf("unresolvable namespace must degrade to a reference-only warning; ok=%v warnings=%v", ok, warnings)
	}
}

func TestReportCredentialProvisioning_StreamsWarnings(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghs_x")
	pack, cfg := ghAuthPack()
	sim := newSimulatedClient()
	sim.workspaces = map[string]*WorkspaceInfo{"ws": {Namespace: "ns"}}
	// Force a create failure so provisioning deterministically degrades and streams
	// a warning through the reporter (independent of the ambient GITHUB_TOKEN).
	sim.createSecretErr = &HTTPError{StatusCode: httpStatusForbidden, Category: ErrCategoryPermission}
	var events []*deploy.ApplyEvent
	ac := &applyContext{
		pack: pack, cfg: cfg, client: sim,
		reporter: adaptersdk.NewProgressReporter(capturingCallback(&events)),
	}
	reportCredentialProvisioning(context.Background(), ac)
	if countContaining(progressMessages(events), "credentials:") == 0 {
		t.Errorf("expected a streamed credentials warning, got %v", progressMessages(events))
	}
}

func TestProvisionToolCredentials_NoCredentialsNoop(t *testing.T) {
	pack := &prompt.Pack{ID: "p", Tools: map[string]*prompt.PackTool{"a": {Name: "a"}}}
	cfg := &Config{Workspace: "ws", sourceTools: map[string]*httpToolSource{"a": {URL: "https://x"}}}
	sim := newSimulatedClient()
	ok, warnings := provisionToolCredentials(context.Background(), sim, pack, cfg)
	if !ok || len(warnings) != 0 {
		t.Errorf("no credentials → no-op success, got ok=%v warnings=%v", ok, warnings)
	}
}
