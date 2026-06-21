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

// HTTPError carries the status-code-driven classification of a failed Omnia
// API response. The HTTP client computes Category/Remediation from the status
// code (via classifyHTTPError) and returns this typed error so the
// classification survives to newDeployError, instead of being re-guessed by
// string-matching the message.
type HTTPError struct {
	StatusCode  int
	Body        string
	Category    string
	Remediation string
}

// Error implements the error interface. It mirrors the previous string form
// (HTTP <code>: <body>) and appends the remediation hint when one is set.
func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
	if e.Remediation != "" {
		msg += " [hint: " + e.Remediation + "]"
	}
	return msg
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
// When the cause carries a typed *HTTPError, the status-code-driven category
// and remediation are used directly (robust). Otherwise — notably for
// transport errors that never produced an HTTP response (connection refused,
// timeout, no such host) — it falls back to classifying the error message.
func newDeployError(operation, resType, resName string, cause error) *DeployError {
	category, remediation := classifyCause(cause)
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

// classifyCause prefers the typed HTTPError classification (status-code-driven)
// and falls back to message-string classification for transport errors that
// have no HTTP response.
func classifyCause(cause error) (category, remediation string) {
	var he *HTTPError
	if errors.As(cause, &he) {
		return he.Category, he.Remediation
	}
	return classifyErrorMessage(cause.Error())
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
