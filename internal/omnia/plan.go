package omnia

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// describeRefValidationError renders a CRD-reference validation failure. A
// genuine 404 keeps the familiar "<kind> <name> not found in workspace"
// wording; any other failure (notably a 401/403 where the token lacks
// read permission on the resource) surfaces the underlying error — including
// its remediation hint — instead of being mislabeled as "not found".
func describeRefValidationError(kind, name string, err error) string {
	var he *HTTPError
	if errors.As(err, &he) && he.StatusCode == httpStatusNotFound {
		return fmt.Sprintf("%s %q not found in workspace", kind, name)
	}
	return fmt.Sprintf("%s %q: %v", kind, name, err)
}

// Plan generates a deployment plan for the given pack and config.
func (p *Provider) Plan(ctx context.Context, req *deploy.PlanRequest) (*deploy.PlanResponse, error) {
	pack, err := adaptersdk.ParsePack([]byte(req.PackJSON))
	if err != nil {
		return nil, fmt.Errorf("omnia: failed to parse pack: %w", err)
	}

	cfg, err := parseConfig(req.DeployConfig)
	if err != nil {
		return nil, fmt.Errorf("omnia: invalid deploy config: %w", err)
	}
	if errs := cfg.validate(); len(errs) > 0 {
		return nil, fmt.Errorf("omnia: config validation failed: %s", errs[0])
	}

	// Validate that referenced providers and skill sources exist (skip in dry-run mode).
	if !cfg.DryRun {
		if err := p.validateProviders(ctx, cfg); err != nil {
			return nil, err
		}
		if err := p.validateSkillSources(ctx, cfg); err != nil {
			return nil, err
		}
	}

	var prior *AdapterState
	if req.PriorState != "" {
		prior = &AdapterState{}
		if err := json.Unmarshal([]byte(req.PriorState), prior); err != nil {
			return nil, fmt.Errorf("omnia: failed to parse prior state: %w", err)
		}
	}

	desired := generateDesiredResources(pack, cfg)
	changes := diffResources(desired, prior)
	summary := buildSummary(changes)

	return &deploy.PlanResponse{
		Changes:  changes,
		Summary:  summary,
		Warnings: providerWarnings(cfg.Providers),
	}, nil
}

// generateDesiredResources builds the list of desired Omnia resources from the
// pack and deploy config.
func generateDesiredResources(pack *prompt.Pack, cfg *Config) []deploy.ResourceChange {
	// Step 0: PromptPack CRD (dashboard folds pack content into a managed ConfigMap).
	desired := []deploy.ResourceChange{
		{
			Type:   ResTypePromptPack,
			Name:   sanitizeName(pack.ID),
			Action: deploy.ActionCreate,
			Detail: fmt.Sprintf("Create PromptPack for %s", pack.ID),
		},
	}

	// Step 1: ToolRegistry (if the deploy config declares tool handlers).
	if len(cfg.Tools) > 0 {
		desired = append(desired, deploy.ResourceChange{
			Type:   ResTypeToolRegistry,
			Name:   sanitizeName(pack.ID + "-tools"),
			Action: deploy.ActionCreate,
			Detail: fmt.Sprintf("Create ToolRegistry with %d handlers", len(cfg.Tools)),
		})
	}

	// Step 2: AgentPolicy (if pack has tool blocklist).
	if hasToolPolicy(pack) {
		desired = append(desired, deploy.ResourceChange{
			Type:   ResTypeAgentPolicy,
			Name:   sanitizeName(pack.ID + "-policy"),
			Action: deploy.ActionCreate,
			Detail: fmt.Sprintf("Create AgentPolicy for %s", pack.ID),
		})
	}

	// Step 3: AgentRuntime(s).
	for _, name := range agentRuntimeNames(pack) {
		desired = append(desired, deploy.ResourceChange{
			Type:   ResTypeAgentRuntime,
			Name:   sanitizeName(name),
			Action: deploy.ActionCreate,
			Detail: fmt.Sprintf("Create AgentRuntime for %s", name),
		})
	}

	return desired
}

