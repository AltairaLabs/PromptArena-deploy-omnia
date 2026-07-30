package omnia

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// Progress checkpoints for the single-POST intent path. Unlike the passthrough
// path there are no per-resource write phases to step through — the server
// applies everything in one call — so progress marks submit, applied, verify.
const (
	intentPctSubmit  = 0.0
	intentPctApplied = 0.5
	intentPctVerify  = 0.75
)

// opDeploy is the DeployError.Operation value for a whole-deploy intent failure.
const opDeploy = "deploy"

// resTypeDeployment is the pseudo resource type used to report a failure of the
// deploy-intent call itself, which is not scoped to any single resource.
const resTypeDeployment = "deployment"

// intentKindToResType maps the Kubernetes kinds the deploy-intent API reports
// back onto the adapter's resource-type constants. ConfigMap is deliberately
// absent: the PromptPack's content ConfigMap is a server-side implementation
// detail the adapter neither names nor deletes (destroy.go).
var intentKindToResType = map[string]string{
	"PromptPack":   ResTypePromptPack,
	"ToolRegistry": ResTypeToolRegistry,
	"AgentPolicy":  ResTypeAgentPolicy,
	"AgentRuntime": ResTypeAgentRuntime,
}

// intentActionToStatus maps a server-reported action onto the adapter's
// ResourceState status vocabulary.
var intentActionToStatus = map[string]string{
	intentActionCreated:   ResStatusCreated,
	intentActionUpdated:   ResStatusUpdated,
	intentActionUnchanged: ResStatusUnchanged,
	intentActionFailed:    ResStatusFailed,
}

// executeApply runs the deploy through the server-owned deploy-intent API when
// that API is available AND the config is fully expressible in intent v1,
// falling back to the CRD passthrough phases otherwise.
//
// The fallback is deliberately narrow. It fires only when the config cannot be
// expressed (checked BEFORE any write) or when the server answers in a way that
// proves it has no deploy-intent API (also before any write — see
// isIntentUnsupported). A deploy that reached the server and partly applied is
// reported as a failure, never retried down the other path, so no resource is
// ever written twice.
func executeApply(ctx context.Context, ac *applyContext) ([]ResourceState, error) {
	if reasons := preflightIntentV1(ac.pack, ac.cfg, ac.binding); len(reasons) > 0 {
		reportIntentSkipped(ac, reasons)
		return executeApplyPhases(ctx, ac)
	}

	resources, supported, err := applyViaIntent(ctx, ac)
	if !supported {
		return executeApplyPhases(ctx, ac)
	}
	return resources, err
}

// reportIntentSkipped tells the user why this deploy uses the CRD passthrough
// path even though the server may support the deploy-intent API. Each reason
// names a config field the v1 contract cannot carry but the per-resource path
// still delivers — the only case where diverting is worth anything.
func reportIntentSkipped(ac *applyContext, reasons []string) {
	_ = ac.reporter.Progress(fmt.Sprintf(
		"Using the per-resource deploy path — %d config setting(s) cannot be expressed in the "+
			"deploy-intent contract: %s", len(reasons), strings.Join(reasons, "; ")),
		intentPctSubmit)
}

// reportRemovedFields surfaces every config setting naming a field Omnia has
// removed. Called only once the deploy-intent path is confirmed, because that
// is the only path whose server lacks these fields — the per-resource path
// targets older servers where they still work. Advisory only.
func reportRemovedFields(ac *applyContext) {
	for _, warning := range removedFieldWarnings(ac.cfg) {
		_ = ac.reporter.Progress(warning, intentPctSubmit)
	}
}

// applyViaIntent submits the whole deploy as one DeployIntent and maps the
// server's per-resource outcome into adapter state.
//
// supported reports whether the deploy-intent API answered at all. It is false
// ONLY when the server proved it does not serve this contract (no route, or an
// unknown apiVersion), in which case nothing was written and the caller may
// safely fall back. Every other error returns supported=true with the error.
func applyViaIntent(ctx context.Context, ac *applyContext) (res []ResourceState, supported bool, err error) {
	body, berr := buildDeployIntent(ac.pack, ac.cfg, ac.binding)
	if berr != nil {
		deployErr := newDeployError("build", resTypeDeployment, ac.pack.ID, berr)
		_ = ac.reporter.Error(deployErr)
		return nil, true, deployErr
	}

	if cbErr := ac.reporter.Progress(
		fmt.Sprintf("Deploying %s via the deploy-intent API (%s)", ac.pack.ID, intentAPIVersionV1),
		intentPctSubmit,
	); cbErr != nil {
		return nil, true, cbErr
	}

	// Provision the tool-credentials Secret BEFORE the POST: the handlers this
	// intent carries reference it by name for both the bearer auth stanza and
	// every headersFromSecret entry, and the server reconciles the ToolRegistry
	// as part of the call. Best-effort and advisory, exactly as on the
	// per-resource path — a failure warns and never blocks the deploy.
	if ac.binding.Mode == toolModeCreate {
		reportCredentialProvisioning(ctx, ac)
	}

	result, perr := ac.client.PostDeployment(ctx, body)
	if perr != nil {
		if isIntentUnsupported(perr) {
			reportIntentUnavailable(ac)
			return nil, false, nil
		}
		deployErr := newDeployError(opDeploy, resTypeDeployment, ac.pack.ID, perr)
		_ = ac.reporter.Error(deployErr)
		return nil, true, deployErr
	}

	// Only now is the deploy-intent path confirmed: these fields are removed on
	// THIS server, whereas on a per-resource-path server they still work.
	reportRemovedFields(ac)

	resources, reportErr := reportIntentResults(ac, result)
	if reportErr != nil {
		return resources, true, reportErr
	}
	resources = append(resources, orphanedPackObjects(ac, resources)...)

	if applyErr := intentResultError(result); applyErr != nil {
		_ = ac.reporter.Error(applyErr)
		return resources, true, applyErr
	}

	return resources, true, verifyIntentAgents(ctx, ac, resources, agentConsoleURLs(result))
}

