#requires -Version 5.1

[CmdletBinding()]
param(
    [ValidateSet("all", "amd64", "arm64")]
    [string[]]$Architecture = @("all"),

    [string]$SourceRoot,

    [string]$OutputPrefix,

    [string]$Version,

    [switch]$Check
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "TwinTidy.Version.ps1")
. (Join-Path $PSScriptRoot "TwinTidy.Release.ps1")

$tool = "github.com/tc-hib/go-winres@v0.3.3"
$repoRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($SourceRoot)) {
    $SourceRoot = $repoRoot
} else {
    $SourceRoot = [System.IO.Path]::GetFullPath($SourceRoot)
}

if ($Architecture -contains "all") {
    if ($Architecture.Count -ne 1) {
        throw "Architecture 'all' cannot be combined with another architecture."
    }
    $targets = @("amd64", "arm64")
} else {
    $targets = @($Architecture)
}
if ($Check -and -not [string]::IsNullOrWhiteSpace($Version)) {
    throw "-Check validates the checked-in development resources; do not combine it with -Version."
}

$configPath = Join-Path $SourceRoot "cmd\twintidy\winres\winres.json"
$manifestPath = Join-Path $SourceRoot "cmd\twintidy\twintidy.manifest"
$iconPath = Join-Path $SourceRoot "cmd\twintidy\winres\icon.png"
if ([string]::IsNullOrWhiteSpace($OutputPrefix)) {
    $OutputPrefix = Join-Path $SourceRoot "cmd\twintidy\rsrc"
} else {
    $OutputPrefix = [System.IO.Path]::GetFullPath($OutputPrefix)
}

foreach ($source in @($configPath, $manifestPath, $iconPath)) {
    if (-not [System.IO.File]::Exists($source)) {
        throw "Required resource source is missing: $source"
    }
}

$null = Assert-TwinTidyManifestPolicy -Path $manifestPath

$generatorConfigPath = $configPath
$temporaryConfigPath = $null

function New-VersionedConfig {
    param([Parameter(Mandatory = $true)][string]$RequestedVersion)

    $resolved = Resolve-TwinTidyVersion -Version $RequestedVersion
    $config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
    $versionResource = $config.RT_VERSION.'#1'.'0000'
    $versionResource.fixed.file_version = $resolved.PEVersion
    $versionResource.fixed.product_version = $resolved.PEVersion
    $versionResource.fixed.flags = if ($resolved.IsPrerelease) { "Prerelease" } else { "" }
    if ($resolved.Canonical -eq "dev") {
        $versionResource.fixed.flags = "Debug,Prerelease"
    }

    $strings = $versionResource.info.'0409'
    $strings.FileVersion = $resolved.Canonical
    $strings.ProductVersion = $resolved.Canonical
    $strings.Comments = if ($resolved.Canonical -eq "dev") { "Development build; official public releases are Authenticode signed" } elseif ($resolved.IsPrerelease) { "Prerelease build" } else { "Release build" }
    $strings.SpecialBuild = $resolved.SpecialBuild

    $script:temporaryConfigPath = Join-Path (Split-Path -Parent $configPath) ("winres.generated." + [System.Guid]::NewGuid().ToString("N") + ".json")
    $json = $config | ConvertTo-Json -Depth 20
    [System.IO.File]::WriteAllText($script:temporaryConfigPath, $json + "`n", [System.Text.UTF8Encoding]::new($false))
    $script:generatorConfigPath = $script:temporaryConfigPath
}

function Invoke-ResourceGenerator {
    param([Parameter(Mandatory = $true)][string]$Prefix)

    [System.IO.Directory]::CreateDirectory((Split-Path -Parent $Prefix)) | Out-Null
    & go run $tool make --in $generatorConfigPath --arch ($targets -join ",") --out $Prefix
    if ($LASTEXITCODE -ne 0) {
        throw "go-winres failed with exit code $LASTEXITCODE"
    }
}