// diffResources compares desired resources against prior state.
func diffResources(desired []deploy.ResourceChange, prior *AdapterState) []deploy.ResourceChange {
	if prior == nil || len(prior.Resources) == 0 {
		return desired
	}

	priorMap := make(map[string]ResourceState, len(prior.Resources))
	for _, r := range prior.Resources {
		priorMap[resourceKey(r.Type, r.Name)] = r
	}

	seen := make(map[string]bool, len(desired))
	changes := make([]deploy.ResourceChange, 0, len(desired)+len(prior.Resources))

	for _, d := range desired {
		key := resourceKey(d.Type, d.Name)
		seen[key] = true

		if _, exists := priorMap[key]; exists {
			changes = append(changes, deploy.ResourceChange{
				Type:   d.Type,
				Name:   d.Name,
				Action: deploy.ActionUpdate,
				Detail: fmt.Sprintf("Update %s %s", d.Type, d.Name),
			})
		} else {
			changes = append(changes, d)
		}
	}

	// Prior resources not in desired set should be deleted.
	var toDelete []ResourceState
	for _, r := range prior.Resources {
		if !seen[resourceKey(r.Type, r.Name)] {
			toDelete = append(toDelete, r)
		}
	}
	sort.Slice(toDelete, func(i, j int) bool {
		return resourceKey(toDelete[i].Type, toDelete[i].Name) < resourceKey(toDelete[j].Type, toDelete[j].Name)
	})
	for _, r := range toDelete {
		changes = append(changes, deploy.ResourceChange{
			Type:   r.Type,
			Name:   r.Name,
			Action: deploy.ActionDelete,
			Detail: fmt.Sprintf("Delete %s %s", r.Type, r.Name),
		})
	}

	return changes
}

// buildSummary produces a human-readable summary line.
func buildSummary(changes []deploy.ResourceChange) string {
	var create, update, del int
	for _, c := range changes {
		switch c.Action {
		case deploy.ActionCreate:
			create++
		case deploy.ActionUpdate:
			update++
		case deploy.ActionDelete:
			del++
		case deploy.ActionNoChange, deploy.ActionDrift:
			// not tallied
		}
	}
	return fmt.Sprintf("Plan: %d to create, %d to update, %d to delete", create, update, del)
}

// validateProviders creates a client and checks that every unique provider
// referenced in cfg.Providers exists in the Omnia workspace.
func (p *Provider) validateProviders(ctx context.Context, cfg *Config) error {
	client, err := p.clientFunc(cfg)
	if err != nil {
		return fmt.Errorf("omnia: failed to create client for provider validation: %w", err)
	}

	// Deduplicate provider refs.
	seen := make(map[string]bool, len(cfg.Providers))
	var errs []string
	for _, b := range cfg.Providers {
		if seen[b.Ref] {
			continue
		}
		seen[b.Ref] = true

		if err := client.ValidateProvider(ctx, b.Ref); err != nil {
			errs = append(errs, describeRefValidationError("provider", b.Ref, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("omnia: provider validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

// validateSkillSources creates a client and checks that every unique
// SkillSource referenced in cfg.Skills exists and is synced. No-op when the
// deploy config declares no skills.
func (p *Provider) validateSkillSources(ctx context.Context, cfg *Config) error {
	if len(cfg.Skills) == 0 {
		return nil
	}

	client, err := p.clientFunc(cfg)
	if err != nil {
		return fmt.Errorf("omnia: failed to create client for skill validation: %w", err)
	}

	// Deduplicate skill sources.
	seen := make(map[string]bool, len(cfg.Skills))
	var errs []string
	for _, b := range cfg.Skills {
		if seen[b.Source] {
			continue
		}
		seen[b.Source] = true

		if err := client.ValidateSkillSource(ctx, b.Source); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("omnia: skill validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}