// reportIntentUnavailable explains the fallback to the per-resource path.
func reportIntentUnavailable(ac *applyContext) {
	_ = ac.reporter.Progress(
		"This Omnia server does not serve the deploy-intent API — falling back to the "+
			"per-resource deploy path", intentPctSubmit)
}

// reportIntentResults converts the server's per-resource outcome into adapter
// resource state, emitting one Resource event per object. Kinds the adapter does
// not track (the PromptPack's content ConfigMap) are reported as progress but
// kept out of state, since the adapter has no route to delete them.
func reportIntentResults(ac *applyContext, result *DeployResult) ([]ResourceState, error) {
	resources := make([]ResourceState, 0, len(result.Results))
	for _, r := range result.Results {
		resType, tracked := intentKindToResType[r.Kind]
		if !tracked {
			if cbErr := ac.reporter.Progress(
				fmt.Sprintf("%s %s: %s", r.Kind, r.Name, r.Action), intentPctApplied,
			); cbErr != nil {
				return resources, cbErr
			}
			continue
		}

		status := intentActionToStatus[r.Action]
		if status == "" {
			status = ResStatusUnchanged
		}
		resources = append(resources, ResourceState{Type: resType, Name: r.Name, Status: status})

		result := &deploy.ResourceResult{
			Type: resType, Name: r.Name,
			Action: intentResourceAction(r.Action),
			Status: status, Detail: r.Error,
		}
		if resType == ResTypeAgentRuntime {
			result.Links = consoleLinksFromURL(r.URL)
		}
		if cbErr := ac.reporter.Resource(result); cbErr != nil {
			return resources, cbErr
		}

		if cbErr := reportPackAlreadyPublished(ac, resType, status); cbErr != nil {
			return resources, cbErr
		}
	}
	return resources, nil
}

// reportPackAlreadyPublished warns when the deploy's PromptPack came back
// unchanged. Pack objects are immutable per version, so a deploy whose pack
// content changed WITHOUT a version bump publishes nothing: the server answers
// AlreadyExists and the edited prompts never reach the cluster.
//
// Without this the deploy reports plain success, which reads as "my edit
// shipped" — the everyday inner-loop trap of tweaking a prompt and redeploying.
// The advisory names the remedy so the operator is never left guessing.
func reportPackAlreadyPublished(ac *applyContext, resType, status string) error {
	if resType != ResTypePromptPack || status != ResStatusUnchanged || ac.pack == nil {
		return nil
	}
	return ac.reporter.Progress(fmt.Sprintf(
		"pack %q version %s is already published and pack versions are immutable — its content "+
			"was NOT republished. If you changed the pack, bump its version to ship the change; "+
			"the agents keep running the published content until you do.",
		ac.pack.ID, ac.pack.Version), intentPctApplied)
}

// intentResourceAction maps a server action onto the SDK's action vocabulary.
func intentResourceAction(action string) deploy.Action {
	if action == intentActionCreated {
		return deploy.ActionCreate
	}
	return deploy.ActionUpdate
}

// orphanedPackObjects returns the PromptPack objects this pack owns in the
// cluster that the current deploy did NOT write. Two cases produce them: a
// workspace previously deployed through the CRD passthrough path (whose pack
// object is named after the pack ID, not the server's per-version name), and
// earlier versions from previous intent deploys (each version is its own
// immutable object).
//
// They are carried into state as unchanged so a later destroy tears them down —
// nothing is deleted here, because the live agent has only just been repointed
// and an in-flight canary may still reference the previous version.
func orphanedPackObjects(ac *applyContext, applied []ResourceState) []ResourceState {
	written := make(map[string]bool, len(applied))
	for _, r := range applied {
		if r.Type == ResTypePromptPack {
			written[r.Name] = true
		}
	}

	var orphans []ResourceState
	for _, prior := range ac.priorMap {
		if prior.Type != ResTypePromptPack || written[prior.Name] {
			continue
		}
		orphans = append(orphans, ResourceState{
			Type: ResTypePromptPack, Name: prior.Name, Status: ResStatusUnchanged,
		})
		_ = ac.reporter.Progress(fmt.Sprintf(
			"Superseded PromptPack %q left in place — it is tracked and will be removed on destroy",
			prior.Name), intentPctApplied)
	}
	sortResourceStates(orphans)
	return orphans
}

