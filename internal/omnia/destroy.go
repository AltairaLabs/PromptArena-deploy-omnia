package omnia

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// destroyEventResource is the DestroyEvent.Type for per-resource deletion events.
const destroyEventResource = "resource"

// destroyOrder defines the reverse dependency order for teardown of the
// pack-scoped resources the deploy OWNS: the AgentRuntime that runs the pack,
// its derived AgentPolicy, and the PromptPack itself. The PromptPack's managed
// content ConfigMap is cleaned up dashboard-side on PromptPack delete, so the
// adapter does not track or delete it. The ToolRegistry is intentionally absent
// — see operatorOwnedTypes.
var destroyOrder = []string{
	ResTypeAgentRuntime,
	ResTypeAgentPolicy,
	ResTypePromptPack,
}

// operatorOwnedTypes are resources the adapter creates for CONVENIENCE but does
// not own — they belong to the platform operator (pre-existing providers and
// skill registries, or a tool registry whose placeholder handlers the operator
// has completed). Destroy LEAVES these in place and only tears down the
// pack-scoped resources this deploy created; the adapter's sole owned artifact
// is the PromptPack (and its underlying pack JSON).
var operatorOwnedTypes = map[string]bool{
	ResTypeToolRegistry: true,
}

// isOperatorOwned reports whether a resource type is left untouched on destroy.
func isOperatorOwned(rtype string) bool { return operatorOwnedTypes[rtype] }

// Destroy tears down deployed resources in reverse dependency order.
func (p *Provider) Destroy(
	ctx context.Context, req *deploy.DestroyRequest, callback deploy.DestroyCallback,
) error {
	state, err := parseAdapterState(req.PriorState)
	if err != nil {
		return fmt.Errorf("omnia: failed to parse prior state: %w", err)
	}

	if len(state.Resources) == 0 {
		emitDestroyEvent(callback, "progress", "No resources to destroy")
		emitDestroyEvent(callback, "complete", "Destroy complete (nothing to do)")
		return nil
	}

	cfg, err := parseConfig(req.DeployConfig)
	if err != nil {
		return fmt.Errorf("omnia: failed to parse deploy config: %w", err)
	}

	client, err := p.clientFunc(cfg)
	if err != nil {
		return fmt.Errorf("omnia: failed to create client: %w", err)
	}

	byType := groupByType(state.Resources)

	emitDestroyEvent(callback, "progress",
		fmt.Sprintf("Destroying %d resources", countDeletable(state.Resources)))

	for step, rtype := range destroyOrder {
		resources, ok := byType[rtype]
		if !ok {
			continue
		}
		emitDestroyEvent(callback, "progress",
			fmt.Sprintf("Step %d: deleting %s resources (%d)", step+1, rtype, len(resources)))
		destroyResourceGroup(ctx, client, resources, callback)
	}

	// Handle any resource types not in the standard destroy order.
	destroyUnorderedResources(ctx, client, state.Resources, callback)

	// Report the operator-owned resources we deliberately leave in place.
	reportOperatorOwnedLeft(state.Resources, callback)

	emitDestroyEvent(callback, "complete", "Destroy complete")
	return nil
}

// countDeletable counts the resources the adapter owns and will delete (i.e. not
// operator-owned), for an accurate "Destroying N resources" message.
func countDeletable(resources []ResourceState) int {
	n := 0
	for _, r := range resources {
		if !isOperatorOwned(r.Type) {
			n++
		}
	}
	return n
}

// reportOperatorOwnedLeft emits an advisory for each operator-owned resource the
// adapter leaves untouched on destroy — it created them for convenience but does
// not own them, so teardown is the operator's call.
func reportOperatorOwnedLeft(resources []ResourceState, callback deploy.DestroyCallback) {
	for _, res := range resources {
		if !isOperatorOwned(res.Type) {
			continue
		}
		emitDestroyEvent(callback, "progress",
			fmt.Sprintf("Left %s %q in place (operator-owned)", res.Type, res.Name))
	}
}

// destroyResourceGroup deletes a slice of resources, emitting events for each.
func destroyResourceGroup(
	ctx context.Context, client omniaClient,
	resources []ResourceState, callback deploy.DestroyCallback,
) {
	for _, res := range resources {
		if err := client.DeleteResource(ctx, res.Type, res.Name); err != nil {
			deployErr := newDeployError("delete", res.Type, res.Name, err)
			_ = callback(&deploy.DestroyEvent{
				Type:    "error",
				Message: deployErr.Error(),
				Resource: &deploy.ResourceResult{
					Type: res.Type, Name: res.Name,
					Action: deploy.ActionDelete, Status: ResStatusFailed,
					Detail: deployErr.Error(),
				},
			})
			continue
		}
		_ = callback(&deploy.DestroyEvent{
			Type:    destroyEventResource,
			Message: fmt.Sprintf("Deleted %s %q", res.Type, res.Name),
			Resource: &deploy.ResourceResult{
				Type: res.Type, Name: res.Name,
				Action: deploy.ActionDelete, Status: ResStatusDeleted,
			},
		})
	}
}

// destroyUnorderedResources handles resource types not in the standard destroy order.
func destroyUnorderedResources(
	ctx context.Context, client omniaClient,
	resources []ResourceState, callback deploy.DestroyCallback,
) {
	for _, res := range resources {
		if isInDestroyOrder(res.Type) || isOperatorOwned(res.Type) {
			continue
		}
		err := client.DeleteResource(ctx, res.Type, res.Name)
		status := ResStatusDeleted
		if err != nil {
			status = ResStatusFailed
		}
		_ = callback(&deploy.DestroyEvent{
			Type:    destroyEventResource,
			Message: fmt.Sprintf("Deleted %s %q", res.Type, res.Name),
			Resource: &deploy.ResourceResult{
				Type: res.Type, Name: res.Name,
				Action: deploy.ActionDelete, Status: status,
			},
		})
	}
}

// emitDestroyEvent sends a simple destroy event.
func emitDestroyEvent(callback deploy.DestroyCallback, eventType, message string) {
	_ = callback(&deploy.DestroyEvent{Type: eventType, Message: message})
}

// groupByType builds a lookup of resources indexed by type.
func groupByType(resources []ResourceState) map[string][]ResourceState {
	byType := make(map[string][]ResourceState)
	for _, r := range resources {
		byType[r.Type] = append(byType[r.Type], r)
	}
	return byType
}

// isInDestroyOrder returns true if the resource type appears in the destroy ordering.
func isInDestroyOrder(rtype string) bool {
	for _, t := range destroyOrder {
		if t == rtype {
			return true
		}
	}
	return false
}

// parseAdapterState deserializes the opaque prior_state JSON.
func parseAdapterState(raw string) (*AdapterState, error) {
	if raw == "" {
		return &AdapterState{}, nil
	}
	var s AdapterState
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, fmt.Errorf("invalid state JSON: %w", err)
	}
	return &s, nil
}
