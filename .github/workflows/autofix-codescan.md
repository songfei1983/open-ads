---
on:
  workflow_dispatch:
  schedule:
    - cron: "0 2 * * *"
  push:
    branches: [main]

permissions:
  contents: read
  pull-requests: read
  security-events: read

engine: copilot

runtimes:
  go:
    version: "1.26"

tools:
  github:
    toolsets: [code_security, repos, pull_requests]
  bash:
    - "go test ./..."

safe-outputs:
  create-pull-request:
    title-prefix: "[security] "
    draft: true
    max: 1
    protected-files: fallback-to-issue
---

# Auto-fix Code Scanning Alerts

Review the repository's open CodeQL code-scanning alerts using the GitHub code-security tools. Do not look for a local SARIF file; the alerts are already uploaded by the CodeQL workflow.

For each alert, verify that it is still open and inspect its rule, severity, message, affected locations, and surrounding source code. Do not dismiss alerts or make speculative changes. Select at most one alert per run, prioritizing the highest severity and then the clearest, smallest fix.

Implement only a focused fix for that alert. Do not change dependencies, workflow files, agent instructions, or other security configuration. Run `go test ./...` and report the result. If the fix is clear and tests pass, create one draft pull request describing the alert, affected code, root cause, fix, and tests. Do not claim tests passed unless the command succeeded.

Before creating a pull request, inspect open pull requests and avoid creating a duplicate for an alert that is already being fixed. If no open alert has a clear, safe fix, or all actionable alerts are already covered by open pull requests, call the `noop` tool with a brief explanation.
