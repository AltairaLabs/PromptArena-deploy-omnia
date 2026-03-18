package omnia

import (
	"strings"
)

// k8sNameMaxLen is the maximum length for a Kubernetes DNS subdomain name.
const k8sNameMaxLen = 253

// sanitizeName converts an arbitrary string into a valid K8s DNS subdomain name.
// It lowercases, replaces underscores and spaces with hyphens, strips invalid
// characters, collapses repeated hyphens, trims leading/trailing hyphens,
// and truncates to 253 characters.
func sanitizeName(name string) string {
	s := strings.ToLower(name)
	s = strings.NewReplacer("_", "-", " ", "-").Replace(s)

	// Strip characters that aren't lowercase alphanumeric, hyphens, or dots.
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' {
			b.WriteRune(c)
		}
	}
	s = b.String()

	// Collapse repeated hyphens.
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}

	// Trim leading/trailing hyphens and dots.
	s = strings.Trim(s, "-.")

	if len(s) > k8sNameMaxLen {
		s = s[:k8sNameMaxLen]
		s = strings.TrimRight(s, "-.")
	}

	return s
}
