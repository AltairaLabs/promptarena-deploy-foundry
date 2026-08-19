# Contributing to promptarena-deploy-foundry

Thank you for your interest in contributing to the Azure AI Foundry deploy adapter for PromptKit. This document provides guidelines for contributing to this project.

## Code of Conduct

This project adheres to the [Contributor Covenant Code of Conduct](./CODE_OF_CONDUCT.md). By participating, you are expected to uphold this code. Please report unacceptable behavior to [conduct@altairalabs.ai](mailto:conduct@altairalabs.ai).

## Developer Certificate of Origin (DCO)

This project uses the Developer Certificate of Origin (DCO) to ensure that contributors have the right to submit their contributions. By making a contribution, you certify that:

1. The contribution was created in whole or in part by you and you have the right to submit it under the open source license indicated in the file; or
2. The contribution is based upon previous work that, to the best of your knowledge, is covered under an appropriate open source license and you have the right under that license to submit that work with modifications; or
3. The contribution was provided directly to you by some other person who certified (1), (2) or (3) and you have not modified it.

### Signing Your Commits

Add the `-s` flag to your git commit command:

```bash
git commit -s -m "Your commit message"
```

This adds a "Signed-off-by" line to your commit message:

```
Signed-off-by: Your Name <your.email@example.com>
```

> **This is enforced.** A required `DCO` check verifies every commit in your pull request carries a matching `Signed-off-by` line. If you forget, fix it with `git commit --amend -s` (single commit) or `git rebase --signoff main` (multiple commits), then force-push.

## Contributor License Agreement (CLA)

Before your first contribution can be merged you must sign our Contributor License Agreement. When you open your first pull request, the **CLA Assistant** bot comments with a link to the CLA; you sign by replying on the PR with:

> I have read the CLA Document and I hereby sign the CLA

You sign **once** — the signature then applies to your future contributions across AltairaLabs repositories. The CLA is a **license grant**, not a copyright assignment: you keep ownership of your contribution and grant AltairaLabs a license to use and relicense it. You can read the full text [here](https://gist.github.com/chaholl/acc8f1f6c38376d00a162351f566b93e).

## How to Contribute

### Reporting Bugs

- Check existing issues first
- Include the adapter version and the runtime image tag
- Provide clear reproduction steps
- Share relevant configuration (redact account names, tokens, registry credentials, and any Azure resource identifiers you consider sensitive)

### Suggesting Features

- Open an issue describing the feature
- Explain the use case and how it relates to Foundry hosted agents or PromptKit deploy workflows

### Submitting Changes

1. **Fork the repository**
2. **Create a feature branch**: `git checkout -b feature/your-feature-name`
3. **Make your changes**
4. **Write or update tests**
5. **Run tests**: `go test ./... -v -race -count=1`
6. **Run linter**: `golangci-lint run`
7. **Commit with sign-off**: `git commit -s -m "Your commit message"`
8. **Push to your fork**: `git push origin feature/your-feature-name`
9. **Open a Pull Request**

## Development Setup

### Prerequisites

- Go 1.26 or later
- [golangci-lint](https://golangci-lint.run/usage/install/) and [goimports](https://pkg.go.dev/golang.org/x/tools/cmd/goimports)
- Docker, for building the runtime image

### Setup

```bash
git clone https://github.com/YOUR_USERNAME/promptarena-deploy-foundry.git
cd promptarena-deploy-foundry

make install-hooks
make check
```

### Project Structure

```
promptarena-deploy-foundry/
├── main.go                      # Entrypoint — thin wrapper calling adaptersdk.Serve()
├── internal/foundry/            # Deploy adapter domain logic
│   ├── provider.go              # Provider and the deploy.Provider methods
│   ├── schema.go                # JSON Schema for the provider config
│   ├── errors.go                # Sentinel errors, matched with errors.Is
│   └── version.go               # Build-time version variables
├── cmd/foundry-runtime/         # Container entrypoint (Foundry protocol contracts)
├── Dockerfile                   # linux/amd64 runtime image
├── test/integration/            # Build-tagged tests against real Azure
├── Makefile                     # fmt, lint, test, build, check, docker-build
├── .githooks/pre-commit         # Pre-commit hook
├── .golangci.yml                # Linter configuration
├── .github/workflows/           # CI, release, runtime-image, dependency workflows
└── LICENSE                      # MIT license
```

The adapter implements PromptKit's `deploy.Provider` interface via `adaptersdk.Serve()`.

## Coding Guidelines

### Go Code Style

- Follow standard Go conventions
- Use `gofmt` / `goimports` for formatting
- Write clear, descriptive variable and function names
- Keep functions focused, below cognitive complexity 15, and testable
- Match sentinel errors with `errors.Is`, never on message text

### Testing

- Write unit tests for new functionality
- Use table-driven tests where appropriate
- Fake the Foundry control plane rather than calling Azure — the deployed tests in `test/integration/` are build-tagged and create billable resources
- Run the full suite before submitting: `go test ./... -v -race -count=1`

### Linting

- Run `golangci-lint run` before submitting
- Fix all warnings — CI enforces a clean lint pass

## Pull Request Process

1. **Ensure CI passes** - Tests, lint, and the linux/amd64 image build must be green
2. **Include tests** - New behavior needs corresponding tests
3. **Sign commits** - Use `git commit -s` for DCO compliance
4. **Keep branch updated** - Rebase on latest `main` before merging
5. **Address review feedback** - Respond to and resolve all review comments

## Release Process

Releases are handled by maintainers:

1. Tag the commit with a `v*` semver tag (e.g. `v0.2.0`)
2. GoReleaser builds the adapter binaries and drafts the release
3. Publishing the release triggers the runtime image build to `ghcr.io/altairalabs/promptkit-foundry-runtime`

## Questions?

- Open a GitHub issue
- Check existing issues and closed PRs

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
