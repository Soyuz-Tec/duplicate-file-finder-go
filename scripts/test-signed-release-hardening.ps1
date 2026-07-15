#requires -Version 5.1

[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "TwinTidy.Release.ps1")
. (Join-Path $PSScriptRoot "TwinTidy.Version.ps1")

$mappedVersion = Resolve-TwinTidyVersion -Version "0.1.0-beta.1"
if ($mappedVersion.PEVersion -cne "0.1.0.1" -or $mappedVersion.MSIXVersion -cne "1.1.0.1") {
    throw "TwinTidy SemVer to PE/MSIX mapping regression."
}
$unrepresentableMSIXVersion = Resolve-TwinTidyVersion -Version "65535.0.0"
if ($null -ne $unrepresentableMSIXVersion.MSIXVersion) {
    throw "SemVer major 65535 must not produce an MSIX version."
}

function Assert-Throws {
    param(
        [Parameter(Mandatory = $true)][scriptblock]$Action,
        [Parameter(Mandatory = $true)][string]$ExpectedMessage
    )

    try {
        & $Action
    } catch {
        if ($_.Exception.Message -notlike "*$ExpectedMessage*") {
            throw "Expected failure containing '$ExpectedMessage', received '$($_.Exception.Message)'."
        }
        return
    }
    throw "Expected failure containing '$ExpectedMessage', but the action succeeded."
}

function Remove-TestRoot {
    param([Parameter(Mandatory = $true)][string]$Path)

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    if (-not [System.IO.Path]::GetFileName($fullPath).StartsWith("TwinTidySignedTests-", [System.StringComparison]::Ordinal)) {
        throw "Refusing to remove unexpected test directory: $fullPath"
    }
    if ([System.IO.Directory]::Exists($fullPath)) {
        [System.IO.Directory]::Delete($fullPath, $true)
    }
}

$signerSubject = "CN=Kayilan Inc, O=Kayilan Inc"
$signerRawData = [System.Text.Encoding]::ASCII.GetBytes("TwinTidy signer certificate fixture")
$timestampRawData = [System.Text.Encoding]::ASCII.GetBytes("TwinTidy timestamp certificate fixture")
$signerThumbprint = Get-TwinTidyCertificateSHA256 -Certificate ([pscustomobject]@{ RawData = $signerRawData })
$timestampThumbprint = Get-TwinTidyCertificateSHA256 -Certificate ([pscustomobject]@{ RawData = $timestampRawData })
$validSignature = [pscustomobject]@{
    Status = "Valid"
    StatusMessage = "Signature verified."
    SignerCertificate = [pscustomobject]@{
        Subject = $signerSubject
        RawData = $signerRawData
        NotBefore = [datetime]"2026-01-01T00:00:00Z"
        NotAfter = [datetime]"2027-01-01T00:00:00Z"
    }
    TimeStamperCertificate = [pscustomobject]@{
        Subject = "CN=Test Timestamp Authority"
        RawData = $timestampRawData
    }
}

