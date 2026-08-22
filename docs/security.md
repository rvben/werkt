# Credential and secret boundary

Werkt owns a named secret vault. Manifests contain lowercase hierarchical references such as `infrastructure/process-alert/github`, never credential values. The management API accepts plaintext only on create or rotation and returns metadata only; there is deliberately no reveal endpoint.

Values are encrypted with AES-256-GCM before PostgreSQL. Every write uses a fresh random nonce and binds the ciphertext to its secret name as authenticated additional data. The master key comes from `WERKT_SECRET_KEY` as base64-encoded 32 bytes and is never stored in PostgreSQL. Back up that key separately: a database backup without it cannot recover the values, and starting Werkt with a different key makes existing secrets fail closed.

Generate a key once and preserve it across restarts:

```bash
export WERKT_SECRET_KEY="$(openssl rand -base64 32)"
```

Secret names may contain lowercase letters, digits, dots, underscores, hyphens, and `/` hierarchy separators, up to 128 bytes. Values must be valid UTF-8 without NUL bytes and contain 8–65536 bytes. Webhook and email ingress credentials have the stronger minimum of 32 bytes.

## Management lifecycle

Create a secret from a local environment variable or a pipe, keeping the value out of process arguments and shell history:

```bash
werkt secret set --from-env GITHUB_TOKEN infrastructure/process-alert/github
printf %s "$WEBHOOK_SECRET" | werkt secret set \
  --description "HMAC key for process-alert ingress" \
  infrastructure/process-alert/webhook
```

`werkt secret list` and `werkt secret get NAME` return `name`, `description`, `version`, and timestamps only. Setting an existing name rotates its value and increments `version`. Create, rotation, and deletion are audit events containing the name and version, never plaintext or ciphertext.

Every deployment records the exact named-secret dependencies of its immutable revision. A missing name fails the validation stage before build work begins, and activation rechecks existence transactionally. Deletion returns `409 Conflict` while any retained runnable revision references the secret. When retention prunes an inactive artifact, it releases that revision's bindings because the revision can no longer run or be rolled back; active and otherwise protected artifacts retain theirs.

The current vault uses one master key. Rotate credential values freely through `secret set`; master-key rotation with multiple simultaneous decrypt keys is intentionally not implemented yet.

### Migrating environment references

This vault replaces the earlier `secretEnv`/`tokenEnv` contract. After upgrading, configure `WERKT_SECRET_KEY`, create the named values, change webhook `secretEnv` to `secret`, change email/ntfy `tokenEnv` to `tokenSecret`, replace each `runtime.secrets` source with a vault name, and redeploy. Old active revisions keep their history but cannot resolve host-environment references under the new contract.

## Webhook signatures

A webhook trigger requires `config.secret`:

```yaml
triggers:
  - id: incoming
    type: webhook
    config:
      secret: infrastructure/process-alert/webhook
      signatureHeader: X-Werkt-Signature # optional
```

The resolved value must be at least 32 bytes. The sender supplies a unique `Idempotency-Key`, the current Unix timestamp in `X-Werkt-Timestamp`, and computes HMAC-SHA256 over this exact byte sequence:

```text
<timestamp>.<idempotency-key>.<exact request body>
```

It sends the digest as lowercase hexadecimal with a `sha256=` prefix:

```text
X-Werkt-Signature: sha256=<64 hexadecimal characters>
```

Werkt accepts timestamps within five minutes, verifies the digest with a constant-time comparison, and uses the signed idempotency key to collapse replays into the original run. Missing or oversized idempotency keys receive `400`; stale timestamps and malformed or incorrect signatures receive `401`. A missing, undecryptable, or too-short credential fails closed with `503` and never becomes a queued run.

## Email and ntfy credentials

An email trigger requires `config.tokenSecret`. The sender supplies the resolved value as `Authorization: Bearer <token>`. Tokens must be at least 32 bytes and are compared in constant time before Werkt parses the RFC 5322 message. Each message must carry `Message-Id` or an `Idempotency-Key`, which prevents provider retries from creating duplicate runs. Because bearer tokens are replayable credentials, expose this endpoint only over TLS outside local development.

An ntfy trigger may use `config.tokenSecret`. Werkt resolves it only when opening the subscription and sends it as a bearer token. A missing vault or value prevents the connection and retries with backoff.

## Runtime injection and redaction

`runtime.secrets` maps guest environment-variable names to vault names:

```yaml
runtime:
  language: python
  image: python:3.13-alpine
  command: [python3, main.py]
  secrets:
    GITHUB_TOKEN: infrastructure/process-alert/github
```

Immediately before each attempt, the executor resolves only the distinct names requested by that revision and supplies the values to the mapped variables. A missing vault, name, or decryptable value fails the attempt before a local process or Husker VM is created. Rotations therefore take effect on the next attempt without redeployment.

Builds receive `runtime.environment` but never `runtime.secrets`. Both process builds and local runtime attempts start with a small allowlist of ordinary host variables such as `PATH`, `HOME`, locale, temporary-directory, and certificate paths; Werkt database, management, Husker, and vault credentials are not inherited. Runtime vault values go only to the attempt process or fresh Husker VM.

Before run logs cross the executor boundary, Werkt replaces every resolved plaintext value and its common URL-encoded, JSON-escaped, standard-base64, and raw-base64 forms with `[REDACTED]`. Redaction happens before the 1 MiB log limit, so stored logs and API responses share the sanitized form. It is deterministic defense in depth, not data-loss prevention: hashes, encryption, arbitrary transformations, split/interleaved output, and values deliberately copied into the JSON result cannot be inferred and removed. Automation authors must still avoid emitting credentials.

## Runtime network policy

Runtime VMs are offline by default. A manifest can declare only the destinations
its code needs:

```yaml
runtime:
  language: python
  command: [python3, main.py]
  egress:
    - host: api.github.com
      port: 443
    - host: metrics.internal.example
      port: 9090
      protocol: tcp
```

Werkt sends policy-bearing runs to Husker as `network: filtered`; it never turns
them into unrestricted NAT. Husker resolves hostnames before boot, pins their
IPv4 answers, and applies a TAP-keyed default-deny policy. DNS is limited to the
configured resolvers and other IPv4, IPv6, and layer-2 traffic is denied. An
older Husker that does not implement this contract rejects the distinct network
mode. The local process executor also rejects the manifest because host-process
network access cannot be constrained honestly.

Allowlisting a destination is not application-layer authorization. Use TLS and
validate the peer in the automation; an allowed service could proxy or change
behavior. Long-running VMs may need recreation after an upstream changes its
addresses, though normal Werkt attempts are intentionally short-lived.

## Remote package intake

The deployment endpoint is a code-execution boundary, not a file-storage endpoint. It requires management authentication, verifies the SHA-256 of the exact compressed request, streams to bounded temporary storage, and extracts only regular files beneath one package root. Absolute paths, traversal, backslashes, NUL bytes, links, special files, duplicate case-insensitive paths, excessive entries, and compressed or expanded size overages are rejected before validation or build execution.

Do not expose a server using `WERKT_EXECUTOR=process` to deployers you do not fully trust: a manifest build command executes on the control-plane host, and runtime code later does too. Use `WERKT_EXECUTOR=husker` to isolate remote builds and attempts in disposable microVMs. Husker currently assumes one administrative trust domain; it is not yet a hostile multi-tenant boundary.
