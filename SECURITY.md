# Security Policy

## Supported versions

While LLMObs is pre-1.0, security fixes are applied to the latest train release
on `main`. Once the project reaches 1.0, this section will enumerate the
supported version window.

## Reporting a vulnerability

**Do not open a public issue for security vulnerabilities.**

Please report suspected vulnerabilities privately using GitHub's
[private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability)
on this repository ("Security" tab → "Report a vulnerability").

Please include:

- A description of the vulnerability and its impact.
- Steps to reproduce, or a proof-of-concept.
- Affected version(s) / commit and deployment profile (lite or scale).
- Any suggested remediation.

## Our commitment

- We will acknowledge your report within **3 business days**.
- We will provide an assessment and expected remediation timeline within
  **10 business days**.
- We will keep you informed of progress and coordinate a disclosure date.
- We credit reporters in the release notes unless you prefer to remain
  anonymous.

## Scope notes

LLMObs treats **plugins as untrusted by construction**: the security boundary is
the gateway, the double-token auth model, and the permission intersection in the
Query API. Reports demonstrating that a plugin can escalate beyond its granted
capabilities, forge or replay an identity assertion, reach infrastructure
directly, or bypass the permission intersection are considered high severity.

Because fixtures and test data are synthetic only, do not send real user prompt
or completion payloads in a report.
