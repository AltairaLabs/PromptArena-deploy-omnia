.PHONY: fmt lint test build check install-hooks

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
