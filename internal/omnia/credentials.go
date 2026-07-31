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
	// keySecretKeyField is the field naming the key WITHIN a Secret, in both a
	// secretRef and a headersFromSecret entry.
	keySecretKeyField = "key"
)

// parseAuthEnv splits a tool's headers_from_env ("HeaderName=ENV_VAR") into the
// env var behind Authorization (empty when absent) and the remaining headers as a
// HeaderName -> ENV_VAR map. Authorization becomes a Secret-backed bearer auth
// stanza; the other headers become Secret-backed headersFromSecret entries (see
// buildSecretHeaders).
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
//
// It is authoritative only on the per-resource path, which writes the Secret
// itself. On the deploy-intent path the SERVER names and owns the Secret and
// rewrites every handler reference whose key the intent carried (Omnia#2008) —
// the name emitted here is a placeholder the CRD requires (both name and key
// are required on a secretRef) and is replaced before the registry is
// persisted. Nothing downstream should treat it as the real name.
func credentialSecretName(packID string) string {
	return sanitizeName(packID + credentialsSuffix)
}

// collectToolCredentials returns the Secret name and every distinct env var
// behind a header across the pack's tools (sorted, deduped — the keys the Secret
// must hold).
//
// This covers BOTH the Authorization env var, consumed by the handler auth
// stanza, and every other headers_from_env var, now consumed by
// headersFromSecret. All of them are treated as credentials: the pack gives no
// way to tell a secret header from a context one, so the safe reading is that
// anything sourced from an env var may be sensitive.
func collectToolCredentials(pack *prompt.Pack, cfg *Config) (secretName string, envVars []string) {
	secretName = credentialSecretName(pack.ID)
	seen := map[string]bool{}
	add := func(env string) {
		if env != "" && !seen[env] {
			seen[env] = true
			envVars = append(envVars, env)
		}
	}
	for _, name := range packToolNames(pack) {
		src := cfg.sourceTools[name]
		if src == nil {
			continue
		}
		authEnv, headerEnv := parseAuthEnv(src.HeadersFromEnv)
		add(authEnv)
		for _, env := range headerEnv {
			add(env)
		}
	}
	sort.Strings(envVars)
	return secretName, envVars
}

// buildSecretHeaders maps a tool's non-Authorization headers_from_env onto
// httpConfig.headersFromSecret entries, each pointing at the per-pack
// credentials Secret under the env var's own name as the key. It returns the
// map and the names of headers whose env var is unset at deploy time (so no
// Secret key can be provisioned and the header will not resolve).
//
// The VALUE never appears here. Previously these were emitted as static
// httpConfig.headers with the env var read at deploy time, which wrote the
// value into the ToolRegistry in plaintext — readable by anyone who can
// get toolregistry. Every env-sourced header is now treated as secret rather
// than the adapter guessing which ones are: nothing in headers_from_env
// distinguishes an act-as-user id from a signing token.
func buildSecretHeaders(
	src *httpToolSource, secretName string,
) (headers map[string]map[string]string, unsetHeaders []string) {
	if src == nil {
		return nil, nil
	}
	_, headerEnv := parseAuthEnv(src.HeadersFromEnv)
	for hdr, env := range headerEnv {
		if headers == nil {
			headers = map[string]map[string]string{}
		}
		headers[hdr] = map[string]string{keyName: secretName, keySecretKeyField: env}
		if os.Getenv(env) == "" {
			unsetHeaders = append(unsetHeaders, fmt.Sprintf("%s (env %s)", hdr, env))
		}
	}
	sort.Strings(unsetHeaders)
	return headers, unsetHeaders
}

// headerEnvWarnings returns advisories for pack tools whose non-Authorization
// headers_from_env reference env vars that are unset at deploy time.
//
// The outcome is the same on both deploy paths, by two different mechanisms: on
// the deploy-intent path no Secret key can be provisioned so the reference
// dangles, and on the per-resource path there is no value to bake into the
// static header. Either way the header is not sent, so the advisory is
// path-agnostic and needs no gating.
func headerEnvWarnings(pack *prompt.Pack, cfg *Config) []string {
	secretName := credentialSecretName(pack.ID)
	var warnings []string
	for _, name := range packToolNames(pack) {
		if _, unset := buildSecretHeaders(cfg.sourceTools[name], secretName); len(unset) > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"tool %s: header(s) %s are sourced from unset env var(s) — the header(s) will "+
					"not be sent; set the env var(s) at deploy time",
				name, strings.Join(unset, ", ")))
		}
	}
	return warnings
}

// addHeaderConfig writes a tool's non-Authorization headers_from_env onto an
// httpConfig in whichever representation the target server understands.
//
// httpConfig.headersFromSecret arrived with Omnia#1831, well AFTER the
// deploy-intent API, so the two paths necessarily differ:
//
//   - secretHeaders=true (deploy-intent path) — the server has the field, so the
//     header references a Secret key and the value never enters the CRD.
//   - secretHeaders=false (per-resource path) — that path runs ONLY against
//     servers with no deploy-intent API, which predate #1831 and PRUNE
//     headersFromSecret. There the header would vanish entirely, which is worse
//     than the plaintext static header it replaced, so those keep static headers.
//
// Same reasoning as the externalAuth vocabulary split in builders.go, and the
// same fate: the static branch goes away with the per-resource path.
func addHeaderConfig(
	cfg map[string]interface{}, src *httpToolSource, secretName string, secretHeaders bool,
) {
	if secretHeaders {
		if headers, _ := buildSecretHeaders(src, secretName); len(headers) > 0 {
			cfg[keyHeadersFromSecret] = headers
		}
		return
	}
	if headers, _ := buildStaticHeaders(src); len(headers) > 0 {
		cfg[keyHeaders] = headers
	}
}

// buildStaticHeaders resolves a tool's non-Authorization headers_from_env to
// concrete header values, reading each env var at deploy time.
//
// This bakes the value into the ToolRegistry in plaintext, readable by anyone
// who can get toolregistry — which is the leak Omnia#1831 exists to close. It
// survives only for servers that lack headersFromSecret, where the alternative
// is not sending the header at all. See addHeaderConfig.
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

// No collision guard lives here on purpose. Omnia#1996 notes that
// headersFromSecret rejects only a literal Authorization key and that a secret
// header sharing the handler's auth header name silently overwrites the
// credential. The adapter cannot emit that pair: parseAuthEnv sends Authorization
// to the auth stanza and everything else here, so the two sets are disjoint by
// construction. TestSecretHeadersNeverCollideWithAuth pins that invariant so it
// fails loudly if the auth stanza ever gains a custom header name.
