package omnia

import (
	"context"
	"fmt"
	"os"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// provisionToolCredentials best-effort-creates the Secret the pack's tool auth
// stanzas reference, reading each Authorization env var's value from the adapter's
// environment. It NEVER errors: any failure (no namespace, missing env value,
// CreateSecret rejected) degrades to a clear warning naming the Secret, namespace,
// and keys the operator must supply, and returns provisioned=false. When the pack
// needs no credentials it is a no-op success.
func provisionToolCredentials(
	ctx context.Context, client omniaClient, pack *prompt.Pack, cfg *Config,
) (provisioned bool, warnings []string) {
	secretName, envVars := collectToolCredentials(pack, cfg)
	if len(envVars) == 0 {
		return true, nil // no Authorization credential to provision
	}

	data, missing := readCredentialEnv(envVars)
	if len(missing) > 0 {
		return false, []string{missingEnvWarning(secretName, missing)}
	}

	ns, err := resolveNamespace(ctx, client, cfg.Workspace)
	if err != nil {
		return false, []string{referenceOnlyWarning(secretName, "", envVars,
			fmt.Sprintf("could not resolve the workspace namespace: %v", err))}
	}

	if err := client.CreateSecret(ctx, ns, secretName, data); err != nil {
		return false, []string{referenceOnlyWarning(secretName, ns, envVars,
			fmt.Sprintf("secret creation was rejected: %v", err))}
	}
	return true, nil
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
	return fmt.Sprintf("tool credentials not provisioned: env var(s) %v are unset in the deploy "+
		"environment — pre-create Secret %q with those keys (raw token values) in the workspace namespace",
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
