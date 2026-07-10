package omnia

import (
	"sort"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

const (
	authHeaderName = "Authorization"
	// credentialsSuffix is a Secret NAME suffix, not a credential value.
	credentialsSuffix = "-tool-credentials" //nolint:gosec // G101 false positive: a Secret name, not a secret
)

// parseAuthEnv splits a tool's headers_from_env ("HeaderName=ENV_VAR") into the
// env var behind Authorization (empty when absent) and the header names of any
// OTHER secret-sourced headers, which the ToolRegistry CRD cannot back with a
// Secret (only the auth stanza's Authorization can) and which are surfaced as a
// warning rather than silently dropped.
func parseAuthEnv(headersFromEnv []string) (authEnvVar string, otherSecretHeaders []string) {
	for _, h := range headersFromEnv {
		name, envVar, ok := strings.Cut(h, "=")
		if !ok {
			continue
		}
		if name == authHeaderName {
			authEnvVar = strings.TrimSpace(envVar)
			continue
		}
		otherSecretHeaders = append(otherSecretHeaders, name)
	}
	return authEnvVar, otherSecretHeaders
}

// credentialSecretName is the deterministic Secret name a pack's tool
// credentials are provisioned/referenced under (one Secret per pack).
func credentialSecretName(packID string) string {
	return sanitizeName(packID + credentialsSuffix)
}

// collectToolCredentials returns the Secret name, the distinct Authorization env
// vars across the pack's tools (sorted, deduped — the keys the Secret must hold),
// and warnings for any non-Authorization secret headers the CRD can't back.
func collectToolCredentials(pack *prompt.Pack, cfg *Config) (secretName string, envVars, warnings []string) {
	secretName = credentialSecretName(pack.ID)
	seen := map[string]bool{}
	for _, name := range packToolNames(pack) {
		src := cfg.sourceTools[name]
		if src == nil {
			continue
		}
		authEnv, others := parseAuthEnv(src.HeadersFromEnv)
		if authEnv != "" && !seen[authEnv] {
			seen[authEnv] = true
			envVars = append(envVars, authEnv)
		}
		for _, hdr := range others {
			warnings = append(warnings, nonAuthHeaderWarning(name, hdr))
		}
	}
	sort.Strings(envVars)
	return secretName, envVars, warnings
}

// nonAuthHeaderWarning explains that a non-Authorization secret header can't be
// Secret-backed by the ToolRegistry CRD, so the adapter leaves it unprovisioned.
func nonAuthHeaderWarning(tool, header string) string {
	return "tool " + tool + ": header " + header + " is sourced from a secret env var, but the " +
		"ToolRegistry CRD can only back the Authorization header from a Secret (auth stanza) — " +
		"this header is not provisioned; set it another way or await an Omnia headersFromSecret enhancement"
}
