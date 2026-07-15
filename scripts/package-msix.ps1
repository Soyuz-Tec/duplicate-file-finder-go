#requires -Version 5.1

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Version,

    [ValidateSet("amd64", "arm64", "all")]
    [string]$Architecture = "all",

    [Parameter(Mandatory = $true)][string]$InputDirectory,

    [Parameter(Mandatory = $true)][string]$OutputDirectory,

    [Parameter(Mandatory = $true)][string]$Publisher,

    [string]$ExpectedSignerThumbprint,

    [ValidateSet("fixed-certificate", "artifact-signing")]
    [string]$SignerVerificationMode = "fixed-certificate",

    [string]$ExpectedSignerIdentityEku,

    [ValidatePattern('^[A-Za-z0-9.-]{3,50}$')]
    [string]$PackageName = "KayilanInc.TwinTidy",

    [switch]$AllowTestSignatureWithoutTimestamp,

    [string]$MakeAppxPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "TwinTidy.Version.ps1")
. (Join-Path $PSScriptRoot "TwinTidy.Release.ps1")

$repoRoot = Split-Path -Parent $PSScriptRoot
$versionInfo = Resolve-TwinTidyVersion -Version $Version
if ($versionInfo.Canonical -ceq "dev") {
    throw "MSIX packages require an explicit release version."
}
if ([string]::IsNullOrWhiteSpace($versionInfo.MSIXVersion)) {
    throw "MSIX cannot represent SemVer major 65535 because its nonzero package major uses SemVer major plus one."
}
if ([string]::IsNullOrWhiteSpace($Publisher) -or $Publisher.Length -gt 8192 -or $Publisher -notmatch '(^|,\s*)CN=') {
    throw "Publisher must be the exact certificate subject distinguished name and include CN=."
}
if ($SignerVerificationMode -ceq "fixed-certificate") {
    $ExpectedSignerThumbprint = Normalize-TwinTidyCertificateThumbprint -Thumbprint $ExpectedSignerThumbprint
} elseif ([string]::IsNullOrWhiteSpace($ExpectedSignerIdentityEku)) {
    throw "Artifact Signing requires -ExpectedSignerIdentityEku."
}

$targets = if ($Architecture -ceq "all") { @("amd64", "arm64") } else { @($Architecture) }
$manifestTemplatePath = Join-Path $repoRoot "packaging\msix\AppxManifest.xml.template"
$assetDirectory = Join-Path $repoRoot "packaging\msix\Assets"
$licensePath = Join-Path $repoRoot "LICENSE"
$noticePath = Join-Path $repoRoot "THIRD_PARTY_NOTICES.txt"

foreach ($requiredPath in @($manifestTemplatePath, $licensePath, $noticePath)) {
    if (-not [System.IO.File]::Exists($requiredPath)) {
        throw "Required MSIX packaging input is missing: $requiredPath"
    }
}
$null = & (Join-Path $PSScriptRoot "generate-msix-assets.ps1") -Check

function Resolve-TwinTidyMakeAppx {
    param([string]$RequestedPath)

    if (-not [string]::IsNullOrWhiteSpace($RequestedPath)) {
        $resolved = [System.IO.Path]::GetFullPath($RequestedPath)
        if (-not [System.IO.File]::Exists($resolved)) {
            throw "MakeAppx.exe is missing: $resolved"
        }
        return $resolved
    }

    $kitsRoot = Join-Path ${env:ProgramFiles(x86)} "Windows Kits\10\bin"
    if ([System.IO.Directory]::Exists($kitsRoot)) {
        $candidates = @(Get-ChildItem -LiteralPath $kitsRoot -Filter "makeappx.exe" -File -Recurse -ErrorAction SilentlyContinue |
            Where-Object { $_.Directory.Name -in @("x64", "x86") } |
            Sort-Object -Property FullName -Descending)
        if ($candidates.Count -gt 0) {
            return $candidates[0].FullName
        }
    }
    throw "MakeAppx.exe was not found. Install the Windows 10 or Windows 11 SDK, or pass -MakeAppxPath."
}

function Write-TwinTidyMSIXManifest {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$IdentityName,
        [Parameter(Mandatory = $true)][string]$PackageVersion,
        [Parameter(Mandatory = $true)][string]$PackageArchitecture,
        [Parameter(Mandatory = $true)][string]$PackagePublisher
    )

    $document = Read-TwinTidyXmlDocument -Path $manifestTemplatePath
    $namespace = [System.Xml.XmlNamespaceManager]::new($document.NameTable)
    $namespace.AddNamespace("f", "http://schemas.microsoft.com/appx/manifest/foundation/windows10")
    $identity = $document.SelectSingleNode("/f:Package/f:Identity", $namespace)
    if ($null -eq $identity) {
        throw "MSIX manifest template is missing Package/Identity."
    }
    $identity.SetAttribute("Name", $IdentityName)
    $identity.SetAttribute("Publisher", $PackagePublisher)
    $identity.SetAttribute("Version", $PackageVersion)
    $identity.SetAttribute("ProcessorArchitecture", $PackageArchitecture)

    $settings = [System.Xml.XmlWriterSettings]::new()
    $settings.Encoding = [System.Text.UTF8Encoding]::new($false)
    $settings.Indent = $true
    $settings.NewLineChars = "`n"
    $settings.NewLineHandling = [System.Xml.NewLineHandling]::Replace
    $writer = [System.Xml.XmlWriter]::Create($Path, $settings)
    try {
        $document.Save($writer)
    } finally {
        $writer.Dispose()
    }
}

