# Credential and secret boundary

Werkt stores references to credentials, never credential values. References are ordinary environment-variable names in `automation.yaml`; their values are resolved only by the component that needs them.

Names beginning with `WERKT_` cannot be referenced by trigger credentials or runtime secrets. This prevents an automation manifest from deliberately mapping the control plane's database, management, or Husker credentials into a guest.

## Remote package intake

The deployment endpoint is a code-execution boundary, not a file-storage endpoint. It requires management authentication, verifies the SHA-256 of the exact compressed request, streams to bounded temporary storage, and extracts only regular files beneath one package root. Absolute paths, traversal, backslashes, NUL bytes, links, special files, duplicate case-insensitive paths, excessive entries, and compressed or expanded size overages are rejected before validation or build execution.

Do not expose a server using `WERKT_EXECUTOR=process` to deployers you do not fully trust: a manifest build command executes on the control-plane host, and runtime code later does too. Use `WERKT_EXECUTOR=husker` to isolate remote builds and attempts in disposable microVMs. Husker currently assumes one administrative trust domain; it is not yet a hostile multi-tenant boundary.

## Webhook signatures

A webhook trigger requires `config.secretEnv`:

```yaml
triggers:
  - id: incoming
    type: webhook
    config:
      secretEnv: PROCESS_ALERT_WEBHOOK_SECRET
      signatureHeader: X-Werkt-Signature # optional
```

The referenced value must be at least 32 bytes. The sender supplies a unique `Idempotency-Key`, the current Unix timestamp in `X-Werkt-Timestamp`, and computes HMAC-SHA256 over this exact byte sequence:

```text
<timestamp>.<idempotency-key>.<exact request body>
```

It sends the digest as lowercase hexadecimal with a `sha256=` prefix:

```text
X-Werkt-Signature: sha256=<64 hexadecimal characters>
```

Werkt accepts timestamps within five minutes, verifies the digest with a constant-time comparison, and uses the signed idempotency key to collapse replays into the original run. Missing or oversized idempotency keys receive `400`; stale timestamps and malformed or incorrect signatures receive `401`. A missing or too-short server-side secret fails closed with `503` and never becomes a queued run.

## Email ingress

An email trigger requires `config.tokenEnv`. The sender supplies that per-trigger value as `Authorization: Bearer <token>`. Tokens must be at least 32 bytes and are compared in constant time before Werkt parses the RFC 5322 message. Each message must carry `Message-Id` or an `Idempotency-Key`, which prevents provider retries from creating duplicate runs. Because bearer tokens are replayable credentials, expose this endpoint only over TLS outside local development.

## Runtime secrets

`runtime.secrets` maps guest variable names to control-plane environment-variable references:

```yaml
runtime:
  language: python
  image: python:3.13-alpine
  command: [python3, main.py]
  secrets:
    GITHUB_TOKEN: PROCESS_ALERT_GITHUB_TOKEN
```

Only `PROCESS_ALERT_GITHUB_TOKEN` is persisted in the manifest. Immediately before an attempt, the executor resolves it and supplies the value as `GITHUB_TOKEN`. An unset or empty reference fails the attempt before a process or Husker VM is created.

Husker builds receive `runtime.environment` but never `runtime.secrets`; runtime values go only to the fresh attempt VM. The local process executor also constructs a minimal runtime environment instead of inheriting the control plane's full environment. Its build path still inherits the host environment because that backend is explicitly for trusted local development.

Werkt does not serialize resolved secret values into API responses, database rows, audit details, or runner errors. Automation stdout and stderr are persisted as run logs, so application code can still disclose a secret by printing it. Log redaction and a dedicated secret backend remain future defense-in-depth work.
