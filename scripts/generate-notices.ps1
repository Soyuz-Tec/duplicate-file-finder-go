#requires -Version 5.1

[CmdletBinding()]
param(
    [string]$OutputPath,
    [switch]$Check
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($OutputPath)) {
    $OutputPath = Join-Path $repoRoot "THIRD_PARTY_NOTICES.txt"
} else {
    $OutputPath = [System.IO.Path]::GetFullPath($OutputPath)
}

Push-Location $repoRoot
try {
    & go mod download
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to download the locked Go module set."
    }
    $moduleLines = @(& go list -mod=readonly -m -f '{{if not .Main}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}' all)
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to enumerate Go modules."
    }
} finally {
    Pop-Location
}

$entries = foreach ($line in $moduleLines) {
    if ([string]::IsNullOrWhiteSpace($line)) { continue }
    $parts = $line -split '\|', 3
    if ($parts.Count -ne 3 -or [string]::IsNullOrWhiteSpace($parts[2])) {
        throw "Invalid or unavailable module metadata: $line"
    }

    $license = Get-ChildItem -LiteralPath $parts[2] -File |
        Where-Object { $_.Name -match '^(LICENSE|LICENCE|COPYING)(\..*)?$' } |
        Sort-Object -Property Name |
        Select-Object -First 1
    if ($null -eq $license) {
        throw "No root license file found for $($parts[0]) $($parts[1])."
    }

    [pscustomobject]@{
        Path = $parts[0]
        Version = $parts[1]
        LicensePath = $license.FullName
    }
}

$builder = [System.Text.StringBuilder]::new()
[void]$builder.Append("TwinTidy third-party notices`n")
[void]$builder.Append("================================`n`n")
[void]$builder.Append("TwinTidy includes the Go modules listed below. Their license texts follow.`n")
[void]$builder.Append("This file is generated deterministically from the modules selected by go.mod and go.sum.`n")

foreach ($entry in ($entries | Sort-Object -Property Path)) {
    $licenseText = [System.IO.File]::ReadAllText($entry.LicensePath)
    $licenseText = $licenseText.Replace("`r`n", "`n").Replace("`r", "`n").TrimEnd()
    [void]$builder.Append("`n`n-------------------------------------------------------------------------------`n")
    [void]$builder.Append($entry.Path)
    [void]$builder.Append(" ")
    [void]$builder.Append($entry.Version)
    [void]$builder.Append("`n-------------------------------------------------------------------------------`n")
    [void]$builder.Append($licenseText)
    [void]$builder.Append("`n")
}

$expected = $builder.ToString()
if ($Check) {
    if (-not [System.IO.File]::Exists($OutputPath)) {
        throw "Third-party notice file is missing: $OutputPath"
    }
    $actual = [System.IO.File]::ReadAllText($OutputPath).Replace("`r`n", "`n").Replace("`r", "`n")
    if ($actual -ne $expected) {
        throw "Third-party notices are stale; run scripts/generate-notices.ps1."
    }
    [pscustomobject]@{ Path = $OutputPath; Current = $true; Modules = @($entries).Count }
    return
}

[System.IO.File]::WriteAllText($OutputPath, $expected, [System.Text.UTF8Encoding]::new($false))
[pscustomobject]@{ Path = $OutputPath; Generated = $true; Modules = @($entries).Count }
