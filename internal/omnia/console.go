package omnia

import (
	"fmt"
	"net/url"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
)

// Console links come from two places, and the split is deliberate.
//
// On the deploy-intent path Omnia RETURNS the URL per resource (Omnia#1978) and
// the adapter passes it through untouched. That is the correct arrangement: the
// dashboard owns its routes and knows its own public address, so it can move
// from /agents/{name} to the /console deep link — as it already has — without
// any adapter change. Building the URL here would hardcode an Omnia route into
// this repo and make changing it a coordinated release across two projects.
//
// The per-resource path gets no URL, because the servers it targets predate the
// field. There the adapter still constructs one, which is safe for exactly those
// servers: /agents/{name} is the route they have. That construction is confined
// to buildLegacyConsoleURL and disappears with the per-resource path itself.

// consoleLinksFromURL wraps a server-provided console URL as resource links.
// Returns nil for an empty URL, which is Omnia's way of saying "not known" — a
// link that 404s or lands in the wrong workspace is worse than no link, and the
// protocol treats an absent Links field as simply no links.
func consoleLinksFromURL(consoleURL string) []deploy.ResourceLink {
	return adaptersdk.ConsoleLink(consoleURL)
}

// buildLegacyConsoleURL builds the agent page URL for servers that do not return
// one — i.e. those with no deploy-intent API. It is the pre-existing behavior
// of the post-deploy access message, kept so upgrading the adapter does not take
// that link away from anyone still on a released Omnia.
//
// Returns "" when the base or the workspace is unknown. The workspace is always
// included: without it the page resolves against whichever workspace the
// dashboard last had selected, which is a wrong link rather than an absent one.
func buildLegacyConsoleURL(cfg *Config, agentName string) string {
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

// legacyConsoleLinks is buildLegacyConsoleURL as resource links, or nil.
func legacyConsoleLinks(cfg *Config, agentName string) []deploy.ResourceLink {
	return adaptersdk.ConsoleLink(buildLegacyConsoleURL(cfg, agentName))
}
