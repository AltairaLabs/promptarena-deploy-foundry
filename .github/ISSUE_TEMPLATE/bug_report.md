---
name: Bug Report
about: Report a bug in the Azure AI Foundry deploy adapter
title: "[BUG] "
labels: bug, needs-triage
assignees: ""
---

## Bug Description

A clear and concise description of the bug.

## Steps to Reproduce

1. Configure the adapter with ...
2. Run `promptarena deploy ...`
3. Observe ...

## Expected Behavior

A clear and concise description of what you expected to happen.

## Actual Behavior

A clear and concise description of what actually happened.

## Environment

- **Adapter version**: (e.g., v0.1.0)
- **Runtime image tag**: (e.g., ghcr.io/altairalabs/promptkit-foundry-runtime:v0.1.0)
- **OS**: (e.g., macOS 15.3, Ubuntu 24.04)
- **Go version**: (e.g., go1.26.0)
- **Foundry account / project region**: (e.g., eastus2 — do not paste the account name if it is sensitive)

## Configuration

Relevant deploy config (redact any secrets):

```yaml
deploy:
  provider: foundry
  config:
    account: my-foundry-account
    project: my-project
    # ...
```

## Error Output

```
Paste any error messages, logs, or stack traces here.
```

## Additional Context

Add any other context about the problem here (screenshots, related issues, etc.).

## Checklist

- [ ] I have searched existing issues to ensure this is not a duplicate.
- [ ] I have included all relevant environment details.
- [ ] I have provided a minimal configuration to reproduce the issue.
- [ ] I have redacted any secrets or sensitive information from the config and logs.