function Get-ResourcePath {
    param(
        [Parameter(Mandatory = $true)][string]$Prefix,
        [Parameter(Mandatory = $true)][string]$TargetArchitecture
    )
    return "${Prefix}_windows_${TargetArchitecture}.syso"
}

function Remove-VerifiedTempDirectory {
    param([Parameter(Mandatory = $true)][string]$Path)

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $tempRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
    if (-not $tempRoot.EndsWith([System.IO.Path]::DirectorySeparatorChar.ToString())) {
        $tempRoot += [System.IO.Path]::DirectorySeparatorChar
    }
    if (-not $fullPath.StartsWith($tempRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to remove a directory outside the system temp root: $fullPath"
    }
    if (-not ([System.IO.Path]::GetFileName($fullPath)).StartsWith("TwinTidyResources-", [System.StringComparison]::Ordinal)) {
        throw "Refusing to remove an unexpected temp directory: $fullPath"
    }
    if ([System.IO.Directory]::Exists($fullPath)) {
        [System.IO.Directory]::Delete($fullPath, $true)
    }
}

try {
    if (-not [string]::IsNullOrWhiteSpace($Version)) {
        New-VersionedConfig -RequestedVersion $Version
    }

    if (-not $Check) {
        Invoke-ResourceGenerator -Prefix $OutputPrefix
        foreach ($arch in $targets) {
            $resourcePath = Get-ResourcePath -Prefix $OutputPrefix -TargetArchitecture $arch
            [pscustomobject]@{
                Architecture = $arch
                Path = $resourcePath
                SHA256 = (Get-FileHash -LiteralPath $resourcePath -Algorithm SHA256).Hash
                Version = if ([string]::IsNullOrWhiteSpace($Version)) { "source-config" } else { (Resolve-TwinTidyVersion -Version $Version).Canonical }
            }
        }
        return
    }

    $tempDirectory = Join-Path ([System.IO.Path]::GetTempPath()) ("TwinTidyResources-" + [System.Guid]::NewGuid().ToString("N"))
    $firstPrefix = Join-Path $tempDirectory "first\rsrc"
    $secondPrefix = Join-Path $tempDirectory "second\rsrc"
    try {
        Invoke-ResourceGenerator -Prefix $firstPrefix
        Invoke-ResourceGenerator -Prefix $secondPrefix

        foreach ($arch in $targets) {
            $firstPath = Get-ResourcePath -Prefix $firstPrefix -TargetArchitecture $arch
            $secondPath = Get-ResourcePath -Prefix $secondPrefix -TargetArchitecture $arch
            $trackedPath = Get-ResourcePath -Prefix $OutputPrefix -TargetArchitecture $arch
            if (-not [System.IO.File]::Exists($trackedPath)) {
                throw "Tracked resource object is missing: $trackedPath"
            }

            $firstHash = (Get-FileHash -LiteralPath $firstPath -Algorithm SHA256).Hash
            $secondHash = (Get-FileHash -LiteralPath $secondPath -Algorithm SHA256).Hash
            $trackedHash = (Get-FileHash -LiteralPath $trackedPath -Algorithm SHA256).Hash
            if ($firstHash -ne $secondHash) {
                throw "Resource regeneration is not deterministic for $arch ($firstHash != $secondHash)."
            }
            if ($firstHash -ne $trackedHash) {
                throw "Tracked $arch resource is stale ($trackedHash); regenerate it to $firstHash."
            }

            [pscustomobject]@{
                Architecture = $arch
                Deterministic = $true
                Tracked = $true
                SHA256 = $trackedHash
            }
        }
    } finally {
        Remove-VerifiedTempDirectory -Path $tempDirectory
    }
} finally {
    if (-not [string]::IsNullOrWhiteSpace($temporaryConfigPath) -and [System.IO.File]::Exists($temporaryConfigPath)) {
        [System.IO.File]::Delete($temporaryConfigPath)
    }
}
