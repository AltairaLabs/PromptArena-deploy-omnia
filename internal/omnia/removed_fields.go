package omnia

import "fmt"

// Omnia removed several AgentRuntime and ToolRegistry fields the adapter's config
// vocabulary still exposes (omnia#1775). This file is the single place that knows
// which ones, what replaced them, and how the adapter now behaves.
//
// The important property: these fields are NOT deliverable by any route. The
// per-resource path proxies raw JSON to the apiserver, which prunes unknown
// fields under a structural schema and returns 201 — so a deploy carrying them
// reports success while silently discarding them. Diverting such a config to
// that path (which the adapter used to do) protects nothing and misleads the
// operator, so instead the adapter states plainly what it dropped and why.

// removedFieldWarnings reports every deploy-config setting that names a field
// Omnia no longer has. Each names the field, the fact it cannot be applied, and
// the current way to achieve the same thing.
func removedFieldWarnings(cfg *Config) []string {
	var warnings []string
	warnings = append(warnings, removedAuthWarnings(cfg.ExternalAuth)...)
	warnings = append(warnings, removedToolWarnings(cfg.Tools)...)
	return warnings
}

// removedAuthWarnings covers the externalAuth block's removed fields.
func removedAuthWarnings(ea *ExternalAuthConfig) []string {
	if ea == nil {
		return nil
	}
	var warnings []string

	if ea.SharedToken != nil {
		warnings = append(warnings, sharedTokenWarning(ea))
	}
	if ea.OIDC != nil && ea.OIDC.ClaimMapping != nil && ea.OIDC.ClaimMapping.Role != "" {
		warnings = append(warnings, fmt.Sprintf(
			"externalAuth.oidc.claimMapping.role (%q) is ignored — Omnia removed role from the "+
				"OIDC claim mapping. A role claim from your IdP now passes through unmapped under "+
				"its own name and is readable as identity.claims.<name>",
			ea.OIDC.ClaimMapping.Role))
	}
	if ea.EdgeTrust != nil && ea.EdgeTrust.HeaderMapping != nil && ea.EdgeTrust.HeaderMapping.Role != "" {
		warnings = append(warnings, fmt.Sprintf(
			"externalAuth.edgeTrust.headerMapping.role (%q) is ignored — Omnia removed role from "+
				"the edge-trust header mapping. The role header name is now a fixed internal "+
				"default and is read into identity.claims.role",
			ea.EdgeTrust.HeaderMapping.Role))
	}
	return warnings
}

// sharedTokenWarning explains how the removed sharedToken block is handled. The
// adapter migrates what it can onto clientKeys, but secretRef has no equivalent:
// client keys are created in the dashboard and stored hashed, not declared in
// the resource, so the Secret is simply no longer consumed.
func sharedTokenWarning(ea *ExternalAuthConfig) string {
	if ea.APIKeys != nil {
		return fmt.Sprintf(
			"externalAuth.sharedToken (secretRef %q) is ignored — Omnia removed it, and this "+
				"config also sets externalAuth.apiKeys, which takes precedence. Create the "+
				"caller's key in the dashboard; the Secret is no longer read",
			ea.SharedToken.SecretRef)
	}
	return fmt.Sprintf(
		"externalAuth.sharedToken (secretRef %q) was removed by Omnia and has been migrated to "+
			"client keys. trustEndUserHeader carried over; the SECRET IS NO LONGER READ — create "+
			"a client key for the caller in the dashboard, or the agent will reject it",
		ea.SharedToken.SecretRef)
}

// removedToolWarnings covers ToolRegistry handler fields Omnia no longer has.
func removedToolWarnings(tools []ToolHandler) []string {
	var warnings []string
	for i := range tools {
		if len(tools[i].Selector) == 0 {
			continue
		}
		warnings = append(warnings, fmt.Sprintf(
			"tools[%q].selector is ignored — Omnia removed Service-discovery selectors from tool "+
				"handlers. Configure the handler's endpoint inline (httpConfig/grpcConfig/etc.) "+
				"instead", tools[i].Name))
	}
	return warnings
}
