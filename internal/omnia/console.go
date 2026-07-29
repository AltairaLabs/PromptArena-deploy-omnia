package omnia

import (
	"fmt"
	"net/url"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
)

// agentConsoleURL builds the dashboard URL for a deployed agent, or "" when the
// console base is unknown.
//
// The workspace is always included: an operator with access to more than one
// would otherwise land on a page that resolves against whichever workspace the
// dashboard last had selected. Both the agent name and the workspace are query-
// escaped, since neither is guaranteed URL-safe just because it is a valid
// Kubernetes name.
//
// Returning "" rather than a partial URL is deliberate — see consoleLinks.
func agentConsoleURL(cfg *Config, agentName string) string {
	if cfg == nil {
		return ""
	}
	root := cfg.consoleRoot()
	if root == "" || cfg.Workspace == "" {
		return ""
	}
	return fmt.Sprintf("%s/agents/%s?workspace=%s",
		root, url.PathEscape(sanitizeName(agentName)), url.QueryEscape(cfg.Workspace))
}

// consoleLinks returns the operator-facing links for a deployed agent, or nil
// when the console URL cannot be determined.
//
// nil is the correct answer for "we don't know". A link that 404s or lands on
// the wrong workspace is worse than no link, and the protocol treats an absent
// Links field as simply "no links" — clients render nothing and must not
// synthesise a URL of their own.
func consoleLinks(cfg *Config, agentName string) []deploy.ResourceLink {
	return adaptersdk.ConsoleLink(agentConsoleURL(cfg, agentName))
}
