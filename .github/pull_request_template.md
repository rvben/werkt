# Pull request

## What changed

Describe the observable behavior and why the change is needed.

## Verification

List the commands and scenarios used to verify the change.

## Checklist

- [ ] I added or updated tests for changed behavior.
- [ ] I documented API, manifest, configuration, security-boundary, or
  operator-facing changes.
- [ ] I ran `go vet ./...` and `go test ./... -count=1`.
- [ ] I ran the relevant SDK and browser-contract tests.
- [ ] I ran `./scripts/check-public-safety.sh` and removed private or sensitive
  data.
- [ ] This change preserves compatibility, or its breaking impact is explicit.
