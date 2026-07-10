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

	// Carry the arena source's HTTP method + URL onto the cfg that builds the
	// registry, so create-mode handlers use the real method (GET stays GET) and
	// wire live tools straight to their real URL rather than a hardcoded POST to a
	// placeholder. Mirrors Plan; degrades to the placeholder URL + POST default.
	cfg.sourceTools = extractSourceTools(req.ArenaConfig)

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
	// registry without introducing new failures the plan didn't show. The
	// advisories are discarded here — the CLI surfaces the plan's warnings on
	// apply (it has no Warnings channel of its own).
	binding, _, err := resolveToolBinding(ctx, p, pack, cfg)
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

	// Phase 1: ToolRegistry — created only in create mode, and CREATE-ONLY: the
	// registry is written exactly once, when it does not yet exist. An existing
	// one is operator-owned and never updated (unlike the other resource types).
	// Bind/none modes reference an existing registry (or none) and create nothing.
	if ac.binding.Mode == toolModeCreate {
		// Best-effort: provision the Secret the synthesized handlers' auth stanzas
		// reference (from env). Advisory only — a failure warns and never blocks.
		reportCredentialProvisioning(ctx, ac)
		res, err = applyToolRegistryCreate(ctx, ac)
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
	targets := agentRuntimeNames(ac.pack)
	for i, tgt := range targets {
		res, abort, err := applyAgentRuntimeTarget(ctx, ac, i, tgt, len(targets))
		resources = append(resources, res...)
		if abort {
			return resources, err
		}
		applyErr = combineErrors(applyErr, err)
	}

	return resources, applyErr
}

// applyAgentRuntimeTarget applies a single AgentRuntime target: it reports
// progress, creates/updates the resource, and — on a successful create/update —
// polls until the resource reconciles and, once Ready, surfaces the dashboard
// access URL. A reconcile failure/timeout is reported and folded into the
// returned error so a created-but-never-reconciled AgentRuntime fails the deploy
// loudly instead of silently reporting success.
//
// abort reports whether a progress-callback transport error occurred (broken
// stdio pipe, etc.) — the caller must treat that as an immediate abort of the
// whole apply rather than accumulating it and continuing to the next target,
// matching the existing early-return behavior for callback errors.
func applyAgentRuntimeTarget(
	ctx context.Context, ac *applyContext, i int, tgt agentRuntimeTarget, numTargets int,
) (res []ResourceState, abort bool, err error) {
	agentName := tgt.name // capture for closure
	entry := tgt.entry    // capture for closure
	pct := float64(stepAgentRuntime)*progressStepSize +
		float64(i)/float64(numTargets+1)*progressStepSize

	if cbErr := ac.reporter.Progress(
		fmt.Sprintf("Creating %s: %s", ResTypeAgentRuntime, sanitizeName(agentName)), pct,
	); cbErr != nil {
		return nil, true, cbErr
	}

	res, applyErr := applyResourcePhase(ctx, ac, stepAgentRuntime, ResTypeAgentRuntime,
		sanitizeName(agentName),
		func() (json.RawMessage, error) { return buildAgentRuntimeRequest(ac.pack, agentName, entry, ac.cfg) })

	// On a successful create/update, verify the AgentRuntime actually reconciles.
	// This is correctness-critical, so it runs BEFORE the best-effort access-URL
	// report and a reconcile failure/timeout is folded into applyErr — it is never
	// skipped by a cosmetic progress-callback error. The dashboard deep-link is
	// surfaced only once the agent is Ready (no link for an agent that failed to
	// reconcile). The /agents/[name] route keys on metadata.name = sanitizeName(agentName).
	if applyErr == nil && agentRuntimeSucceeded(res) {
		if rerr := waitForReconcile(ctx, ac.client, ResTypeAgentRuntime, sanitizeName(agentName)); rerr != nil {
			_ = ac.reporter.Error(rerr)
			applyErr = combineErrors(applyErr, rerr)
		} else if cbErr := reportAgentAccessURL(ac, agentName, pct); cbErr != nil {
			return res, true, cbErr
		}
	}

	return res, false, applyErr
}

