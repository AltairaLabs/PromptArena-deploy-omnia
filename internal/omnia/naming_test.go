package omnia

import (
	"regexp"
	"strings"
	"testing"
)

// k8sNameRe validates a Kubernetes DNS subdomain name:
// lowercase alphanumeric, hyphens, dots; must start and end with alphanumeric.
var k8sNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

// isValidK8sName returns true if name is a valid K8s DNS subdomain name.
func isValidK8sName(name string) bool {
	return name != "" && len(name) <= k8sNameMaxLen && k8sNameRe.MatchString(name)
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "underscore to hyphen", input: "my_pack", want: "my-pack"},
		{name: "spaces and uppercase", input: "My Pack", want: "my-pack"},
		{name: "collapse double hyphens", input: "a--b", want: "a-b"},
		{name: "trim leading/trailing hyphens", input: "-leading-", want: "leading"},
		{name: "long string truncated", input: strings.Repeat("a", 300), want: strings.Repeat("a", 253)},
		{name: "already valid", input: "valid-name", want: "valid-name"},
		{name: "mixed invalid chars", input: "hello@world!", want: "helloworld"},
		{name: "dots preserved", input: "v1.2.3", want: "v1.2.3"},
		{name: "leading dot trimmed", input: ".dotfile", want: "dotfile"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsValidK8sName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "simple valid", input: "my-resource", want: true},
		{name: "single char", input: "a", want: true},
		{name: "with dots", input: "my.resource.v1", want: true},
		{name: "empty string", input: "", want: false},
		{name: "too long", input: strings.Repeat("a", 254), want: false},
		{name: "uppercase", input: "MyResource", want: false},
		{name: "special chars", input: "my_resource!", want: false},
		{name: "leading hyphen", input: "-invalid", want: false},
		{name: "trailing hyphen", input: "invalid-", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidK8sName(tt.input)
			if got != tt.want {
				t.Errorf("isValidK8sName(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