function Copy-TwinTidyLockedFile {
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$Destination
    )

    $input = [System.IO.File]::Open($Source, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
    try {
        $output = [System.IO.File]::Open($Destination, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
        try {
            $input.CopyTo($output)
            $output.Flush($true)
        } finally {
            $output.Dispose()
        }
    } finally {
        $input.Dispose()
    }
}

$makeAppx = Resolve-TwinTidyMakeAppx -RequestedPath $MakeAppxPath
[System.IO.Directory]::CreateDirectory($OutputDirectory) | Out-Null
$results = @()

foreach ($arch in $targets) {
    $input = Join-Path $InputDirectory "TwinTidy-$($versionInfo.Canonical)-windows-$arch"
    $executablePath = Join-Path $input "TwinTidy.exe"
    $receiptPath = Join-Path $input "TwinTidy.build-receipt.json"
    $provenancePath = Join-Path $input "TwinTidy.signed-provenance.json"
    foreach ($requiredPath in @($executablePath, $receiptPath, $provenancePath)) {
        if (-not [System.IO.File]::Exists($requiredPath)) {
            throw "Required signed MSIX input is missing: $requiredPath"
        }
    }

    $null = Assert-TwinTidyAuthenticodeSignature `
        -Path $executablePath `
        -ExpectedSignerSubject $Publisher `
        -ExpectedSignerThumbprint $ExpectedSignerThumbprint `
        -SignerVerificationMode $SignerVerificationMode `
        -ExpectedSignerIdentityEku $ExpectedSignerIdentityEku `
        -RequireTimestamp (-not $AllowTestSignatureWithoutTimestamp)

    $receipt = ConvertFrom-TwinTidyJson -Json (Get-Content -LiteralPath $receiptPath -Raw)
    $executableHash = Get-TwinTidyFileSHA256 -Path $executablePath
    $signedProvenance = ConvertFrom-TwinTidyJson -Json (Get-Content -LiteralPath $provenancePath -Raw)
    $signedProvenanceHash = Get-TwinTidyFileSHA256 -Path $provenancePath
    $unsignedHash = [string](Get-TwinTidyRequiredJsonProperty -Object $signedProvenance.unsigned -Name "sha256" -Context "signed provenance unsigned")
    $receiptHash = Get-TwinTidyFileSHA256 -Path $receiptPath
    $receiptBinding = Assert-TwinTidyBuildReceipt `
        -Receipt $receipt `
        -ExpectedVersion $versionInfo.Canonical `
        -ExpectedArchitecture $arch `
        -ExpectedSourceDate ([string]$receipt.sourceDate) `
        -ExpectedExecutableSHA256 $unsignedHash
    if (-not $AllowTestSignatureWithoutTimestamp) {
        $null = Assert-TwinTidySignedProvenance `
            -Provenance $signedProvenance `
            -ExpectedVersion $versionInfo.Canonical `
            -ExpectedArchitecture $arch `
            -ExpectedSourceDate ([string]$receipt.sourceDate) `
            -ExpectedCommit $receiptBinding.Commit `
            -ExpectedGitTree $receiptBinding.GitTree `
            -ExpectedSourceTreeSHA256 $receiptBinding.SourceTreeSHA256 `
            -ExpectedUnsignedExecutableSHA256 $unsignedHash `
            -ExpectedBuildReceiptSHA256 $receiptHash `
            -ExpectedSignedExecutableSHA256 $executableHash `
            -ExpectedSignerSubject $Publisher `
            -ExpectedSignerThumbprint $ExpectedSignerThumbprint `
            -SignerVerificationMode $SignerVerificationMode `
            -ExpectedSignerIdentityEku $ExpectedSignerIdentityEku
    } else {
        if ([string]$signedProvenance.schema -cne "twintidy.signed-provenance/v1" -or
            [string]$signedProvenance.product -cne "TwinTidy" -or
            [string]$signedProvenance.version -cne $versionInfo.Canonical -or
            [string]$signedProvenance.architecture -cne $arch -or
            [string]$signedProvenance.signed.sha256 -cne $executableHash -or
            [string]$signedProvenance.authenticode.signerSubject -cne $Publisher -or
            [string]$signedProvenance.authenticode.verificationMode -cne "fixed-certificate" -or
            (Normalize-TwinTidyCertificateThumbprint -Thumbprint ([string]$signedProvenance.authenticode.signerThumbprint)) -cne $ExpectedSignerThumbprint) {
            throw "Disposable MSIX lifecycle provenance does not bind the signed $arch executable and test certificate."
        }
    }

    $stage = Join-Path ([System.IO.Path]::GetTempPath()) ("TwinTidyMSIX-" + [System.Guid]::NewGuid().ToString("N"))
    [System.IO.Directory]::CreateDirectory($stage) | Out-Null
    try {
        $stageAssets = Join-Path $stage "Assets"
        $stageMetadata = Join-Path $stage "ReleaseMetadata"
        [System.IO.Directory]::CreateDirectory($stageAssets) | Out-Null
        [System.IO.Directory]::CreateDirectory($stageMetadata) | Out-Null
        $packageArchitecture = if ($arch -ceq "amd64") { "x64" } else { "arm64" }
        Write-TwinTidyMSIXManifest `
            -Path (Join-Path $stage "AppxManifest.xml") `
            -IdentityName $PackageName `
            -PackageVersion $versionInfo.MSIXVersion `
            -PackageArchitecture $packageArchitecture `
            -PackagePublisher $Publisher
        Copy-TwinTidyLockedFile -Source $executablePath -Destination (Join-Path $stage "TwinTidy.exe")
        Copy-TwinTidyLockedFile -Source $licensePath -Destination (Join-Path $stage "LICENSE")
        Copy-TwinTidyLockedFile -Source $noticePath -Destination (Join-Path $stage "THIRD_PARTY_NOTICES.txt")
        Copy-TwinTidyLockedFile -Source $receiptPath -Destination (Join-Path $stageMetadata "TwinTidy.build-receipt.json")
        Copy-TwinTidyLockedFile -Source $provenancePath -Destination (Join-Path $stageMetadata "TwinTidy.signed-provenance.json")
        foreach ($asset in @("Square44x44Logo.png", "Square150x150Logo.png", "StoreLogo.png")) {
            Copy-TwinTidyLockedFile -Source (Join-Path $assetDirectory $asset) -Destination (Join-Path $stageAssets $asset)
        }

        $packagePath = Join-Path $OutputDirectory "TwinTidy-$($versionInfo.Canonical)-windows-$arch.unsigned.msix"
        if ([System.IO.File]::Exists($packagePath)) {
            [System.IO.File]::Delete($packagePath)
        }
        # MakeAppx writes progress to stdout; keep it out of this script's pipeline
        # output, which callers consume as structured packaging results.
        & $makeAppx pack /d $stage /p $packagePath /o /h SHA256 | Out-Host
        if ($LASTEXITCODE -ne 0 -or -not [System.IO.File]::Exists($packagePath)) {
            throw "MakeAppx failed to create the unsigned $arch MSIX package."
        }
        $null = Assert-TwinTidyMSIXPackageBinding `
            -Path $packagePath `
            -ExpectedPackageName $PackageName `
            -ExpectedPublisher $Publisher `
            -ExpectedPackageVersion $versionInfo.MSIXVersion `
            -ExpectedArchitecture $packageArchitecture `
            -ExpectedExecutableSHA256 $executableHash `
            -ExpectedBuildReceiptSHA256 $receiptHash `
            -ExpectedSignedProvenanceSHA256 $signedProvenanceHash
        $results += [pscustomobject]@{
            Architecture = $arch
            Path = $packagePath
            SHA256 = Get-TwinTidyFileSHA256 -Path $packagePath
            ExecutableSHA256 = $executableHash
            BuildReceiptSHA256 = $receiptHash
            SignedProvenanceSHA256 = $signedProvenanceHash
            Publisher = $Publisher
            PackageName = $PackageName
            PackageVersion = $versionInfo.MSIXVersion
            Signed = $false
        }
    } finally {
        $fullStage = [System.IO.Path]::GetFullPath($stage)
        $systemTemp = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
        if (-not $systemTemp.EndsWith([System.IO.Path]::DirectorySeparatorChar.ToString())) {
            $systemTemp += [System.IO.Path]::DirectorySeparatorChar
        }
        if (-not $fullStage.StartsWith($systemTemp, [System.StringComparison]::OrdinalIgnoreCase) -or
            -not [System.IO.Path]::GetFileName($fullStage).StartsWith("TwinTidyMSIX-", [System.StringComparison]::Ordinal)) {
            throw "Refusing to remove unexpected MSIX staging directory: $fullStage"
        }
        if ([System.IO.Directory]::Exists($fullStage)) {
            [System.IO.Directory]::Delete($fullStage, $true)
        }
    }
}

$results
