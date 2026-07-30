package omnia

import (
	"context"
	"fmt"
	"os"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// provisionToolCredentials best-effort-creates the Secret the pack's tool
// handlers reference — the auth stanza's bearer token and every
// headersFromSecret entry — reading each env var's value from the adapter's
// environment. It NEVER errors: any failure (no namespace, missing env value,
// CreateSecret rejected) degrades to a clear warning naming the Secret,
// namespace, and keys the operator must supply. When the pack needs no
// credentials it is a no-op success.
//
// Unset env vars do NOT suppress the whole Secret. The keys are independent —
// one absent header credential must not cost the deploy its auth token — so
// whatever resolved is written and the rest is warned about. provisioned is true
// only when every key made it.
func provisionToolCredentials(
	ctx context.Context, client omniaClient, pack *prompt.Pack, cfg *Config,
) (provisioned bool, warnings []string) {
	secretName, envVars := collectToolCredentials(pack, cfg)
	if len(envVars) == 0 {
		return true, nil // no credential to provision
	}

	data, missing := readCredentialEnv(envVars)
	if len(missing) > 0 {
		warnings = append(warnings, missingEnvWarning(secretName, missing))
	}
	if len(data) == 0 {
		return false, warnings // nothing resolved — there is no Secret to write
	}

	ns, err := resolveNamespace(ctx, client, cfg.Workspace)
	if err != nil {
		return false, append(warnings, referenceOnlyWarning(secretName, "", envVars,
			fmt.Sprintf("could not resolve the workspace namespace: %v", err)))
	}

	if err := client.CreateSecret(ctx, ns, secretName, data); err != nil {
		return false, append(warnings, referenceOnlyWarning(secretName, ns, envVars,
			fmt.Sprintf("secret creation was rejected: %v", err)))
	}
	return len(missing) == 0, warnings
}

// reportCredentialProvisioning runs the best-effort provisioning step and streams
// any advisory warnings through the apply progress reporter. It is purely
// advisory: warnings do not alter the apply result (the deploy still proceeds,
// with the auth stanza referencing the Secret the operator must supply).
func reportCredentialProvisioning(ctx context.Context, ac *applyContext) {
	_, warnings := provisionToolCredentials(ctx, ac.client, ac.pack, ac.cfg)
	pct := float64(stepToolRegistry) * progressStepSize
	for _, w := range warnings {
		_ = ac.reporter.Progress("credentials: "+w, pct)
	}
}

// readCredentialEnv reads each env var's value, collecting the names of any that
// are unset/empty so the caller can degrade with an actionable warning.
func readCredentialEnv(envVars []string) (data map[string]string, missing []string) {
	data = make(map[string]string, len(envVars))
	for _, v := range envVars {
		val := os.Getenv(v)
		if val == "" {
			missing = append(missing, v)
			continue
		}
		data[v] = val
	}
	return data, missing
}

// resolveNamespace resolves the workspace's target namespace for the Secret.
func resolveNamespace(ctx context.Context, client omniaClient, workspace string) (string, error) {
	wi, err := client.GetWorkspace(ctx, workspace)
	if err != nil {
		return "", err
	}
	if wi.Namespace == "" {
		return "", fmt.Errorf("workspace %q has no spec.namespace.name", workspace)
	}
	return wi.Namespace, nil
}

func missingEnvWarning(secretName string, missing []string) string {
	return fmt.Sprintf("tool credential key(s) %v not provisioned: those env var(s) are unset in "+
		"the deploy environment — add them to Secret %q (raw values) in the workspace namespace, "+
		"or the auth/header(s) using them will not be sent",
		missing, secretName)
}

func referenceOnlyWarning(secretName, ns string, envVars []string, why string) string {
	loc := secretName
	if ns != "" {
		loc = ns + "/" + secretName
	}
	return fmt.Sprintf("tool credentials not provisioned (%s) — pre-create Secret %q with key(s) %v "+
		"(raw token values); the tool auth stanza references it", why, loc, envVars)
}
