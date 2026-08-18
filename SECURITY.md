# Security Policy

## Reporting a vulnerability

Email **ebrahimhanna580@gmail.com** with a description of the issue, the
affected component, and steps to reproduce. Please do not open a public issue
for security reports.

- **Acknowledgment:** every report is acknowledged within **72 hours**.
- **Coordinated disclosure:** we work with you toward a fix and coordinate
  public disclosure within **90 days** of the initial report, or sooner once a
  fix is released. We will credit you unless you prefer to remain anonymous.

## Scope

This policy covers the agent-brain client CLI in this repository. The hosted
backend is a separate product with its own reporting channel at the same
address.

## Supported versions

Security fixes are applied to the latest released version. CI runs
`govulncheck` on every push and fails on any finding reachable from the call
graph.
