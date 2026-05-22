# Security Policy

## Reporting

Report suspected vulnerabilities privately through GitHub Security Advisories for
this repository. If GHSA is unavailable to you, email security@openclaw.ai.

Do not open public issues for vulnerabilities or include secrets, private
archives, database rows, tokens, or message contents in reports.

## Scope

In scope:

- crawlkit archive, cache, snapshot, SQLite, and filesystem handling
- credential discovery, config loading, and local path safety
- command output that could disclose tokens or private archive contents
- dependency or runtime behavior that materially affects local archive integrity

Out of scope:

- upstream service outages or API changes outside crawlkit control
- compromise of a trusted local account, shell, filesystem, or device
- scanner-only findings without a reachable exploit path in supported usage

## Expectations

We prioritize reachable issues that affect local archive confidentiality,
integrity, or safe execution. Include the affected version or commit, platform,
minimal reproduction steps, and sanitized impact details.
