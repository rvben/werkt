# Contributing to Werkt

Thanks for helping improve Werkt. The project is an executable MVP and its
security boundaries are still evolving, so changes should be small, explicit,
and supported by tests.

## Before you start

- Search existing issues before opening a new one.
- Use an issue to discuss substantial features, manifest changes, or API
  changes before investing in an implementation.
- Report vulnerabilities privately according to [SECURITY.md](SECURITY.md).
- Keep environment-specific infrastructure, addresses, credentials, and
  deployment evidence out of the repository.

## Development setup

Install Go 1.26, Docker with Compose, and PostgreSQL client tooling. Start the
development database:

```bash
docker compose up -d postgres
```

Run the primary verification suite:

```bash
go vet ./...
go test ./... -count=1
```

The browser contract additionally requires Google Chrome:

```bash
WERKT_BROWSER_TESTS=1 go test ./internal/httpapi \
  -run TestWorkspaceBrowserKeyboardFocusAndResponsiveModality -count=1
```

Run the SDK suites when their code or shared protocol changes:

```bash
PYTHONPATH=sdk/python python3 -m unittest discover -s sdk/python/tests -v
(cd sdk/rust && cargo test --locked)
```

Format Go code with `gofmt` and run the public-safety gate before committing:

```bash
gofmt -w path/to/changed.go
./scripts/check-public-safety.sh
```

Run `./scripts/check-public-safety.sh --history` before publishing repository
history.

## Change expectations

- Add regression tests before or alongside behavior changes.
- Preserve API, manifest, and process-protocol compatibility unless a breaking
  change has been discussed explicitly.
- Document changes to security boundaries, failure behavior, configuration,
  or operator workflows.
- Keep commits focused and use Conventional Commits messages.
- Never commit plaintext credentials or private environment details.

By submitting a contribution, you agree that it is licensed under the
[Apache License 2.0](LICENSE), as described by section 5 of that license.
