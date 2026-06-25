package omnia

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// numApplyPhases is the total number of apply phases for progress tracking.
const numApplyPhases = 4

// Progress verbs for the create/update path.
const (
	verbCreating = "Creating"
	verbUpdating = "Updating"
)

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
	provider *Provider
	pack     *prompt.Pack
	cfg      *Config
	reporter *adaptersdk.ProgressReporter
	client   omniaClient
	priorMap map[string]ResourceState
	binding  ToolBinding
}

// echoToolWarnings re-emits the resolver's advisories through the progress
// stream. Apply has no Warnings return field (the deploy.Provider.Apply
// signature is (state string, err error)), so — like reportAgentAccessURL —
// advisories surface as progress messages.
func echoToolWarnings(reporter *adaptersdk.ProgressReporter, warnings []string) error {
	for _, w := range warnings {
		if err := reporter.Progress("warning: "+w, 0); err != nil {
			return err
		}
	}
	return nil
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

	// Run the SAME tool-registry resolution plan ran, so apply binds the same
	// registry and re-echoes the same advisories without introducing new
	// failures the plan didn't show.
	binding, toolWarnings, err := resolveToolBinding(ctx, p, pack, cfg)
	if err != nil {
		return "", err
	}
	cfg.resolvedRegistryName = binding.RegistryName

	ac := &applyContext{
		provider: p,
		pack:     pack,
		cfg:      cfg,
		reporter: adaptersdk.NewProgressReporter(callback),
		client:   client,
		priorMap: p.applyPriorMap(ctx, pack, cfg, req),
		binding:  binding,
	}

	if cbErr := echoToolWarnings(ac.reporter, toolWarnings); cbErr != nil {
		return "", cbErr
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

	// Phase 1: ToolRegistry — created only in create mode (same decision plan
	// reached). Bind/none modes reference an existing registry (or none). The
	// live registry's handlers are fetched first so the merge preserves an
	// operator's completed handlers verbatim; a non-404 fetch failure fails the
	// phase (we must not silently clobber operator edits we couldn't read).
	if ac.binding.Mode == toolModeCreate {
		existing, ferr := ac.provider.currentRegistryHandlers(ctx, ac.client, ac.pack)
		if ferr != nil {
			deployErr := newDeployError("read", ResTypeToolRegistry, ac.binding.RegistryName, ferr)
			_ = ac.reporter.Error(deployErr)
			resources = append(resources, ResourceState{
				Type: ResTypeToolRegistry, Name: ac.binding.RegistryName, Status: ResStatusFailed,
			})
			applyErr = combineErrors(applyErr, deployErr)
		} else {
			res, err = applyResourcePhase(ctx, ac, stepToolRegistry, ResTypeToolRegistry,
				ac.binding.RegistryName,
				func() (json.RawMessage, error) { return buildToolRegistryRequest(ac.pack, ac.cfg, existing) })
			resources = append(resources, res...)
			applyErr = combineErrors(applyErr, err)
		}
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

// updateConflictRetries bounds the retry on a 409 Conflict. updateConflictBackoff
// is a var so tests can zero it.
const updateConflictRetries = 3

var updateConflictBackoff = 300 * time.Millisecond

// updateWithRetry issues an update, retrying on a 409 Conflict ("the object has
// been modified") so a controller mutating the resource between the server's read
// and write doesn't fail the apply. Each retry re-issues the update, prompting a
// fresh read of the latest resourceVersion server-side. A 409 AlreadyExists is
// NOT a conflict and is returned immediately.
func updateWithRetry(
	ctx context.Context, client omniaClient, resType, name string, body json.RawMessage,
) (*ResourceResponse, error) {
	var (
		resp *ResourceResponse
		err  error
	)
	for attempt := 0; ; attempt++ {
		resp, err = client.UpdateResource(ctx, resType, name, body)
		if err == nil || !isRetryableConflict(err) || attempt >= updateConflictRetries {
			return resp, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(updateConflictBackoff):
		}
	}
}

// isRetryableConflict reports whether err is a 409 Conflict (optimistic-lock,
// "object has been modified") — retryable — as distinct from a 409 AlreadyExists.
func isRetryableConflict(err error) bool {
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != httpStatusConflict {
		return false
	}
	return strings.Contains(he.Body, "Conflict") || strings.Contains(he.Body, "has been modified")
}

// isAlreadyExists reports whether err is a 409 AlreadyExists — a resource the
// adapter tried to CREATE that the cluster already has. This is distinct from
// isRetryableConflict's optimistic-lock 409 ("the object has been modified"): an
// AlreadyExists is not retried as a create, it is transparently converted to an
// update by applyResourcePhase. It can arise from a race between the adopt-list
// and the write, or when adoption itself failed and apply fell back to a stale
// local state that didn't know the resource existed.
func isAlreadyExists(err error) bool {
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != httpStatusConflict {
		return false
	}
	return strings.Contains(he.Body, "AlreadyExists") || strings.Contains(he.Body, "already exists")
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
	verb := verbCreating
	status := ResStatusCreated
	if hasPrior {
		action = deploy.ActionUpdate
		verb = verbUpdating
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
		resp, err = updateWithRetry(ctx, ac.client, resType, name, body)
	} else {
		resp, err = ac.client.CreateResource(ctx, resType, name, body)
		// Belt-and-braces: if the resource already exists (adopt missed it, or a
		// race between the adopt-list and this write created it), transparently
		// switch to an update rather than failing the apply with a 409.
		if err != nil && isAlreadyExists(err) {
			action, verb, status = deploy.ActionUpdate, verbUpdating, ResStatusUpdated
			resp, err = updateWithRetry(ctx, ac.client, resType, name, body)
		}
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
	binding := dryRunToolBinding(pack, cfg)
	cfg.resolvedRegistryName = binding.RegistryName
	desired := generateDesiredResources(pack, cfg, binding)

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

// applyPriorMap resolves the prior-state lookup map apply uses to decide
// create-vs-update. It adopts THIS pack's live resources from the cluster (the
// source of truth), superseding req.PriorState, so a lost/stale local state file
// can't make apply blind-CREATE a resource that already exists. On adopt failure
// it falls back to parsing req.PriorState — the prior behavior.
func (p *Provider) applyPriorMap(
	ctx context.Context, pack *prompt.Pack, cfg *Config, req *deploy.PlanRequest,
) map[string]ResourceState {
	adopted, aerr := p.adoptPriorState(ctx, pack, cfg)
	if aerr != nil {
		return parsePriorState(req.PriorState)
	}
	priorMap := make(map[string]ResourceState, len(adopted))
	for _, r := range adopted {
		priorMap[resourceKey(r.Type, r.Name)] = r
	}
	return priorMap
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