$signatureBinding = Assert-TwinTidyAuthenticodeSignature `
    -Path "C:\policy-test\TwinTidy.exe" `
    -ExpectedSignerSubject $signerSubject `
    -ExpectedSignerThumbprint $signerThumbprint `
    -RequireTimestamp $true `
    -Signature $validSignature
if ($signatureBinding.SignerThumbprint -cne $signerThumbprint -or -not $signatureBinding.Timestamped) {
    throw "Valid signature policy binding was not returned."
}

Assert-Throws -ExpectedMessage "does not match expected" -Action {
    $null = Assert-TwinTidyAuthenticodeSignature `
        -Path "C:\policy-test\TwinTidy.exe" `
        -ExpectedSignerSubject "CN=Different Publisher" `
        -ExpectedSignerThumbprint $signerThumbprint `
        -Signature $validSignature
}
Assert-Throws -ExpectedMessage "does not match expected" -Action {
    $null = Assert-TwinTidyAuthenticodeSignature `
        -Path "C:\policy-test\TwinTidy.exe" `
        -ExpectedSignerSubject $signerSubject `
        -ExpectedSignerThumbprint ("C" * 64) `
        -Signature $validSignature
}
$notSigned = $validSignature.PSObject.Copy()
$notSigned.Status = "NotSigned"
Assert-Throws -ExpectedMessage "is not valid" -Action {
    $null = Assert-TwinTidyAuthenticodeSignature `
        -Path "C:\policy-test\TwinTidy.exe" `
        -ExpectedSignerSubject $signerSubject `
        -ExpectedSignerThumbprint $signerThumbprint `
        -Signature $notSigned
}
$notTimestamped = $validSignature.PSObject.Copy()
$notTimestamped.TimeStamperCertificate = $null
Assert-Throws -ExpectedMessage "is not timestamped" -Action {
    $null = Assert-TwinTidyAuthenticodeSignature `
        -Path "C:\policy-test\TwinTidy.exe" `
        -ExpectedSignerSubject $signerSubject `
        -ExpectedSignerThumbprint $signerThumbprint `
        -Signature $notTimestamped
}

$artifactIdentityEku = "1.3.6.1.4.1.311.97.990309390.766961637.194916062.941502583"
$artifactEkus = [System.Security.Cryptography.OidCollection]::new()
[void]$artifactEkus.Add([System.Security.Cryptography.Oid]::new("1.3.6.1.5.5.7.3.3"))
[void]$artifactEkus.Add([System.Security.Cryptography.Oid]::new("1.3.6.1.4.1.311.97.1.0"))
[void]$artifactEkus.Add([System.Security.Cryptography.Oid]::new($artifactIdentityEku))
$artifactExtension = [System.Security.Cryptography.X509Certificates.X509EnhancedKeyUsageExtension]::new($artifactEkus, $false)
$artifactSignature = $validSignature.PSObject.Copy()
$artifactSignature.SignerCertificate = [pscustomobject]@{
    Subject = $signerSubject
    RawData = [System.Text.Encoding]::ASCII.GetBytes("rotating Artifact Signing certificate fixture")
    Extensions = @($artifactExtension)
    NotBefore = [datetime]"2026-07-12T00:00:00Z"
    NotAfter = [datetime]"2026-07-15T00:00:00Z"
}
$artifactBinding = Assert-TwinTidyAuthenticodeSignature `
    -Path "C:\policy-test\TwinTidy.exe" `
    -ExpectedSignerSubject $signerSubject `
    -SignerVerificationMode artifact-signing `
    -ExpectedSignerIdentityEku $artifactIdentityEku `
    -RequireTimestamp $true `
    -Signature $artifactSignature
if ($artifactBinding.SignerIdentityEku -cne $artifactIdentityEku -or $artifactBinding.VerificationMode -cne "artifact-signing") {
    throw "Artifact Signing durable identity policy was not returned."
}
Assert-Throws -ExpectedMessage "missing required EKU" -Action {
    $null = Assert-TwinTidyAuthenticodeSignature `
        -Path "C:\policy-test\TwinTidy.exe" `
        -ExpectedSignerSubject $signerSubject `
        -SignerVerificationMode artifact-signing `
        -ExpectedSignerIdentityEku "1.3.6.1.4.1.311.97.1.2.3.4" `
        -RequireTimestamp $true `
        -Signature $artifactSignature
}

$unsignedHash = "0" * 64
$receiptHash = "1" * 64
$signedHash = "2" * 64
$provenance = [pscustomobject]@{
    schema = "twintidy.signed-provenance/v1"
    product = "TwinTidy"
    version = "1.2.3-beta.1"
    architecture = "amd64"
    sourceDate = "2026-07-12T00:00:00-04:00"
    source = [pscustomobject]@{
        commit = "a" * 40
        gitTree = "b" * 40
        treeSHA256 = "c" * 64
    }
    unsigned = [pscustomobject]@{
        path = "TwinTidy.exe"
        sha256 = $unsignedHash
        size = 100
        buildReceiptSHA256 = $receiptHash
    }
    signed = [pscustomobject]@{
        path = "TwinTidy.exe"
        sha256 = $signedHash
        size = 200
    }
    authenticode = [pscustomobject]@{
        status = "Valid"
        signerSubject = $signerSubject
        signerThumbprint = $signerThumbprint
        verificationMode = "fixed-certificate"
        signerIdentityEku = $null
        timestamped = $true
        timestampSubject = "CN=Test Timestamp Authority"
        timestampThumbprint = $timestampThumbprint
    }
}
$null = Assert-TwinTidySignedProvenance `
    -Provenance $provenance `
    -ExpectedVersion "1.2.3-beta.1" `
    -ExpectedArchitecture "amd64" `
    -ExpectedSourceDate "2026-07-12T00:00:00-04:00" `
    -ExpectedCommit ("a" * 40) `
    -ExpectedGitTree ("b" * 40) `
    -ExpectedSourceTreeSHA256 ("c" * 64) `
    -ExpectedUnsignedExecutableSHA256 $unsignedHash `
    -ExpectedBuildReceiptSHA256 $receiptHash `
    -ExpectedSignedExecutableSHA256 $signedHash `
    -ExpectedSignerSubject $signerSubject `
    -ExpectedSignerThumbprint $signerThumbprint

$tamperedProvenance = $provenance.PSObject.Copy()
$tamperedProvenance.signed = $provenance.signed.PSObject.Copy()
$tamperedProvenance.signed.sha256 = "3" * 64
Assert-Throws -ExpectedMessage "does not bind the expected signed executable" -Action {
    $null = Assert-TwinTidySignedProvenance `
        -Provenance $tamperedProvenance `
        -ExpectedVersion "1.2.3-beta.1" `
        -ExpectedArchitecture "amd64" `
        -ExpectedSourceDate "2026-07-12T00:00:00-04:00" `
        -ExpectedCommit ("a" * 40) `
        -ExpectedGitTree ("b" * 40) `
        -ExpectedSourceTreeSHA256 ("c" * 64) `
        -ExpectedUnsignedExecutableSHA256 $unsignedHash `
        -ExpectedBuildReceiptSHA256 $receiptHash `
        -ExpectedSignedExecutableSHA256 $signedHash `
        -ExpectedSignerSubject $signerSubject `
        -ExpectedSignerThumbprint $signerThumbprint
}

$testRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("TwinTidySignedTests-" + [System.Guid]::NewGuid().ToString("N"))
try {
    $inputRoot = Join-Path $testRoot "input"
    $outputRoot = Join-Path $testRoot "output"
    $artifactRoot = Join-Path $inputRoot "TwinTidy-1.2.3-beta.1-windows-amd64"
    [System.IO.Directory]::CreateDirectory($artifactRoot) | Out-Null
    $unsignedPath = Join-Path $artifactRoot "TwinTidy.exe"
    $receiptPath = Join-Path $artifactRoot "TwinTidy.build-receipt.json"
    [System.IO.File]::WriteAllBytes($unsignedPath, [System.Text.Encoding]::ASCII.GetBytes("unsigned TwinTidy test executable"))
    $actualUnsignedHash = Get-TwinTidyFileSHA256 -Path $unsignedPath
    $actualUnsignedSize = [System.IO.FileInfo]::new($unsignedPath).Length
    $sourceDate = "2026-07-12T00:00:00-04:00"
    $receipt = [ordered]@{
        schema = "twintidy.build-receipt/v1"
        product = "TwinTidy"
        version = "1.2.3-beta.1"
        architecture = "amd64"
        sourceDate = $sourceDate
        source = [ordered]@{
            kind = "git-commit"
            commit = "a" * 40
            gitTree = "b" * 40
            clean = $true
            treeDigestAlgorithm = "sha256-path-length-v1"
            treeSHA256 = "c" * 64
            fileCount = 1
        }
        build = [ordered]@{
            goVersion = "go-test"
            goos = "windows"
            goarch = "amd64"
            cgoEnabled = $false
            trimpath = $true
            buildVCS = $false
            resourceSHA256 = "d" * 64
        }
        executable = [ordered]@{
            path = "TwinTidy.exe"
            sha256 = $actualUnsignedHash
            size = $actualUnsignedSize
        }
    }
    [System.IO.File]::WriteAllText($receiptPath, (($receipt | ConvertTo-Json -Depth 8) + "`n"), [System.Text.UTF8Encoding]::new($false))
    $actualReceiptHash = Get-TwinTidyFileSHA256 -Path $receiptPath
    $adapterPath = Join-Path $testRoot "fake-signer.ps1"
    $adapterSource = @'