// sortResourceStates orders resource states by name for deterministic state
// output — priorMap iteration order is randomized by the runtime.
func sortResourceStates(states []ResourceState) {
	for i := 1; i < len(states); i++ {
		for j := i; j > 0 && states[j].Name < states[j-1].Name; j-- {
			states[j], states[j-1] = states[j-1], states[j]
		}
	}
}

// intentResultError builds a DeployError when the server reports the apply did
// not fully succeed, naming every resource that failed and why.
func intentResultError(result *DeployResult) error {
	if result.Succeeded {
		return nil
	}
	var failures []string
	for _, r := range result.Results {
		if r.Action != intentActionFailed {
			continue
		}
		failures = append(failures, fmt.Sprintf("%s %s: %s", r.Kind, r.Name, r.Error))
	}
	if len(failures) == 0 {
		failures = append(failures, "the server reported the deploy did not succeed")
	}
	return newDeployError(opDeploy, resTypeDeployment, "",
		errors.New(strings.Join(failures, "; ")))
}

// verifyIntentAgents polls each AgentRuntime the deploy wrote until it
// reconciles, then surfaces its dashboard deep-link. This is the same
// correctness check the passthrough path performs per target: a created-but-
// never-reconciled AgentRuntime fails the deploy loudly instead of reporting a
// success the cluster never delivered.
func verifyIntentAgents(
	ctx context.Context, ac *applyContext, resources []ResourceState, consoleURLs map[string]string,
) error {
	var verifyErr error
	for _, r := range resources {
		if r.Type != ResTypeAgentRuntime || !intentAgentWritten(r.Status) {
			continue
		}
		if rerr := waitForReconcile(ctx, ac.client, ResTypeAgentRuntime, r.Name); rerr != nil {
			_ = ac.reporter.Error(rerr)
			verifyErr = combineErrors(verifyErr, rerr)
			continue
		}
		if cbErr := reportAgentReady(ac, r.Name, consoleURLs[r.Name], intentPctVerify); cbErr != nil {
			return cbErr
		}
	}
	return verifyErr
}

// intentAgentWritten reports whether an AgentRuntime status means the server
// actually wrote the object (so it is worth waiting on).
func intentAgentWritten(status string) bool {
	return status == ResStatusCreated || status == ResStatusUpdated
}

// agentConsoleURLs indexes the console URLs Omnia returned, by agent name, so
// the readiness message and the structured link quote the same URL.
func agentConsoleURLs(result *DeployResult) map[string]string {
	urls := make(map[string]string, len(result.Results))
	for _, r := range result.Results {
		if r.Kind == kindAgentRuntime && r.URL != "" {
			urls[r.Name] = r.URL
		}
	}
	return urls
}

// kindAgentRuntime is the deploy-intent API's kind for an agent.
const kindAgentRuntime = "AgentRuntime"

// isIntentUnsupported reports whether an error from the deploy-intent endpoint
// proves this server does not serve the contract, so falling back to the
// per-resource path is safe.
//
// It is true for exactly three answers, all of which the server returns BEFORE
// writing anything:
//
//   - 404 — the dashboard predates the deployments route entirely;
//   - 405 — the path exists but serves no POST;
//   - 400 naming the apiVersion — the server serves a different contract version.
//
// Everything else (401/403 permission, 409/422 rejection, 5xx, transport
// failure) is a genuine failure and must NOT trigger a second apply attempt.
func isIntentUnsupported(err error) bool {
	var he *HTTPError
	if !errors.As(err, &he) {
		return false
	}
	switch he.StatusCode {
	case httpStatusNotFound, httpStatusMethodNotAllowed:
		return true
	case httpStatusBadRequest:
		return strings.Contains(he.Body, "apiVersion")
	default:
		return false
	}
}

// intentSupported probes the workspace's deploy profile to learn whether this
// server serves the deploy-intent contract the adapter emits. A probe failure is
// NOT an error: an older server has no profile field (or no route at all), which
// simply means "use the per-resource path".
func (p *Provider) intentSupported(ctx context.Context, cfg *Config) bool {
	client, err := p.clientFunc(cfg)
	if err != nil {
		return false
	}
	profile, perr := client.GetDeployProfile(ctx)
	if perr != nil {
		return false
	}
	return profile.supportsIntentV1()
}
