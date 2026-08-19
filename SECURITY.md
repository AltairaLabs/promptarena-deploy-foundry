# Security Policy

## Supported Versions

| Version        | Supported          |
| -------------- | ------------------ |
| main           | :white_check_mark: |
| Latest release | :white_check_mark: |
| Older releases | :x:                |

## Reporting a Vulnerability

We appreciate responsible disclosure of security vulnerabilities.

### Do Not Create Public Issues

**Please do not report security vulnerabilities through public GitHub issues.** Public disclosure before a fix is available can put users at risk.

### Report Privately

Send an email to: **[security@altairalabs.ai](mailto:security@altairalabs.ai)**

Include the following information:

- **Description** of the vulnerability
- **Steps to reproduce** the issue
- **Potential impact** and attack scenarios
- **Suggested fixes** or mitigations, if any
- Your **contact information** for follow-up

### Response Timeline

- **Initial Response**: Within 48 hours
- **Triage**: Within 5 business days
- **Resolution**: Typically within 30-90 days depending on severity

## Security Measures

### Static Analysis

- **gosec** via golangci-lint runs on all pull requests to catch common Go security issues.

### Dependency Management

- **Dependabot** monitors Go module and GitHub Actions dependencies and opens pull requests for security updates automatically.

### Supply Chain

- GitHub Actions are pinned by commit SHA in the workflows that handle release and repository write access.
- The runtime image is built from `gcr.io/distroless/static-debian12:nonroot` and runs as a non-root user.

### Code Review

- All changes require peer review before merging to `main`.

## Security Considerations for Users

### Credentials

This adapter authenticates to the Azure AI Foundry data plane with an Entra
token resolved by `DefaultAzureCredential`. Follow these practices:

- **Never commit credentials or client secrets** to version control. Prefer
  managed identity or workload identity federation over client secrets.
- Apply the **principle of least privilege** to the identity used for deploys —
  it needs data-plane write access to one project, not subscription-level rights.
- Rotate any secret-based credential regularly.

### Secrets reaching the deployed agent

Foundry supplies secrets to hosted agents through **project connections**,
referenced as `${{connections.<name>.credentials.<field>}}`. Prefer that over
placing secret material in `environment_variables`, which is visible on the
agent version.

### Container registry

Foundry pulls the runtime image from an Azure Container Registry. Restrict pull
access to the project's identity, and prefer a network-secured registry where
your project supports it.

### Input Validation

- All configuration inputs are validated before use. Ensure configuration files are sourced from trusted locations.

## Resources

- **Security Advisories**: [GitHub Security Advisories](https://github.com/AltairaLabs/promptarena-deploy-foundry/security/advisories)
- **Security Contact**: [security@altairalabs.ai](mailto:security@altairalabs.ai)
- **Parent Project**: This adapter is part of the [PromptKit](https://github.com/AltairaLabs/PromptKit) ecosystem.

---

For questions about this security policy, contact: [security@altairalabs.ai](mailto:security@altairalabs.ai)
