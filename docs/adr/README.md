# TwinTidy Architecture Decision Records

ADRs record material decisions that should not be rediscovered from code or chat history. They are append-only decision history: supersede an accepted ADR with a new ADR instead of rewriting its outcome.

## Status values

- **Proposed:** under review and not yet authoritative
- **Accepted:** the required direction; implementation may still be in progress
- **Superseded:** replaced by a named later ADR
- **Rejected:** retained to explain why an option was not chosen

## Index

| ADR | Status | Decision |
|---|---|---|
| [0001](0001-windows-native-no-cgo.md) | Accepted | Use a Windows-native Go/Walk desktop monolith with no CGO in release binaries |
| [0002](0002-verified-recycle-safety.md) | Accepted | Revalidate file identity and verify native recycle outcomes; no automatic permanent fallback |
| [0003](0003-amd64-arm64-resources.md) | Accepted | Support amd64 and ARM64 with generated architecture-specific resources |
| [0004](0004-versioning-signing-release.md) | Accepted | Use SemVer, reproducible unsigned builds, protected signing, and immutable releases |
| [0005](0005-disable-path-based-recycle.md) | Accepted | Disable path-based Windows recycling until the destructive sink is identity-bound |
| [0006](0006-per-user-msix-distribution.md) | Accepted | Ship a protected, signed, per-user MSIX for x64 and ARM64 alongside portable packages |

## Creating an ADR

Use the next four-digit number and include: status/date, context, decision, consequences, alternatives, and validation. Update this index and any affected architecture, security, or release document in the same change.
