package omnia

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

const (
	authHeaderName    = "Authorization"
	credentialsSuffix = "-tool-credentials" //nolint:gosec // G101 false positive: a Secret name, not a secret
)

// parseAuthEnv splits a tool's headers_from_env ("HeaderName=ENV_VAR") into the
// env var behind Authorization (empty when absent) and the remaining headers as a
// HeaderName -> ENV_VAR map. Authorization becomes a Secret-backed bearer auth
// stanza; the other headers are emitted as static headers with their env value
// resolved at deploy time (see buildStaticHeaders).
func parseAuthEnv(headersFromEnv []string) (authEnvVar string, headerEnv map[string]string) {
	for _, h := range headersFromEnv {
		name, envVar, ok := strings.Cut(h, "=")
		if !ok {
			continue
		}
		envVar = strings.TrimSpace(envVar)
		if name == authHeaderName {
			authEnvVar = envVar
			continue
		}
		if headerEnv == nil {
			headerEnv = map[string]string{}
		}
		headerEnv[name] = envVar
	}
	return authEnvVar, headerEnv
}

// credentialSecretName is the deterministic Secret name a pack's tool
// credentials are provisioned/referenced under (one Secret per pack).
func credentialSecretName(packID string) string {
	return sanitizeName(packID + credentialsSuffix)
}

// collectToolCredentials returns the Secret name and the distinct Authorization
// env vars across the pack's tools (sorted, deduped — the keys the Secret must
// hold). Non-Authorization headers are NOT credentials; they are emitted as
// static headers (see buildStaticHeaders / headerEnvWarnings).
func collectToolCredentials(pack *prompt.Pack, cfg *Config) (secretName string, envVars []string) {
	secretName = credentialSecretName(pack.ID)
	seen := map[string]bool{}
	for _, name := range packToolNames(pack) {
		src := cfg.sourceTools[name]
		if src == nil {
			continue
		}
		if authEnv, _ := parseAuthEnv(src.HeadersFromEnv); authEnv != "" && !seen[authEnv] {
			seen[authEnv] = true
			envVars = append(envVars, authEnv)
		}
	}
	sort.Strings(envVars)
	return secretName, envVars
}

// buildStaticHeaders resolves a tool's non-Authorization headers_from_env to
// concrete header values, reading each env var at deploy time. It returns the
// populated header map and the names of headers whose env var is unset (so the
// header can't be sent). A resolved value is baked into the ToolRegistry CRD's
// httpConfig.headers — appropriate for a non-secret context header (e.g. an
// act-as-user id); a genuinely sensitive value should instead go through the auth
// stanza or be overridden by the operator.
func buildStaticHeaders(src *httpToolSource) (headers map[string]string, unsetHeaders []string) {
	if src == nil {
		return nil, nil
	}
	_, headerEnv := parseAuthEnv(src.HeadersFromEnv)
	for hdr, env := range headerEnv {
		if v := os.Getenv(env); v != "" {
			if headers == nil {
				headers = map[string]string{}
			}
			headers[hdr] = v
		} else {
			unsetHeaders = append(unsetHeaders, fmt.Sprintf("%s (env %s)", hdr, env))
		}
	}
	sort.Strings(unsetHeaders)
	return headers, unsetHeaders
}

// headerEnvWarnings returns advisories for pack tools whose non-Authorization
// headers_from_env reference env vars that are unset at deploy time (the header
// will be omitted from the request).
func headerEnvWarnings(pack *prompt.Pack, cfg *Config) []string {
	var warnings []string
	for _, name := range packToolNames(pack) {
		if _, unset := buildStaticHeaders(cfg.sourceTools[name]); len(unset) > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"tool %s: header(s) %s are sourced from unset env var(s) — they will be omitted; "+
					"set the env var(s) at deploy time", name, strings.Join(unset, ", ")))
		}
	}
	return warnings
}
