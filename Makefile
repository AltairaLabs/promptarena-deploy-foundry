.PHONY: fmt lint test test-integration build build-adapter build-runtime docker-build check install-hooks

fmt:
	GOWORK=off goimports -w -local github.com/AltairaLabs/promptarena-deploy-foundry .

lint:
	GOWORK=off golangci-lint run ./...

test:
	GOWORK=off go test ./... -race -count=1

# Deployed integration tests. These create billable Azure resources and are
# skipped unless FOUNDRY_TEST_ACCOUNT, FOUNDRY_TEST_PROJECT and
# FOUNDRY_TEST_IMAGE are set. See test/integration/README.md.
test-integration:
	GOWORK=off go test -tags=integration ./test/integration/ -v -count=1 -timeout=30m

build: build-adapter build-runtime

build-adapter:
	GOWORK=off go build -o promptarena-deploy-foundry .

build-runtime:
	GOWORK=off go build -o foundry-runtime ./cmd/foundry-runtime/

# The Dockerfile pins linux/amd64 itself — Foundry rejects anything else —
# so this is correct on an arm64 workstation too.
docker-build:
	docker build -t promptkit-foundry-runtime:local .

check: fmt lint test build

install-hooks:
	git config core.hooksPath .githooks
