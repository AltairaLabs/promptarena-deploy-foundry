---
name: Feature Request
about: Suggest a new feature or improvement for the Azure AI Foundry deploy adapter
title: "[FEATURE] "
labels: enhancement, needs-triage
assignees: ""
---

## Feature Summary

A brief, one-line summary of the feature.

## Problem Statement

Describe the problem or limitation you are experiencing. Why is this feature needed?

## Proposed Solution

Describe your proposed solution in detail.

### Go Code Example

```go
// Example of how the feature might be used in code
cfg := foundry.Config{
    // ...
}
```

### Config Example

```yaml
deploy:
  provider: foundry
  config:
    # proposed new configuration options
```

## Alternative Solutions

Describe any alternative solutions or workarounds you have considered.

## Use Cases

- **Use case 1**: ...
- **Use case 2**: ...

## Implementation Considerations

- Are there any breaking changes?
- Are there Foundry hosted-agent constraints to be aware of (linux/amd64 only,
  the three fixed cpu/memory pairs, immutable versions, ACR-only images)?
- Does this affect existing deploy configurations?

## Documentation Impact

**Would closing this make something in the docs untrue?**
Check the docs for statements this issue would invalidate — a documented limitation,
a "not supported yet", or a described behaviour that changes. Docs deliberately carry
no issue links, so nothing points back here when this closes; the doc update has to be
part of the work.

- [ ] No documented statement changes
- [ ] Yes — the pages to update are listed below, and updating them is part of this issue

```
<paths under docs/ that this makes stale>
```

## Checklist

- [ ] I have searched existing issues and feature requests to ensure this is not a duplicate.
- [ ] I have considered backward compatibility.
- [ ] I have provided concrete use cases for this feature.
- [ ] I have checked whether this makes any existing documentation untrue, and listed the pages if so
