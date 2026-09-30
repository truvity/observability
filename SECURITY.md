# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability, please report it privately via
[GitHub Security Advisories](https://github.com/truvity/observability/security/advisories/new).

Do NOT open a public issue for security vulnerabilities.

## Supported Versions

Only the latest release is supported with security updates.

## What is in scope

This repository publishes:

- The charts `observability-crds`, `platform-alerts`, `observability-stack`, `observability-emitters`, `alert-ingress`, `observability-dashboards`, `observability-grafana` and `observability-mcp`.
- The Go packages and commands: `pkg/tenancy`, `pkg/statusbox`, `cmd/alert-ingress`, `cmd/dashboardlint` and `cmd/rulecheck`, and the `setup.sh` that stands up the status box.
- The documentation, where it tells an adopter to expose or trust something it should not.

Reports that matter most:

- Tenancy: a grant, a rendered proxy entry or a token claim that lets a caller read a cluster or namespace it was not given, or a name `pkg/tenancy` should refuse and instead accepts or escapes.
- `alert-ingress` accepting a notification it should reject: an unsigned message, a topic outside the allow-list, or a mapping that drops an alert instead of routing it.
- A chart default that weakens TLS, authentication or network policy on the store, Grafana, the MCP servers or the status box.
- A secret, token or credential reaching a log line, a rendered manifest or a dashboard, or an MCP proxy that passes a caller's token on or widens what the store sees.

A finding that depends on how a particular deployment uses this repository
belongs with that deployment's owner.
