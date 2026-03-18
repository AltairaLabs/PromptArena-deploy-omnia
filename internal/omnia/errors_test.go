package omnia

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestDeployError_Error(t *testing.T) {
	de := &DeployError{
		Category:     ErrCategoryPermission,
		ResourceType: "configmap",
		ResourceName: "my-config",
		Operation:    "create",
		Message:      "forbidden",
		Remediation:  "check your token",
		Cause:        fmt.Errorf("HTTP 403"),
	}

	got := de.Error()
	if !strings.Contains(got, "create configmap") {
		t.Errorf("expected operation and resource type in error, got %q", got)
	}
	if !strings.Contains(got, `"my-config"`) {
		t.Errorf("expected resource name in error, got %q", got)
	}
	if !strings.Contains(got, "forbidden") {
		t.Errorf("expected message in error, got %q", got)
	}
	if !strings.Contains(got, "HTTP 403") {
		t.Errorf("expected cause in error, got %q", got)
	}
	if !strings.Contains(got, "check your token") {
		t.Errorf("expected remediation hint in error, got %q", got)
	}
}

func TestDeployError_Unwrap(t *testing.T) {
	cause := fmt.Errorf("root cause")
	de := &DeployError{
		Category:     ErrCategoryResource,
		ResourceType: "configmap",
		ResourceName: "test",
		Operation:    "get",
		Cause:        cause,
	}

	if !errors.Is(de, cause) {
		t.Error("Unwrap should return the underlying cause")
	}
}

func TestClassifyHTTPError(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		wantCategory string
	}{
		{name: "401 unauthorized", statusCode: 401, wantCategory: ErrCategoryPermission},
		{name: "403 forbidden", statusCode: 403, wantCategory: ErrCategoryPermission},
		{name: "404 not found", statusCode: 404, wantCategory: ErrCategoryNotFound},
		{name: "409 conflict", statusCode: 409, wantCategory: ErrCategoryConflict},
		{name: "500 server error", statusCode: 500, wantCategory: ErrCategoryNetwork},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			category, _ := classifyHTTPError(tt.statusCode)
			if category != tt.wantCategory {
				t.Errorf("classifyHTTPError(%d) category = %q, want %q",
					tt.statusCode, category, tt.wantCategory)
			}
		})
	}
}

func TestClassifyErrorMessage(t *testing.T) {
	tests := []struct {
		name         string
		msg          string
		wantCategory string
	}{
		{name: "permission keyword", msg: "access denied to resource", wantCategory: ErrCategoryPermission},
		{name: "network keyword", msg: "connection refused", wantCategory: ErrCategoryNetwork},
		{name: "timeout keyword", msg: "context deadline exceeded", wantCategory: ErrCategoryTimeout},
		{name: "config keyword", msg: "validation failed for field X", wantCategory: ErrCategoryConfiguration},
		{name: "unknown falls through", msg: "something unexpected", wantCategory: ErrCategoryResource},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			category, _ := classifyErrorMessage(tt.msg)
			if category != tt.wantCategory {
				t.Errorf("classifyErrorMessage(%q) category = %q, want %q",
					tt.msg, category, tt.wantCategory)
			}
		})
	}
}

func TestNewDeployError(t *testing.T) {
	cause := fmt.Errorf("unauthorized access")
	de := newDeployError("create", "configmap", "my-config", cause)

	if de.Category != ErrCategoryPermission {
		t.Errorf("Category = %q, want %q", de.Category, ErrCategoryPermission)
	}
	if de.Operation != "create" {
		t.Errorf("Operation = %q, want %q", de.Operation, "create")
	}
	if de.ResourceType != "configmap" {
		t.Errorf("ResourceType = %q, want %q", de.ResourceType, "configmap")
	}
	if de.ResourceName != "my-config" {
		t.Errorf("ResourceName = %q, want %q", de.ResourceName, "my-config")
	}
	if de.Cause != cause {
		t.Errorf("Cause = %v, want %v", de.Cause, cause)
	}
	if de.Remediation == "" {
		t.Error("expected non-empty Remediation for permission error")
	}
}

func TestCombineErrors(t *testing.T) {
	err1 := fmt.Errorf("first")
	err2 := fmt.Errorf("second")

	t.Run("nil+nil", func(t *testing.T) {
		if got := combineErrors(nil, nil); got != nil {
			t.Errorf("combineErrors(nil, nil) = %v, want nil", got)
		}
	})

	t.Run("err+nil", func(t *testing.T) {
		got := combineErrors(err1, nil)
		if got != err1 {
			t.Errorf("combineErrors(err, nil) = %v, want %v", got, err1)
		}
	})

	t.Run("nil+err", func(t *testing.T) {
		got := combineErrors(nil, err2)
		if got != err2 {
			t.Errorf("combineErrors(nil, err) = %v, want %v", got, err2)
		}
	})

	t.Run("err+err", func(t *testing.T) {
		got := combineErrors(err1, err2)
		if got == nil {
			t.Fatal("combineErrors(err, err) = nil, want non-nil")
		}
		msg := got.Error()
		if !strings.Contains(msg, "first") || !strings.Contains(msg, "second") {
			t.Errorf("combined error = %q, want both messages", msg)
		}
	})
}
