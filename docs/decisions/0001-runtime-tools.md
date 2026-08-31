# ADR 0001: Resolve tools into immutable local Husker images

Status: accepted

## Context

Automation authors need a reusable way to request language runtimes without
maintaining a Dockerfile and registry image for every version combination.
Werkt and Husker run on separate hosts, Firecracker cannot consume Husker's
QEMU-only virtiofs host mounts, and a mutable shared mise cache would make an
old revision's runtime change underneath it.

## Decision

`runtime.tools` is an additive, exact-version map. Werkt resolves it through a
small built-in catalog. The resolution identity includes the catalog revision,
preparer revision, guest platform, exact tool backends and versions,
capabilities, preparation egress, local base-image digest, and mise version and
digest.

Deployment materializes a missing identity in a disposable, secret-free Husker
VM. Werkt uploads the checksum-verified mise binary, permits only catalogued
download destinations, installs and smoke-tests the requested tools, stops the
VM, and asks Husker to commit its root filesystem into the local image catalog.
Husker records the parent image and SHA-256 of the complete artifact. Werkt
includes that physical digest in revision identity and provenance v2.

Normal build and runtime VMs use Husker's existing reflink clone path. They do
not run mise installation and do not mount a shared cache. `runtime.image` and
`runtime.buildImage` remain mutually exclusive compatibility escape hatches for
software outside the curated catalog.

## Consequences

- Authors declare intent rather than build machinery.
- No per-automation Dockerfile, registry push, or OCI runtime layer is needed.
- Existing revisions fail closed if their prepared image is absent or changed;
  they never rebuild silently from newer operator inputs.
- Catalog additions are reviewed code changes. Arbitrary plugins, URLs, hooks,
  ranges, `latest`, and user-supplied installer scripts are not accepted.
- A small generic local base rootfs and a mise binary remain operator-managed,
  checksum-pinned inputs.