param([string]$InputPath, [string]$OutputPath)
[System.IO.File]::Copy($InputPath, $OutputPath, $false)
$stream = [System.IO.File]::Open($OutputPath, [System.IO.FileMode]::Append, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
try { $stream.WriteByte(1) } finally { $stream.Dispose() }
'@
    [System.IO.File]::WriteAllText($adapterPath, $adapterSource, [System.Text.UTF8Encoding]::new($false))

    Assert-Throws -ExpectedMessage "Authenticode signature" -Action {
        & (Join-Path $PSScriptRoot "sign-release.ps1") `
            -Version "1.2.3-beta.1" `
            -Architecture amd64 `
            -InputDirectory $inputRoot `
            -OutputDirectory $outputRoot `
            -SourceDate $sourceDate `
            -ExpectedCommit ("a" * 40) `
            -ExpectedGitTree ("b" * 40) `
            -ExpectedSourceTreeSHA256 ("c" * 64) `
            -SignerAdapter $adapterPath `
            -ExpectedSignerSubject $signerSubject `
            -ExpectedSignerThumbprint $signerThumbprint `
            -ExpectedUnsignedExecutableSHA256 @{ amd64 = $actualUnsignedHash } `
            -ExpectedBuildReceiptSHA256 @{ amd64 = $actualReceiptHash }
    }
    if ((Get-TwinTidyFileSHA256 -Path $unsignedPath) -cne $actualUnsignedHash -or
        (Get-TwinTidyFileSHA256 -Path $receiptPath) -cne $actualReceiptHash) {
        throw "Failed signing attempt mutated immutable unsigned inputs."
    }
    $publishedArtifact = Join-Path $outputRoot "TwinTidy-1.2.3-beta.1-windows-amd64"
    if ([System.IO.Directory]::Exists($publishedArtifact)) {
        throw "Failed signing attempt published an artifact."
    }

    $msixPath = Join-Path $testRoot "TwinTidy-test.unsigned.msix"
    $msixExecutableBytes = [System.Text.Encoding]::ASCII.GetBytes("signed executable fixture")
    $msixReceiptBytes = [System.Text.Encoding]::UTF8.GetBytes("{`"schema`":`"receipt-fixture`"}`n")
    $msixProvenanceBytes = [System.Text.Encoding]::UTF8.GetBytes("{`"schema`":`"provenance-fixture`"}`n")
    $manifestBytes = [System.Text.Encoding]::UTF8.GetBytes(@'
<?xml version="1.0" encoding="utf-8"?>
<Package xmlns="http://schemas.microsoft.com/appx/manifest/foundation/windows10">
  <Identity Name="KayilanInc.TwinTidy" Publisher="CN=Kayilan Inc, O=Kayilan Inc" Version="1.2.3.1" ProcessorArchitecture="x64" />
  <Applications><Application Id="TwinTidy" Executable="TwinTidy.exe" EntryPoint="Windows.FullTrustApplication" /></Applications>
</Package>
'@)
    $fixtureEntries = [ordered]@{
        "[Content_Types].xml" = [System.Text.Encoding]::UTF8.GetBytes("<Types />")
        "AppxBlockMap.xml" = [System.Text.Encoding]::UTF8.GetBytes("<BlockMap />")
        "AppxManifest.xml" = $manifestBytes
        "Assets/Square44x44Logo.png" = [byte[]]@(1)
        "Assets/Square150x150Logo.png" = [byte[]]@(2)
        "Assets/StoreLogo.png" = [byte[]]@(3)
        "LICENSE" = [System.Text.Encoding]::ASCII.GetBytes("MIT")
        "ReleaseMetadata/TwinTidy.build-receipt.json" = $msixReceiptBytes
        "ReleaseMetadata/TwinTidy.signed-provenance.json" = $msixProvenanceBytes
        "THIRD_PARTY_NOTICES.txt" = [System.Text.Encoding]::ASCII.GetBytes("notices")
        "TwinTidy.exe" = $msixExecutableBytes
    }
    Add-Type -AssemblyName System.IO.Compression
    $msixStream = [System.IO.File]::Open($msixPath, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
    try {
        $msixArchive = [System.IO.Compression.ZipArchive]::new($msixStream, [System.IO.Compression.ZipArchiveMode]::Create, $false)
        try {
            foreach ($fixture in $fixtureEntries.GetEnumerator()) {
                $entry = $msixArchive.CreateEntry($fixture.Key)
                $entryStream = $entry.Open()
                try {
                    $entryStream.Write($fixture.Value, 0, $fixture.Value.Length)
                } finally {
                    $entryStream.Dispose()
                }
            }
        } finally {
            $msixArchive.Dispose()
        }
    } finally {
        $msixStream.Dispose()
    }
    $hashAlgorithm = [System.Security.Cryptography.SHA256]::Create()
    try {
        $msixExecutableHash = ConvertTo-TwinTidyHex -Bytes $hashAlgorithm.ComputeHash($msixExecutableBytes)
        $msixReceiptHash = ConvertTo-TwinTidyHex -Bytes $hashAlgorithm.ComputeHash($msixReceiptBytes)
        $msixProvenanceHash = ConvertTo-TwinTidyHex -Bytes $hashAlgorithm.ComputeHash($msixProvenanceBytes)
    } finally {
        $hashAlgorithm.Dispose()
    }
    $null = Assert-TwinTidyMSIXPackageBinding `
        -Path $msixPath `
        -ExpectedPackageName "KayilanInc.TwinTidy" `
        -ExpectedPublisher $signerSubject `
        -ExpectedPackageVersion "1.2.3.1" `
        -ExpectedArchitecture x64 `
        -ExpectedExecutableSHA256 $msixExecutableHash `
        -ExpectedBuildReceiptSHA256 $msixReceiptHash `
        -ExpectedSignedProvenanceSHA256 $msixProvenanceHash
    Assert-Throws -ExpectedMessage "TwinTidy.exe' SHA-256" -Action {
        $null = Assert-TwinTidyMSIXPackageBinding `
            -Path $msixPath `
            -ExpectedPackageName "KayilanInc.TwinTidy" `
            -ExpectedPublisher $signerSubject `
            -ExpectedPackageVersion "1.2.3.1" `
            -ExpectedArchitecture x64 `
            -ExpectedExecutableSHA256 ("f" * 64) `
            -ExpectedBuildReceiptSHA256 $msixReceiptHash `
            -ExpectedSignedProvenanceSHA256 $msixProvenanceHash
    }
    Assert-Throws -ExpectedMessage "manifest identity" -Action {
        $null = Assert-TwinTidyMSIXPackageBinding `
            -Path $msixPath `
            -ExpectedPackageName "KayilanInc.TwinTidy" `
            -ExpectedPublisher "CN=Different Publisher" `
            -ExpectedPackageVersion "1.2.3.1" `
            -ExpectedArchitecture x64 `
            -ExpectedExecutableSHA256 $msixExecutableHash `
            -ExpectedBuildReceiptSHA256 $msixReceiptHash `
            -ExpectedSignedProvenanceSHA256 $msixProvenanceHash
    }
    Assert-Throws -ExpectedMessage "manifest identity" -Action {
        $null = Assert-TwinTidyMSIXPackageBinding `
            -Path $msixPath `
            -ExpectedPackageName "KayilanInc.TwinTidy" `
            -ExpectedPublisher $signerSubject `
            -ExpectedPackageVersion "1.2.3.1" `
            -ExpectedArchitecture arm64 `
            -ExpectedExecutableSHA256 $msixExecutableHash `
            -ExpectedBuildReceiptSHA256 $msixReceiptHash `
            -ExpectedSignedProvenanceSHA256 $msixProvenanceHash
    }
    Assert-Throws -ExpectedMessage "AppxSignature.p7x" -Action {
        $null = Assert-TwinTidyMSIXPackageBinding `
            -Path $msixPath `
            -ExpectedPackageName "KayilanInc.TwinTidy" `
            -ExpectedPublisher $signerSubject `
            -ExpectedPackageVersion "1.2.3.1" `
            -ExpectedArchitecture x64 `
            -ExpectedExecutableSHA256 $msixExecutableHash `
            -ExpectedBuildReceiptSHA256 $msixReceiptHash `
            -ExpectedSignedProvenanceSHA256 $msixProvenanceHash `
            -RequirePackageSignature
    }
} finally {
    Remove-TestRoot -Path $testRoot
}

[pscustomobject]@{
    SignaturePolicyTests = "passed"
    VersionMappingTests = "passed"
    ProvenanceBindingTests = "passed"
    MSIXBindingTests = "passed"
    UnsignedInputImmutabilityTest = "passed"
    UnsignedSignerRejectionTest = "passed"
}
