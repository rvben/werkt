# Security policy

## Supported versions

Werkt is pre-1.0 software. Security fixes are made on the current `main` branch;
older commits and development snapshots are not supported independently.

| Version | Supported |
|---|---|
| Current `main` | Yes |
| Older snapshots | No |

## Reporting a vulnerability

Email [security@werkt.dev](mailto:security@werkt.dev). Do not open a public
issue for a suspected vulnerability.

Include, when available:

- the affected commit or version;
- the executor and deployment topology involved;
- reproduction steps or a minimal proof of concept;
- the impact and conditions required for exploitation; and
- any suggested mitigation.

Remove credentials, private addresses, customer data, and other unnecessary
sensitive material. If the report itself requires encrypted transport beyond
TLS email, ask for a secure exchange channel before sending the sensitive
details.

Expect an acknowledgement within three business days and an initial assessment
within seven business days. Remediation and disclosure timing depend on impact,
exploitability, and release readiness. Reporters will be kept informed of
material status changes and credited if they want attribution.

## Security scope

High-value reports include authentication or authorization bypasses, secret
exposure, sandbox escapes, unsafe package extraction, signature-validation
failures, cross-automation data access, and ways to bypass declared runtime
network policy.

The documented trust boundaries and non-goals still apply. In particular, the
local process executor is for trusted development and is not a security sandbox.
See [docs/security.md](docs/security.md) and
[docs/execution.md](docs/execution.md) before reporting behavior that those
documents explicitly classify as a non-goal.
