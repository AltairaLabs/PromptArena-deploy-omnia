.PHONY: fmt lint test build check install-hooks sync-crds

# Omnia repo + pinned version the vendored CRD contract schemas track.
CRD_REPO ?= AltairaLabs/Omnia
CRD_DIR  := internal/omnia/testdata/crds
CRD_VERSION := $(shell cat $(CRD_DIR)/VERSION)
CRD_FILES := agentruntimes promptpacks toolregistries agentpolicies

# Format code with goimports
fmt:
	GOWORK=off goimports -w -local github.com/AltairaLabs/promptarena-deploy-omnia .

# Run golangci-lint
lint:
	GOWORK=off golangci-lint run ./...

# Run tests with race detector
test:
	GOWORK=off go test ./... -race -count=1

# Build binary
build:
	GOWORK=off go build -o promptarena-deploy-omnia .

# Run all quality checks
check: fmt lint test build

# Install git hooks
install-hooks:
	git config core.hooksPath .githooks

# Re-vendor Omnia's CRD schemas at the pinned $(CRD_VERSION) for the CRD
# contract tests (internal/omnia/crd_contract_test.go). Bump the version by
# editing $(CRD_DIR)/VERSION, then run `make sync-crds` and fix any newly
# red contract tests — that is the CRD-drift signal.
sync-crds:
	@echo "Syncing Omnia CRDs @ $(CRD_VERSION) from $(CRD_REPO)"
	@for c in $(CRD_FILES); do \
		curl -fsSL "https://raw.githubusercontent.com/$(CRD_REPO)/$(CRD_VERSION)/config/crd/bases/omnia.altairalabs.ai_$$c.yaml" \
			-o "$(CRD_DIR)/$$c.yaml" && echo "  ok $$c" || { echo "  FAIL $$c"; exit 1; }; \
	done
