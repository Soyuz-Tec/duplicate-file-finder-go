# ADR 0004: Reproducible, signed, and immutable releases

- Status: Accepted
- Date: 2026-07-10

## Context

TwinTidy performs destructive filesystem actions. Users must be able to identify what version they are running, verify that an artifact came from the publisher, and distinguish a reviewed release from an arbitrary local build.

Embedding wall-clock build times, building on a maintainer workstation, or replacing assets under an existing release prevents meaningful reproduction and incident analysis. Authenticode signing is important for publisher identity but timestamped signatures necessarily change otherwise reproducible bytes.

## Decision

TwinTidy uses Semantic Versioning and protected `vMAJOR.MINOR.PATCH[-PRERELEASE]` tags. The semantic version, commit, and commit-derived source date are injected into runtime build information and aligned with PE and installer metadata.

Release verification rejects any modified, staged, deleted, renamed, or untracked repository input before building. Canonical release builds export the exact resolved Git commit into an isolated stage. They do not copy the mutable working tree and label it with `HEAD`. The claimed source date must equal the commit date. Local development retains an explicit working-tree build mode; dirty binaries carry a `+working-tree.<source-digest>` runtime identity and cannot serve as verified-release evidence.

Every unsigned executable has a deterministic JSON build receipt that binds its full commit, Git tree, SHA-256 source-tree digest, derived resource digest, toolchain and target settings, and executable SHA-256. Packaging requires independently captured executable and receipt digests, holds both files immutable through ZIP creation, includes the receipt in the archive, and re-verifies the embedded bytes before writing archive checksums.

Release workflows:

1. Check out the exact protected tag with read-only default token permissions.
2. Run source, safety, dependency, CodeQL, resource, and native architecture gates.
3. Build unsigned amd64 and ARM64 artifacts with a pinned Go patch release, `CGO_ENABLED=0`, `-trimpath`, and deterministic inputs.
4. Build twice from separate paths and compare unsigned hashes.
5. Record provenance and unsigned hashes.
6. Sign and RFC 3161 timestamp executables in a protected environment.
7. Package, sign, and verify installers.
8. Calculate SHA-256 checksums from final distributed bytes.
9. Publish immutable assets first as a draft or prerelease, then promote after verification.

Pull-request workflows never receive signing authority. Signing credentials live only in a protected environment with approval. Artifacts are never silently replaced under an existing version.

## Consequences

### Positive

- Runtime, Explorer, installer, source tag, and release identity agree.
- Unsigned build reproducibility remains measurable despite signed-byte variability.
- Compromised pull-request code cannot directly access signing authority.
- Incident response can map every artifact to source and provenance.
- A path replacement between build verification and packaging is rejected rather than coherently re-checksummed.

### Negative

- A trusted publisher identity and signing service/certificate are external release prerequisites.
- Signing and native ARM64 validation add release latency and operational cost.
- Release automation must manage PE numeric prerelease mapping and immutable artifacts carefully.
- Development and release modes are intentionally distinct, and callers must pass captured build and receipt digests into packaging.

## Alternatives considered

- **Unsigned stable releases:** rejected for a destructive Windows utility because publisher identity is part of user trust.
- **Build and upload from a maintainer workstation:** rejected because it is difficult to attest, reproduce, and constrain.
- **Embed current build time:** rejected because it defeats byte-for-byte unsigned reproduction; use the commit source date.
- **Overwrite a broken release asset:** rejected because users and checksums could refer to different bytes under one version; publish a patch release instead.

## Validation

- `TwinTidy.exe --version`, About UI, PE metadata, installer metadata, tag, and release name agree.
- Two clean unsigned builds from distinct paths have identical SHA-256 values.
- Build receipts from both runs identify the same exact Git tree and source digest and contain the matching executable SHA-256.
- Packaging rejects a modified executable or receipt even when it remains at the expected path.
- Authenticode and installer signature verification pass after timestamping.
- Final checksums and provenance refer to the exact published bytes.
- Install, upgrade, downgrade rejection, uninstall, reinstall, rollback-after-uninstall, package-integrity, amd64 smoke, and ARM64 smoke evidence is retained with the release.
