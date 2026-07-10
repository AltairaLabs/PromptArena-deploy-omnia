package omnia

import (
	"context"
	"fmt"
	"testing"
)

func TestSimulatedClient_CreateSecretAndGetWorkspace(t *testing.T) {
	sim := newSimulatedClient()
	sim.workspaces = map[string]*WorkspaceInfo{"ws": {Namespace: "agents-ns"}}
	wi, err := sim.GetWorkspace(context.Background(), "ws")
	if err != nil || wi.Namespace != "agents-ns" {
		t.Fatalf("GetWorkspace = %+v, %v", wi, err)
	}
	if err := sim.CreateSecret(context.Background(), "agents-ns", "p-tool-credentials",
		map[string]string{"GITHUB_TOKEN": "ghs_x"}); err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}
	if sim.createdSecrets["agents-ns/p-tool-credentials"]["GITHUB_TOKEN"] != "ghs_x" {
		t.Errorf("secret not recorded: %v", sim.createdSecrets)
	}
	// Injected failure path.
	sim.createSecretErr = fmt.Errorf("forbidden")
	if err := sim.CreateSecret(context.Background(), "ns", "n", nil); err == nil {
		t.Error("expected injected CreateSecret error")
	}
	// Unknown workspace → typed not-found.
	if _, err := sim.GetWorkspace(context.Background(), "missing"); err == nil {
		t.Error("expected error for unknown workspace")
	}
}
