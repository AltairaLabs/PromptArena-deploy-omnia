package omnia

import (
	"context"
	"fmt"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// adoptResTypes are the managed resource types adopt reconciles against the
// cluster. They mirror the four types Apply manages.
var adoptResTypes = []string{
	ResTypePromptPack,
	ResTypeToolRegistry,
	ResTypeAgentPolicy,
	ResTypeAgentRuntime,
}

// adoptPriorState reconciles prior state against the cluster: it lists THIS
// pack's managed resources by label and returns them as the prior state,
// superseding any passed-in req.PriorState. This makes the cluster the source
// of truth so a lost/stale/empty local state file (or an out-of-band deploy)
// can't cause a blind CREATE of a resource that already exists.
//
// Adoption is scoped strictly to OUR resources for THIS pack: the labelSelector
// filters server-side on pack-id, and every returned resource is double-checked
// client-side to carry both managed-by=managedByValue and pack-id=pack.ID before
// being adopted. Anything else (another pack's resources, hand-made resources)
// is never adopted, updated, or deleted.
//
// If any ListResources call fails, it returns (nil, err) so the caller can fall
// back to the passed-in req.PriorState.
func (p *Provider) adoptPriorState(
	ctx context.Context, pack *prompt.Pack, cfg *Config,
) ([]ResourceState, error) {
	client, err := p.clientFunc(cfg)
	if err != nil {
		return nil, fmt.Errorf("omnia: failed to create client for adopt: %w", err)
	}

	labelSelector := fmt.Sprintf("%s=%s", LabelPackID, pack.ID)

	var adopted []ResourceState
	for _, resType := range adoptResTypes {
		items, listErr := client.ListResources(ctx, resType, labelSelector)
		if listErr != nil {
			return nil, fmt.Errorf("omnia: failed to list %s for adopt: %w", resType, listErr)
		}
		for i := range items {
			it := items[i]
			if !ownedByThisPack(it.Metadata.Labels, pack.ID) {
				continue
			}
			adopted = append(adopted, ResourceState{Type: resType, Name: it.Metadata.Name})
		}
	}
	return adopted, nil
}

// ownedByThisPack reports whether a resource's labels mark it as managed by
// promptarena AND belonging to this pack. It is the client-side defensive
// double-check that backs the server-side labelSelector — adopt never touches a
// resource that fails this test.
func ownedByThisPack(labels map[string]string, packID string) bool {
	return labels[LabelManagedBy] == managedByValue && labels[LabelPackID] == packID
}
