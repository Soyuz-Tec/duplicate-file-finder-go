# Contributing to TwinTidy

TwinTidy welcomes focused fixes and improvements that preserve its local-first, fail-closed cleanup model.

## Before changing code

Read [AGENTS.md](AGENTS.md), [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md), [docs/SECURITY_MODEL.md](docs/SECURITY_MODEL.md), and the applicable [ADRs](docs/adr/README.md). Material changes to file identity, cleanup, security, public APIs, dependencies, GUI concurrency, supported architectures, or release topology need an ADR update.

Create a branch for one reviewable problem. Avoid unrelated formatting or dependency churn.

## Local verification

Use the Go version in `go.mod` and set CGO only for the current shell:

```powershell
$env:CGO_ENABLED = "0"
go mod verify
go mod tidy -diff
go vet ./...
go test ./... -count=1
git diff --check
```

For resource or release changes, also run:

```powershell
./scripts/generate-resources.ps1 -Architecture all -Check
./scripts/test-release-hardening.ps1
./scripts/verify-release.ps1 -Version dev
```

Static analysis and workflow linting run in CI and can be reproduced locally without a separate installation:

```powershell
go run honnef.co/go/tools/cmd/staticcheck@2026.1 ./...
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
```

If a gitignored directory containing Go module sources exists in the working tree (for example a local module-cache export under `dist/`), the `./...` pattern fails on its versioned import paths; run the same commands against `./cmd/... ./internal/...` instead. CI checkouts are clean, so workflows keep using `./...`.

Do not use `go env -w` in project instructions, commit generated executables, hard-code credentials, or add a permanent-delete fallback.

## Destructive-workflow changes

Start with a regression test. Exercise replacement, same-size mutation, timestamp preservation, hard links, alternate data streams, reparse points, missing/changed keepers, locked/access-denied targets, native cancellation, ambiguous native results, source-path post-checks, and stale GUI operations as applicable. Use disposable fixtures only.

Pull requests must explain the safety impact, exact verification commands, architecture impact, risk, and rollback using the repository template.

Security and possible data-loss findings belong in [private vulnerability reporting](https://github.com/Soyuz-Tec/twintidy/security/advisories/new).
