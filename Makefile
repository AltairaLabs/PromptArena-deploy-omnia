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
#
# $(CRD_REPO) is a PRIVATE repository, so this fetch must be authenticated —
# an anonymous raw.githubusercontent.com request returns 404. We go through
# `gh api` rather than passing a token to curl so the credential is never
# visible in the process list, and so it picks up whatever auth the developer
# already has. Requires `gh auth login` with access to $(CRD_REPO).
#
# Each file is written to a temp path first: `gh api` writes its error body to
# stdout on failure, and a bare redirect would happily overwrite a good
# vendored schema with an error payload.
sync-crds:
	@command -v gh >/dev/null 2>&1 || { \
		echo "gh CLI not found. $(CRD_REPO) is private, so the CRD fetch must be"; \
		echo "authenticated. Install the GitHub CLI and run 'gh auth login'."; \
		exit 1; \
	}
	@echo "Syncing Omnia CRDs @ $(CRD_VERSION) from $(CRD_REPO)"
	@for c in $(CRD_FILES); do \
		if gh api "repos/$(CRD_REPO)/contents/config/crd/bases/omnia.altairalabs.ai_$$c.yaml?ref=$(CRD_VERSION)" \
				-H "Accept: application/vnd.github.raw" > "$(CRD_DIR)/$$c.yaml.tmp" 2>/dev/null; then \
			mv "$(CRD_DIR)/$$c.yaml.tmp" "$(CRD_DIR)/$$c.yaml"; \
			echo "  ok $$c"; \
		else \
			rm -f "$(CRD_DIR)/$$c.yaml.tmp"; \
			echo "  FAIL $$c"; \
			exit 1; \
		fi; \
	done
