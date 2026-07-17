# TwinTidy Code Signing Policy

Status: required policy, 2026-07-17

TwinTidy is an open-source Windows application published by **Kayilan Inc**. Release binaries are Authenticode-signed so that Windows and its users can verify that a downloaded artifact was produced by this project and has not been altered. This document states who may sign TwinTidy releases, how signing is controlled, and what data the signing process handles. It complements the technical controls in [RELEASE.md](RELEASE.md) and the provider-neutral [signing adapter contract](SIGNING_ADAPTER.md).

Code signing for TwinTidy is provided free of charge by the [**SignPath Foundation**](https://signpath.org/), which issues the code-signing certificate. The certificate is owned by the SignPath Foundation and used under its terms; Kayilan Inc is the subscriber and the party responsible for the signed content.

## Scope

This policy covers every artifact distributed under a TwinTidy version tag: the per-architecture `TwinTidy.exe`, the per-user MSIX packages, and any installer or archive signed on their behalf. It does not cover unsigned development builds, which are explicitly labeled as such and are never published as releases.

## Team and roles

Signing authority is divided into three roles. During the current phase a single maintainer holds all three; the separation is retained so that additional reviewers and approvers can be added without changing the policy.

| Role | Responsibility | Current holder |
| --- | --- | --- |
| **Author** | Produces the source and the reproducible unsigned build to be signed. | Kayilan Inc maintainer (GitHub: `Soyuz-Tec`) |
| **Reviewer** | Confirms the artifact was built from the reviewed default-branch commit, that reproducibility and provenance checks passed, and that the version metadata matches the release. | Kayilan Inc maintainer (GitHub: `Soyuz-Tec`) |
| **Approver** | Grants the manual, per-release approval that releases the signing operation in the protected environment. | Kayilan Inc maintainer (GitHub: `Soyuz-Tec`) |

Every person holding any of these roles maintains the TwinTidy source repository and signs only from that repository's reviewed source. As the team grows, new members are added to the appropriate role and recorded in this table by a reviewed change to this file.

## Authentication and account security

Every account with a signing role enforces multi-factor authentication on both the signing platform (SignPath) and the source-code platform (GitHub). An account that cannot present a second factor is not permitted to hold a signing role.

## Signing controls

TwinTidy signing follows these rules, enforced by the release pipeline and the signing adapter:

1. **Own binaries only.** Only binaries built from TwinTidy's own source are signed. Unsigned upstream open-source library binaries may be embedded in a package, but the signature attests to the TwinTidy build, not to third-party code.
2. **Reproducible input.** The exact bytes to be signed are produced by the reproducible, provenance-bound unsigned build described in [RELEASE.md](RELEASE.md). The unsigned build receipt is never rewritten to describe a signed executable.
3. **Manual per-release approval.** Each release is signed only after an explicit human approval in the protected `production-signing` environment. No automated or unattended path can sign an artifact.
4. **Protected environment.** Signing runs only in the protected release environment, never for pull requests or untrusted forks. Signing credentials are held by the provider and are never present in repository files, logs, command history, or build artifacts.
5. **Enforced metadata.** Every signed artifact carries a consistent product name (`TwinTidy`), company/publisher identity (`Kayilan Inc`), and a version that matches the release tag across the Git tag, runtime `--version`, PE metadata, installer metadata, and artifact names.
6. **Identity pinning.** The release verifies the signer against the exact configured certificate subject and certificate identity (leaf-certificate SHA-256 or durable Artifact Signing EKU) before any release is published. Any mismatch is a release-stopping failure.
7. **No test certificates.** A self-signed or test certificate is never substituted for a public release.

## Privacy

TwinTidy is a local desktop application. It does not transmit user files, scan results, or personal data off the user's machine; duplicate scanning and reporting are performed locally, and diagnostics are written locally with privacy-conscious content as described in [SECURITY_MODEL.md](SECURITY_MODEL.md).

The signing process itself handles only build artifacts and public certificate identifiers. It collects no end-user data. Signing-provider audit logs record the release tag, commit, and signing operation for accountability and are retained by the maintainer and the signing provider; they contain no end-user personal data.

## Attribution

Free code signing for this project is provided by the [SignPath Foundation](https://signpath.org/), with a free code-signing certificate. This attribution is included here and is reproduced on the project's release and download materials.

## Changes to this policy

This policy is versioned in the repository. Any change to the signing team, roles, certificate identity, or controls is made through a reviewed change to this file on the protected default branch. Certificate-identity changes additionally follow the reviewed identity-rotation process described in [SIGNING_ADAPTER.md](SIGNING_ADAPTER.md).
