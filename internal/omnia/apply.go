package omnia

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// numApplyPhases is the total number of apply phases for progress tracking.
const numApplyPhases = 4

// progressStepSize is the fraction of the progress bar each phase occupies.
const progressStepSize = 1.0 / numApplyPhases

// Apply phase step indices.
const (
	stepPromptPack   = 0
	stepToolRegistry = 1
	stepAgentPolicy  = 2
	stepAgentRuntime = 3
)

// applyContext holds parsed inputs for the Apply method.
type applyContext struct {
	pack     *prompt.Pack
	cfg      *Config
	reporter *adaptersdk.ProgressReporter
	client   omniaClient
	priorMap map[string]ResourceState
}

// Apply executes a deployment plan, streaming progress events via the callback.
func (p *Provider) Apply(
	ctx context.Context, req *deploy.PlanRequest, callback deploy.ApplyCallback,
) (string, error) {
	cfg, err := parseConfig(req.DeployConfig)
	if err != nil {
		return "", fmt.Errorf("omnia: failed to parse deploy config: %w", err)
	}
	if cfg.DryRun {
		return p.applyDryRun(req, callback)
	}

	pack, err := adaptersdk.ParsePack([]byte(req.PackJSON))
	if err != nil {
		return "", fmt.Errorf("omnia: failed to parse pack: %w", err)
	}

	client, err := p.clientFunc(cfg)
	if err != nil {
		return "", fmt.Errorf("omnia: failed to create client: %w", err)
	}

	cfg.PackJSON = req.PackJSON

	ac := &applyContext{
		pack:     pack,
		cfg:      cfg,
		reporter: adaptersdk.NewProgressReporter(callback),
		client:   client,
		priorMap: parsePriorState(req.PriorState),
	}

	resources, applyErr := executeApplyPhases(ctx, ac)

	state := AdapterState{
		Resources: resources,
		PackID:    pack.ID,
		Version:   pack.Version,
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("omnia: failed to marshal state: %w", err)
	}

	return string(stateJSON), applyErr
}

// executeApplyPhases runs all deploy phases in dependency order.
func executeApplyPhases(ctx context.Context, ac *applyContext) ([]ResourceState, error) {
	var resources []ResourceState
	var applyErr error

	// Phase 0: PromptPack (dashboard folds pack content into a managed ConfigMap)
	res, err := applyResourcePhase(ctx, ac, stepPromptPack, ResTypePromptPack,
		sanitizeName(ac.pack.ID),
		func() (json.RawMessage, error) { return buildPromptPackRequest(ac.pack, ac.cfg) })
	resources = append(resources, res...)
	applyErr = combineErrors(applyErr, err)

	// Phase 1: ToolRegistry (if the deploy config declares tool handlers)
	if len(ac.cfg.Tools) > 0 {
		res, err = applyResourcePhase(ctx, ac, stepToolRegistry, ResTypeToolRegistry,
			sanitizeName(ac.pack.ID+"-tools"),
			func() (json.RawMessage, error) { return buildToolRegistryRequest(ac.pack, ac.cfg) })
		resources = append(resources, res...)
		applyErr = combineErrors(applyErr, err)
	}

	// Phase 2: AgentPolicy (if pack has tool policy)
	if hasToolPolicy(ac.pack) {
		res, err = applyResourcePhase(ctx, ac, stepAgentPolicy, ResTypeAgentPolicy,
			sanitizeName(ac.pack.ID+"-policy"),
			func() (json.RawMessage, error) { return buildAgentPolicyRequest(ac.pack, ac.cfg) })
		resources = append(resources, res...)
		applyErr = combineErrors(applyErr, err)
	}

	// Phase 3: AgentRuntime(s)
	names := agentRuntimeNames(ac.pack)
	for i, name := range names {
		agentName := name // capture for closure
		pct := float64(stepAgentRuntime)*progressStepSize +
			float64(i)/float64(len(names)+1)*progressStepSize
		if cbErr := ac.reporter.Progress(
			fmt.Sprintf("Creating %s: %s", ResTypeAgentRuntime, sanitizeName(name)), pct,
		); cbErr != nil {
			return resources, cbErr
		}
		res, err = applyResourcePhase(ctx, ac, stepAgentRuntime, ResTypeAgentRuntime,
			sanitizeName(name),
			func() (json.RawMessage, error) { return buildAgentRuntimeRequest(ac.pack, agentName, ac.cfg) })
		resources = append(resources, res...)
		applyErr = combineErrors(applyErr, err)

		// On a successful create/update, surface a dashboard deep-link so the
		// operator can open the agent immediately. The /agents/[name] route
		// keys on the AgentRuntime metadata.name = sanitizeName(agentName).
		if err == nil && agentRuntimeSucceeded(res) {
			if cbErr := reportAgentAccessURL(ac, agentName, pct); cbErr != nil {
				return resources, cbErr
			}
		}
	}

	return resources, applyErr
}