// applyToolRegistryCreate applies the create-mode ToolRegistry under the
// CREATE-ONLY rule: it is written exactly once, when it does not yet exist, and
// an existing registry is left entirely untouched (operator-owned). Existence is
// determined two ways:
//
//   - the adopted prior state (priorMap) already carries the registry key — it
//     was found in the cluster, so skip without any API call; or
//   - the CreateResource call returns a 409 AlreadyExists (a race, or adoption
//     missed it) — unlike the generic applyResourcePhase belt-and-braces, this
//     does NOT fall back to an update; the registry becomes a no-op.
//
// In either skip case the registry is recorded as ResStatusUnchanged (never
// updated) and an advisory is reported. A genuine create succeeds as today.
func applyToolRegistryCreate(ctx context.Context, ac *applyContext) ([]ResourceState, error) {
	name := ac.binding.RegistryName
	pct := float64(stepToolRegistry) * progressStepSize

	if _, hasPrior := ac.priorMap[resourceKey(ResTypeToolRegistry, name)]; hasPrior {
		return reportRegistryUnchanged(ac, name, pct)
	}

	if cbErr := ac.reporter.Progress(
		fmt.Sprintf("%s %s: %s", verbCreating, ResTypeToolRegistry, name), pct,
	); cbErr != nil {
		return nil, cbErr
	}

	body, berr := buildToolRegistryRequest(ac.pack, ac.cfg)
	if berr != nil {
		deployErr := newDeployError("build", ResTypeToolRegistry, name, berr)
		_ = ac.reporter.Error(deployErr)
		return []ResourceState{{Type: ResTypeToolRegistry, Name: name, Status: ResStatusFailed}}, deployErr
	}

	resp, cerr := ac.client.CreateResource(ctx, ResTypeToolRegistry, name, body)
	if cerr != nil {
		// CREATE-ONLY: an AlreadyExists is a no-op, never an update.
		if isAlreadyExists(cerr) {
			return reportRegistryUnchanged(ac, name, pct)
		}
		// RBAC gate: a permission denial means this token isn't allowed to create
		// tool registries in this workspace (e.g. a GitOps/production workspace
		// where the registry is provisioned out-of-band). Degrade to a warning and
		// continue — the AgentRuntime still references the registry by name and
		// resolves its tools once an admin/GitOps provides it — rather than failing
		// the whole deploy over a governance boundary.
		if isPermissionDenied(cerr) {
			return reportRegistryPermissionSkipped(ac, name, pct)
		}
		deployErr := newDeployError(verbCreating, ResTypeToolRegistry, name, cerr)
		_ = ac.reporter.Error(deployErr)
		return []ResourceState{{Type: ResTypeToolRegistry, Name: name, Status: ResStatusFailed}}, deployErr
	}

	if cbErr := ac.reporter.Resource(&deploy.ResourceResult{
		Type: ResTypeToolRegistry, Name: name, Action: deploy.ActionCreate,
		Status: ResStatusCreated, Detail: resp.Metadata.UID,
	}); cbErr != nil {
		return nil, cbErr
	}
	return []ResourceState{{
		Type:            ResTypeToolRegistry,
		Name:            name,
		UID:             resp.Metadata.UID,
		ResourceVersion: resp.Metadata.ResourceVersion,
		Status:          ResStatusCreated,
	}}, nil
}

// reportRegistryUnchanged records the create-mode ToolRegistry as left unchanged
// (operator-owned) and reports the advisory through the progress stream. It
// performs no API write.
func reportRegistryUnchanged(ac *applyContext, name string, pct float64) ([]ResourceState, error) {
	if cbErr := ac.reporter.Progress(
		fmt.Sprintf("tool registry %q exists — left unchanged", name), pct,
	); cbErr != nil {
		return nil, cbErr
	}
	return []ResourceState{{Type: ResTypeToolRegistry, Name: name, Status: ResStatusUnchanged}}, nil
}

// reportRegistryPermissionSkipped records that the create-mode ToolRegistry was
// NOT created because the deploy token lacks permission (the RBAC gate — e.g. a
// GitOps/production workspace). It emits a clear advisory and returns no resource
// state (nothing was created) and no error, so the deploy continues: the
// AgentRuntime references the registry by name and resolves its tools once an
// admin/GitOps provisions it under that name.
func reportRegistryPermissionSkipped(ac *applyContext, name string, pct float64) ([]ResourceState, error) {
	msg := fmt.Sprintf("tool registry %q not created — the deploy token lacks permission to create "+
		"tool registries in this workspace. Provision it via GitOps (named %q) or grant the token "+
		"tool-registry create permission; the agent references it by name and resolves its tools "+
		"once it exists.", name, name)
	if cbErr := ac.reporter.Progress(msg, pct); cbErr != nil {
		return nil, cbErr
	}
	return nil, nil
}

// isPermissionDenied reports whether err is an Omnia RBAC denial (401/403),
// classified as ErrCategoryPermission by the HTTP client.
func isPermissionDenied(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Category == ErrCategoryPermission
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
