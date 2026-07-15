#requires -Version 5.1

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Tag,
    [string]$ExpectedCommit,
    [string]$DefaultBranch = "origin/main"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "TwinTidy.Version.ps1")

$repoRoot = Split-Path -Parent $PSScriptRoot
if ($Tag -notmatch '^v(.+)$') {
    throw "Release tag '$Tag' must start with v."
}
$versionInfo = Resolve-TwinTidyVersion -Version $Matches[1]
if ($versionInfo.Canonical -ceq "dev" -or $Tag -cne "v$($versionInfo.Canonical)") {
    throw "Release tag '$Tag' is not a canonical TwinTidy SemVer tag."
}

Push-Location $repoRoot
try {
    $tagRef = "refs/tags/$Tag"
    $tagType = (& git cat-file -t $tagRef 2>&1 | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $tagType -cne "tag") {
        throw "Release tag '$Tag' must be an annotated tag, not a lightweight tag."
    }
    $commit = (& git rev-parse --verify "$tagRef^{commit}" 2>&1 | Out-String).Trim().ToLowerInvariant()
    if ($LASTEXITCODE -ne 0 -or $commit -notmatch '^[0-9a-f]{40,64}$') {
        throw "Release tag '$Tag' does not resolve to a commit."
    }
    if (-not [string]::IsNullOrWhiteSpace($ExpectedCommit) -and $commit -cne $ExpectedCommit.ToLowerInvariant()) {
        throw "Release tag commit '$commit' does not match expected '$ExpectedCommit'."
    }
    & git rev-parse --verify "$DefaultBranch^{commit}" *> $null
    if ($LASTEXITCODE -ne 0) {
        throw "Default-branch ref '$DefaultBranch' is unavailable; release checkout must use fetch-depth 0."
    }
    & git merge-base --is-ancestor $commit $DefaultBranch
    if ($LASTEXITCODE -ne 0) {
        throw "Release tag '$Tag' does not point to a commit reachable from $DefaultBranch."
    }
    $head = (& git rev-parse --verify "HEAD^{commit}" 2>&1 | Out-String).Trim().ToLowerInvariant()
    if ($LASTEXITCODE -ne 0 -or $head -cne $commit) {
        throw "Checked-out HEAD '$head' does not equal release tag commit '$commit'."
    }
    $sourceDate = (& git show -s --format=%cI $commit 2>&1 | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($sourceDate)) {
        throw "Unable to resolve release source date."
    }
} finally {
    Pop-Location
}

$releaseNotesPath = Join-Path $repoRoot "docs\releases\$Tag.md"
if (-not [System.IO.File]::Exists($releaseNotesPath)) {
    throw "Release notes are missing: $releaseNotesPath"
}
$changelog = [System.IO.File]::ReadAllText((Join-Path $repoRoot "CHANGELOG.md"))
if ($changelog -notmatch [regex]::Escape("## [$($versionInfo.Canonical)]")) {
    throw "CHANGELOG.md has no versioned section for $($versionInfo.Canonical)."
}
if ([string]::IsNullOrWhiteSpace($versionInfo.MSIXVersion)) {
    throw "Version '$($versionInfo.Canonical)' cannot be represented by the TwinTidy MSIX version policy."
}

$currentMSIXVersion = [System.Version]::Parse($versionInfo.MSIXVersion)
$highestExistingMSIXVersion = $null
$highestExistingTag = $null
Push-Location $repoRoot
try {
    $existingTags = @(& git tag --list "v*")
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to enumerate existing release tags for MSIX version policy."
    }
} finally {
    Pop-Location
}
foreach ($existingTag in $existingTags) {
    if ($existingTag -ceq $Tag -or $existingTag -notmatch '^v(.+)$') {
        continue
    }
    try {
        $existingVersion = Resolve-TwinTidyVersion -Version $Matches[1]
    } catch {
        throw "Existing release tag '$existingTag' is not canonical under the current version policy: $($_.Exception.Message)"
    }
    if ([string]::IsNullOrWhiteSpace($existingVersion.MSIXVersion)) {
        throw "Existing release tag '$existingTag' cannot be represented by the MSIX version policy."
    }
    $existingMSIXVersion = [System.Version]::Parse($existingVersion.MSIXVersion)
    if ($existingMSIXVersion -eq $currentMSIXVersion) {
        throw "Release tag '$Tag' collides with '$existingTag' at MSIX version '$($versionInfo.MSIXVersion)'."
    }
    if ($null -eq $highestExistingMSIXVersion -or $existingMSIXVersion -gt $highestExistingMSIXVersion) {
        $highestExistingMSIXVersion = $existingMSIXVersion
        $highestExistingTag = $existingTag
    }
}
if ($null -ne $highestExistingMSIXVersion -and $currentMSIXVersion -le $highestExistingMSIXVersion) {
    throw "Release tag '$Tag' maps to MSIX '$currentMSIXVersion', which is not newer than '$highestExistingTag' ('$highestExistingMSIXVersion')."
}

[pscustomobject]@{
    Tag = $Tag
    Version = $versionInfo.Canonical
    PEVersion = $versionInfo.PEVersion
    MSIXVersion = $versionInfo.MSIXVersion
    Commit = $commit
    SourceDate = $sourceDate
    ReleaseNotesPath = $releaseNotesPath
    Prerelease = $versionInfo.IsPrerelease
}
