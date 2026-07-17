# TwinTidy security policy

TwinTidy performs destructive filesystem operations, so possible data-loss behavior is treated as a security issue.

## Reporting privately

Use [GitHub private vulnerability reporting](https://github.com/Soyuz-Tec/twintidy/security/advisories/new) for:

- recycling a file that was not explicitly checked
- recycling every physical copy in a duplicate group
- bypassing identity, keeper, reparse-point, hard-link, or alternate-stream protections
- reporting success when Windows failed, cancelled, or produced an ambiguous result
- reading or exposing files outside the chosen scope
- code execution, dependency compromise, signing, or release-provenance concerns

Do not attach personal files, credentials, or unreviewed diagnostic logs. Reproduce destructive cases only with disposable fixtures or verified backups.

Include the TwinTidy version/commit, Windows version and architecture, storage type, exact reproduction steps, expected result, actual result, and the smallest safe supporting evidence. Please do not open a public issue until coordinated disclosure is complete.

## Supported versions

Before the first stable release, only the current `main` branch receives security fixes. After stable release, the latest signed release and current development branch are supported; superseded versions should be upgraded.

## Security design

The authoritative trust boundaries and destructive-action invariants are documented in [docs/SECURITY_MODEL.md](docs/SECURITY_MODEL.md), [ADR 0002](docs/adr/0002-verified-recycle-safety.md), and [ADR 0005](docs/adr/0005-disable-path-based-recycle.md). TwinTidy never treats a path alone as deletion authority and never automatically falls back to permanent deletion.
