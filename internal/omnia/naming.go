package omnia

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// packObjectHashLen is the number of hex characters of the pack digest kept in a
// PromptPack object name. It mirrors omnia's v1alpha1.PromptPackObjectName.
const packObjectHashLen = 12

// promptPackObjectName reproduces omnia's v1alpha1.PromptPackObjectName: the
// immutable, per-version PromptPack object name the deploy-intent server derives
// from the pack name and version. The adapter computes it locally ONLY to
// preview the object in a plan — apply always takes the name from the server's
// response, so a future change to the server's scheme cannot corrupt state.
func promptPackObjectName(packName, version string) string {
	sum := sha256.Sum256([]byte(packName + "@" + version))
	return "pp-" + hex.EncodeToString(sum[:])[:packObjectHashLen]
}

// toolRegistryObjectName is the ToolRegistry name the deploy-intent server
// derives for a create-mode deploy ("<pack>-tools"). The adapter compares this
// against its own resolver's decision before taking the intent path.
func toolRegistryObjectName(packName string) string {
	return fmt.Sprintf("%s-tools", packName)
}
