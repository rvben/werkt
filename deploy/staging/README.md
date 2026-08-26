# Werkt staging deployment

This directory defines a persistent, private staging environment with:

- a Linux x86-64 control host running Werkt and PostgreSQL 17.11;
- a separate Linux x86-64 execution host running Husker v0.4.47 with Firecracker;
- a loopback-only Husker API reached through a restricted SSH tunnel;
- atomic Werkt binary deployment with readiness-based rollback;
- a protected, manually dispatched GitHub staging environment.

No private address, credential, key, database dump, or environment-specific
inventory belongs in this repository.

## Control host

Install Docker with the Compose plugin, OpenSSH client, curl, and a TLS reverse
proxy. Create the service identity and directories:

```bash
sudo useradd --system --home /var/lib/werkt --shell /usr/sbin/nologin werkt
sudo install -d -m 0750 -o werkt -g werkt /var/lib/werkt
sudo install -d -m 0700 -o root -g root /etc/werkt
sudo install -d -m 0755 -o root -g root /usr/local/lib/werkt/releases
```

Install the tracked service definitions and root-owned deployment helper:

```bash
sudo install -m 0644 deploy/staging/werkt.service /etc/systemd/system/werkt.service
sudo install -m 0644 deploy/staging/werkt-postgres.service /etc/systemd/system/werkt-postgres.service
sudo install -m 0644 deploy/staging/werkt-husker-tunnel.service /etc/systemd/system/werkt-husker-tunnel.service
sudo install -m 0644 deploy/staging/postgres.compose.yml /etc/werkt/postgres.compose.yml
sudo install -m 0755 deploy/staging/install-werkt-release /usr/local/sbin/install-werkt-release
sudo install -m 0755 deploy/staging/verify-werkt-staging /usr/local/sbin/verify-werkt-staging
```

Create these root-owned files outside Git:

| Path | Mode | Purpose |
|---|---:|---|
| `/etc/werkt/werkt.env` | `0640`, group `werkt` | Copy of `werkt.env.example` with real tokens and key |
| `/etc/werkt/postgres-password` | `0600` | PostgreSQL account password used by Compose |
| `/etc/werkt/pgpass` | `0600`, owner `werkt` | `127.0.0.1:5432:werkt:werkt:<password>` |
| `/etc/werkt/husker-tunnel.env` | `0640`, group `werkt` | Copy of `husker-tunnel.env.example` with the execution host |
| `/etc/werkt/husker_tunnel_key` | `0600`, owner `werkt` | Dedicated SSH private key used only for forwarding |
| `/etc/werkt/husker_known_hosts` | `0644` | Pinned execution-host SSH key |
| `/usr/local/sbin/verify-werkt-staging` | `0755`, owner `root` | Host-local authenticated deployment check |

Keep `WERKT_ENVIRONMENT=staging` and `WERKT_INSTANCE=staging-control` from the
example environment file. The protected workflow embeds those values as the
staging artifact's fallback operator scope and refuses conflicting runtime
overrides before opening the server. This makes a mislabeled control plane fail
the installer's readiness window and recover the previous release automatically.

Generate independent staging credentials. Keep the vault key outside database
backups and never reuse production values:

```bash
openssl rand -hex 32
openssl rand -base64 32
```

Enable PostgreSQL and the tunnel after their configuration is present:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now werkt-postgres.service
sudo systemctl enable --now werkt-husker-tunnel.service
```

## Execution host

Use a dedicated Linux x86-64 host with `/dev/kvm`. Install and pin Husker
v0.4.47, copy `husker-config.toml.example` to `/etc/husker/config.toml`, replace
the API token, install Husker's upstream systemd unit, then run:

```bash
sudo husker images pull
sudo husker doctor
sudo systemctl enable --now husker.service
```

Leave the Husker API on its loopback default. Create a dedicated SSH account and
authorize only the tunnel key. Its `authorized_keys` entry should begin with:

```text
restrict,port-forwarding,permitopen="127.0.0.1:7777"
```

Append the public key on that same line. Firewall SSH to the control host and
administrative network. Do not expose port 7777.

## TLS boundary

Use separate private and public hostnames:

- The private hostname proxies the full `127.0.0.1:8080` application and is
  reachable only through the administrative network or identity-aware proxy.
- The public hostname proxies only `/api/v1/hooks/*` and `/api/v1/email/*`.
  Return `404` for every other path.

Both listeners require TLS. PostgreSQL, SSH forwarding, and Husker remain on the
private network.

## Protected staging deployment

Install a GitHub Actions runner on the control host with labels
`self-hosted`, `linux`, `x64`, and `staging-control`. Do not use this runner for
pull-request workflows. Give its service account passwordless sudo permission
for these two root-owned helpers only:

```text
/usr/local/sbin/install-werkt-release
/usr/local/sbin/verify-werkt-staging
```

The tracked installer pins and verifies the runner release before registering
it. Generate a short-lived repository registration token in GitHub, then run:

```bash
sudo install -m 0440 deploy/staging/werkt-runner.sudoers /etc/sudoers.d/werkt-staging-runner
sudo visudo --check --file=/etc/sudoers.d/werkt-staging-runner
sudo --preserve-env=WERKT_RUNNER_TOKEN deploy/staging/install-github-runner
```

Pass `WERKT_RUNNER_TOKEN` only through the process environment and unset it
immediately afterward. The runner is repository-scoped, runs as the isolated
`werkt-runner` account, and can elevate only through the two constrained helpers.
The release installer does not finish a failed promotion until the previously
active commit has recovered readiness; a rollback that cannot become ready is
reported separately in its logs.
All remote actions in this repository are pinned to immutable commit SHAs, and
CI rejects `pull_request_target` or use of the self-hosted label outside the
protected staging workflow.

Create a GitHub environment named `staging` with a required reviewer, then add:

- environment variable `WERKT_STAGING_URL` containing the private TLS URL.

Keep `WERKT_MANAGEMENT_TOKEN` only in `/etc/werkt/werkt.env`. The protected
workflow performs its authenticated check through the root-owned
`verify-werkt-staging` helper, so the credential never enters GitHub or the
runner workspace. Set `WERKT_STAGING_CANARY_AUTOMATION` to a dedicated private
automation whose manual invocation has no external side effects. The host-local
check requires its active revision to carry signed artifact provenance, queues
one revision-pinned run, and waits for it to succeed. Before the first
provenance-aware Werkt upgrade, deploy this canary with digest-pinned images on
the existing release and add the environment setting; older Werkt releases
accept the pinned syntax, so the protected upgrade can adopt and execute it.

Run the **Deploy staging** workflow manually. It verifies the current `main`,
builds one immutable target-bound binary, checks its checksum on the control
host, switches the managed symlink, and rolls back automatically unless
`/readyz` reports the expected commit. The final smoke test confirms the exact
`staging` / `staging-control` operator scope, workspace, authentication boundary,
database readiness, build identity, retained artifact verification, and one
attested canary execution.

## Backup and restore gate

Create a PostgreSQL custom-format backup without writing credentials to the
command line:

```bash
sudo docker compose -f /etc/werkt/postgres.compose.yml exec -T postgres \
  pg_dump -U werkt -d werkt --format=custom > werkt-staging.dump
```

Encrypt and copy that dump off-host. Back up `WERKT_SECRET_KEY` separately. A
release is not staging-qualified until a clean host can restore both and decrypt
an existing staging secret.
