package omnia

import (
	"errors"
	"fmt"
	"strings"
)

// Error category constants classify deployment failures for diagnostics.
const (
	ErrCategoryPermission    = "permission"
	ErrCategoryConfiguration = "configuration"
	ErrCategoryResource      = "resource"
	ErrCategoryTimeout       = "timeout"
	ErrCategoryNetwork       = "network"
	ErrCategoryNotFound      = "not_found"
	ErrCategoryConflict      = "conflict"
)

// DeployError is a structured error type that provides actionable diagnostics
// for deployment failures.
type DeployError struct {
	Category     string
	ResourceType string
	ResourceName string
	Operation    string
	Message      string
	Remediation  string
	Cause        error
}

// Error implements the error interface with a diagnostic-rich message.
func (e *DeployError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %q failed", e.Operation, e.ResourceType, e.ResourceName)
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.Cause != nil {
		fmt.Fprintf(&b, " (cause: %v)", e.Cause)
	}
	if e.Remediation != "" {
		fmt.Fprintf(&b, " [hint: %s]", e.Remediation)
	}
	return b.String()
}

// Unwrap returns the underlying cause for errors.Is/As compatibility.
func (e *DeployError) Unwrap() error {
	return e.Cause
}

// HTTP status code ranges for error classification.
const (
	httpStatusUnauthorized = 401
	httpStatusForbidden    = 403
	httpStatusNotFound     = 404
	httpStatusConflict     = 409
	httpStatusServerError  = 500
)

// classifyHTTPError maps an HTTP status code to an error category and remediation hint.
func classifyHTTPError(statusCode int) (category, remediation string) {
	switch {
	case statusCode == httpStatusUnauthorized || statusCode == httpStatusForbidden:
		return ErrCategoryPermission, "verify the API token has sufficient permissions for the workspace"
	case statusCode == httpStatusNotFound:
		return ErrCategoryNotFound, "verify the resource exists and the workspace/name are correct"
	case statusCode == httpStatusConflict:
		return ErrCategoryConflict, "resource already exists; consider updating instead of creating"
	case statusCode >= httpStatusServerError:
		return ErrCategoryNetwork, "Omnia API server error; retry after a short wait"
	default:
		return ErrCategoryResource, ""
	}
}

// classifyErrorMessage determines category and remediation from an error string.
func classifyErrorMessage(msg string) (category, remediation string) {
	lower := strings.ToLower(msg)

	if containsAny(lower, permissionKeywords) {
		return ErrCategoryPermission, hintCheckToken
	}
	if containsAny(lower, networkKeywords) {
		return ErrCategoryNetwork, hintCheckNetwork
	}
	if containsAny(lower, timeoutKeywords) {
		return ErrCategoryTimeout, hintRetryOrTimeout
	}
	if containsAny(lower, configKeywords) {
		return ErrCategoryConfiguration, hintCheckConfig
	}
	return ErrCategoryResource, ""
}

// Keyword groups for error classification.
var (
	permissionKeywords = []string{
		"unauthorized", "forbidden", "access denied", "not authorized",
	}
	networkKeywords = []string{
		"connection refused", "no such host", "dial tcp", "tls handshake",
	}
	timeoutKeywords = []string{
		"deadline exceeded", "context canceled", "timeout",
	}
	configKeywords = []string{
		"validation", "invalid", "malformed",
	}
)

// Remediation hint constants.
const (
	hintCheckToken     = "verify the api_token or OMNIA_API_TOKEN is valid and has workspace access"
	hintCheckNetwork   = "verify the api_endpoint URL is correct and reachable"
	hintRetryOrTimeout = "the operation timed out; retry after a short wait"
	hintCheckConfig    = "check the deploy config values match Omnia API requirements"
)

// containsAny returns true if s contains any of the substrings.
func containsAny(s string, substrings []string) bool {
	for _, sub := range substrings {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// newDeployError creates a DeployError with automatic error classification.
func newDeployError(operation, resType, resName string, cause error) *DeployError {
	category, remediation := classifyErrorMessage(cause.Error())
	return &DeployError{
		Category:     category,
		ResourceType: resType,
		ResourceName: resName,
		Operation:    operation,
		Message:      cause.Error(),
		Remediation:  remediation,
		Cause:        cause,
	}
}

// IsDeployError returns the DeployError if err is (or wraps) one.
func IsDeployError(err error) *DeployError {
	var de *DeployError
	if errors.As(err, &de) {
		return de
	}
	return nil
}

// combineErrors joins two errors, preferring the first non-nil.
func combineErrors(existing, additional error) error {
	if existing == nil {
		return additional
	}
	if additional == nil {
		return existing
	}
	return fmt.Errorf("%w; %v", existing, additional)
}