// agentRuntimeSucceeded reports whether the AgentRuntime phase produced a
// created/updated resource (not failed/skipped).
func agentRuntimeSucceeded(res []ResourceState) bool {
	for _, r := range res {
		if r.Type == ResTypeAgentRuntime &&
			(r.Status == ResStatusCreated || r.Status == ResStatusUpdated) {
			return true
		}
	}
	return false
}

// reportAgentAccessURL emits a Progress event with the dashboard deep-link for
// a freshly deployed AgentRuntime.
func reportAgentAccessURL(ac *applyContext, agentName string, pct float64) error {
	url := fmt.Sprintf("%s/agents/%s?workspace=%s",
		ac.cfg.endpointRoot(), sanitizeName(agentName), ac.cfg.Workspace)
	return ac.reporter.Progress(
		fmt.Sprintf("Agent %q ready — open: %s", agentName, url), pct)
}

// applyResourcePhase creates or updates a single resource, reporting progress.
func applyResourcePhase(
	ctx context.Context,
	ac *applyContext,
	stepIndex int,
	resType, name string,
	buildBody func() (json.RawMessage, error),
) ([]ResourceState, error) {
	pct := float64(stepIndex) * progressStepSize
	_, hasPrior := ac.priorMap[resourceKey(resType, name)]
	action := deploy.ActionCreate
	verb := "Creating"
	status := ResStatusCreated
	if hasPrior {
		action = deploy.ActionUpdate
		verb = "Updating"
		status = ResStatusUpdated
	}

	if cbErr := ac.reporter.Progress(fmt.Sprintf("%s %s: %s", verb, resType, name), pct); cbErr != nil {
		return nil, cbErr
	}

	body, err := buildBody()
	if err != nil {
		deployErr := newDeployError("build", resType, name, err)
		_ = ac.reporter.Error(deployErr)
		return []ResourceState{{Type: resType, Name: name, Status: ResStatusFailed}}, deployErr
	}

	var resp *ResourceResponse
	if hasPrior {
		resp, err = ac.client.UpdateResource(ctx, resType, name, body)
	} else {
		resp, err = ac.client.CreateResource(ctx, resType, name, body)
	}
	if err != nil {
		deployErr := newDeployError(verb, resType, name, err)
		_ = ac.reporter.Error(deployErr)
		return []ResourceState{{Type: resType, Name: name, Status: ResStatusFailed}}, deployErr
	}

	if cbErr := ac.reporter.Resource(&deploy.ResourceResult{
		Type: resType, Name: name, Action: action,
		Status: status, Detail: resp.Metadata.UID,
	}); cbErr != nil {
		return nil, cbErr
	}

	return []ResourceState{{
		Type:            resType,
		Name:            name,
		UID:             resp.Metadata.UID,
		ResourceVersion: resp.Metadata.ResourceVersion,
		Status:          status,
	}}, nil
}

// applyDryRun generates a deployment preview without API calls.
func (p *Provider) applyDryRun(
	req *deploy.PlanRequest, callback deploy.ApplyCallback,
) (string, error) {
	pack, err := adaptersdk.ParsePack([]byte(req.PackJSON))
	if err != nil {
		return "", fmt.Errorf("omnia: failed to parse pack: %w", err)
	}

	cfg, cfgErr := parseConfig(req.DeployConfig)
	if cfgErr != nil {
		return "", fmt.Errorf("omnia: failed to parse deploy config: %w", cfgErr)
	}

	reporter := adaptersdk.NewProgressReporter(callback)
	desired := generateDesiredResources(pack, cfg)

	resources := make([]ResourceState, 0, len(desired))
	for i, d := range desired {
		pct := float64(i) / float64(len(desired)+1)
		msg := fmt.Sprintf("Planned %s: %s", d.Type, d.Name)

		if cbErr := reporter.Progress(msg, pct); cbErr != nil {
			return "", cbErr
		}
		if cbErr := reporter.Resource(&deploy.ResourceResult{
			Type: d.Type, Name: d.Name, Action: d.Action,
			Status: ResStatusPlanned, Detail: d.Detail,
		}); cbErr != nil {
			return "", cbErr
		}
		resources = append(resources, ResourceState{
			Type: d.Type, Name: d.Name, Status: ResStatusPlanned,
		})
	}

	state := AdapterState{
		Resources: resources,
		PackID:    pack.ID,
		Version:   pack.Version,
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("omnia: failed to marshal state: %w", err)
	}
	return string(stateJSON), nil
}

// parsePriorState deserializes the prior state string into a lookup map.
func parsePriorState(priorState string) map[string]ResourceState {
	priorMap := make(map[string]ResourceState)
	if priorState == "" {
		return priorMap
	}
	var state AdapterState
	if err := json.Unmarshal([]byte(priorState), &state); err != nil {
		return priorMap
	}
	for _, r := range state.Resources {
		priorMap[resourceKey(r.Type, r.Name)] = r
	}
	return priorMap
}
