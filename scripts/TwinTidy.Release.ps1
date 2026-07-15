#requires -Version 5.1

Set-StrictMode -Version Latest

function ConvertTo-TwinTidyHex {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)

    return ([System.BitConverter]::ToString($Bytes)).Replace("-", "").ToLowerInvariant()
}

function Get-TwinTidyStreamSHA256 {
    param([Parameter(Mandatory = $true)][System.IO.Stream]$Stream)

    if (-not $Stream.CanRead) {
        throw "Cannot hash an unreadable stream."
    }

    $originalPosition = $null
    if ($Stream.CanSeek) {
        $originalPosition = $Stream.Position
        $Stream.Position = 0
    }

    $algorithm = [System.Security.Cryptography.SHA256]::Create()
    try {
        return ConvertTo-TwinTidyHex -Bytes $algorithm.ComputeHash($Stream)
    } finally {
        $algorithm.Dispose()
        if ($null -ne $originalPosition) {
            $Stream.Position = $originalPosition
        }
    }
}

function Get-TwinTidyFileSHA256 {
    param([Parameter(Mandatory = $true)][string]$Path)

    $stream = [System.IO.File]::Open(
        [System.IO.Path]::GetFullPath($Path),
        [System.IO.FileMode]::Open,
        [System.IO.FileAccess]::Read,
        [System.IO.FileShare]::Read
    )
    try {
        return Get-TwinTidyStreamSHA256 -Stream $stream
    } finally {
        $stream.Dispose()
    }
}

function Read-TwinTidyXmlDocument {
    param([Parameter(Mandatory = $true)][string]$Path)

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    if (-not [System.IO.File]::Exists($fullPath)) {
        throw "XML file is missing: $fullPath"
    }

    $settings = [System.Xml.XmlReaderSettings]::new()
    $settings.DtdProcessing = [System.Xml.DtdProcessing]::Prohibit
    $settings.XmlResolver = $null
    $reader = [System.Xml.XmlReader]::Create($fullPath, $settings)
    try {
        $document = [System.Xml.XmlDocument]::new()
        $document.XmlResolver = $null
        $document.PreserveWhitespace = $true
        $document.Load($reader)
        return $document
    } catch {
        throw "Invalid or unsafe XML in '$fullPath': $($_.Exception.Message)"
    } finally {
        $reader.Dispose()
    }
}

function Assert-TwinTidyManifestPolicy {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Path)

    $document = Read-TwinTidyXmlDocument -Path $Path
    $allExecutionLevels = @($document.SelectNodes("//*[local-name()='requestedExecutionLevel']"))
    if ($allExecutionLevels.Count -ne 1) {
        throw "Manifest '$Path' must contain exactly one requestedExecutionLevel element; found $($allExecutionLevels.Count)."
    }

    $namespaceManager = [System.Xml.XmlNamespaceManager]::new($document.NameTable)
    $namespaceManager.AddNamespace("asm1", "urn:schemas-microsoft-com:asm.v1")
    $namespaceManager.AddNamespace("asm3", "urn:schemas-microsoft-com:asm.v3")
    $activeExecutionLevels = @($document.SelectNodes(
        "/asm1:assembly/asm3:trustInfo/asm3:security/asm3:requestedPrivileges/asm3:requestedExecutionLevel",
        $namespaceManager
    ))
    if ($activeExecutionLevels.Count -ne 1 -or -not [object]::ReferenceEquals($allExecutionLevels[0], $activeExecutionLevels[0])) {
        throw "Manifest '$Path' does not contain exactly one active requestedExecutionLevel in the Windows trustInfo policy path."
    }

    $executionLevel = [System.Xml.XmlElement]$activeExecutionLevels[0]
    if ($executionLevel.GetAttribute("level") -cne "asInvoker") {
        throw "Manifest '$Path' must request level='asInvoker'."
    }
    if ($executionLevel.GetAttribute("uiAccess") -cne "false") {
        throw "Manifest '$Path' must set uiAccess='false'."
    }

    foreach ($attribute in @($executionLevel.Attributes)) {
        if ($attribute.NamespaceURI -ne "" -or $attribute.LocalName -notin @("level", "uiAccess")) {
            throw "Manifest '$Path' has an unexpected requestedExecutionLevel attribute '$($attribute.Name)'."
        }
    }
    if ($executionLevel.Attributes.Count -ne 2) {
        throw "Manifest '$Path' must define only level and uiAccess on requestedExecutionLevel."
    }

    return [pscustomobject]@{
        Path = [System.IO.Path]::GetFullPath($Path)
        RequestedExecutionLevelCount = 1
        Level = "asInvoker"
        UIAccess = $false
    }
}

function Assert-TwinTidyReleaseSourceClean {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$RepositoryRoot)

    $repository = [System.IO.Path]::GetFullPath($RepositoryRoot)
    Push-Location $repository
    try {
        $inside = (& git rev-parse --is-inside-work-tree 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -ne 0 -or $inside -cne "true") {
            throw "Release source is not a Git working tree: $repository"
        }

        $status = @(& git status --porcelain=v1 --untracked-files=all --)
        if ($LASTEXITCODE -ne 0) {
            throw "Unable to inspect release source state."
        }
        if ($status.Count -gt 0) {
            $preview = ($status | Select-Object -First 20) -join "`n"
            throw "Release source must be clean. Modified, staged, deleted, renamed, or untracked files are not buildable release input:`n$preview"
        }
    } finally {
        Pop-Location
    }

    return [pscustomobject]@{
        RepositoryRoot = $repository
        Clean = $true
    }
}

function Resolve-TwinTidyGitSourceIdentity {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [string]$Commit = "HEAD",
        [string]$SourceDate
    )

    $repository = [System.IO.Path]::GetFullPath($RepositoryRoot)
    Push-Location $repository
    try {
        $resolvedCommit = (& git rev-parse --verify "$Commit^{commit}" 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -ne 0 -or $resolvedCommit -notmatch '^[0-9a-fA-F]{40,64}$') {
            throw "Unable to resolve source commit '$Commit'."
        }
        $resolvedCommit = $resolvedCommit.ToLowerInvariant()

        $headCommit = (& git rev-parse --verify "HEAD^{commit}" 2>&1 | Out-String).Trim().ToLowerInvariant()
        if ($LASTEXITCODE -ne 0) {
            throw "Unable to resolve HEAD."
        }

        $gitTree = (& git show -s --format=%T $resolvedCommit 2>&1 | Out-String).Trim().ToLowerInvariant()
        if ($LASTEXITCODE -ne 0 -or $gitTree -notmatch '^[0-9a-f]{40,64}$') {
            throw "Unable to resolve the source tree for commit '$resolvedCommit'."
        }

        $commitSourceDate = (& git show -s --format=%cI $resolvedCommit 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($commitSourceDate)) {
            throw "Unable to resolve the source date for commit '$resolvedCommit'."
        }
        if (-not [string]::IsNullOrWhiteSpace($SourceDate) -and $SourceDate -cne $commitSourceDate) {
            throw "Claimed source date '$SourceDate' does not match commit '$resolvedCommit' source date '$commitSourceDate'."
        }

        return [pscustomobject]@{
            Commit = $resolvedCommit
            HeadCommit = $headCommit
            GitTree = $gitTree
            SourceDate = $commitSourceDate
        }
    } finally {
        Pop-Location
    }
}

function Get-TwinTidySourceTreeDigest {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$SourceRoot)

    $root = [System.IO.Path]::GetFullPath($SourceRoot)
    if (-not [System.IO.Directory]::Exists($root)) {
        throw "Source tree is missing: $root"
    }
    if (-not $root.EndsWith([System.IO.Path]::DirectorySeparatorChar.ToString())) {
        $rootPrefix = $root + [System.IO.Path]::DirectorySeparatorChar
    } else {
        $rootPrefix = $root
    }

    $relativePaths = [System.Collections.Generic.List[string]]::new()
    foreach ($path in [System.IO.Directory]::EnumerateFiles($root, "*", [System.IO.SearchOption]::AllDirectories)) {
        $fullPath = [System.IO.Path]::GetFullPath($path)
        if (-not $fullPath.StartsWith($rootPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw "Source file escapes the source root: $fullPath"
        }
        $relativePaths.Add($fullPath.Substring($rootPrefix.Length).Replace('\', '/'))
    }
    $paths = $relativePaths.ToArray()
    [System.Array]::Sort($paths, [System.StringComparer]::Ordinal)

    $builder = [System.Text.StringBuilder]::new()
    $utf8 = [System.Text.UTF8Encoding]::new($false, $true)
    foreach ($relativePath in $paths) {
        $fullPath = Join-Path $root ($relativePath.Replace('/', [System.IO.Path]::DirectorySeparatorChar))
        $info = [System.IO.FileInfo]::new($fullPath)
        $pathByteCount = $utf8.GetByteCount($relativePath)
        $fileHash = Get-TwinTidyFileSHA256 -Path $fullPath
        [void]$builder.Append($pathByteCount)
        [void]$builder.Append(':')
        [void]$builder.Append($relativePath)
        [void]$builder.Append([char]0)
        [void]$builder.Append($info.Length)
        [void]$builder.Append([char]0)
        [void]$builder.Append($fileHash)
        [void]$builder.Append("`n")
    }

    $algorithm = [System.Security.Cryptography.SHA256]::Create()
    try {
        $digest = ConvertTo-TwinTidyHex -Bytes $algorithm.ComputeHash($utf8.GetBytes($builder.ToString()))
    } finally {
        $algorithm.Dispose()
    }

    return [pscustomobject]@{
        Algorithm = "sha256-path-length-v1"
        SHA256 = $digest
        FileCount = $paths.Count
    }
}

function Get-TwinTidyRequiredJsonProperty {
    param(
        [Parameter(Mandatory = $true)]$Object,
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$Context
    )

    if ($null -eq $Object -or $Object.PSObject.Properties.Name -notcontains $Name) {
        throw "$Context is missing required property '$Name'."
    }
    return $Object.$Name
}

function ConvertFrom-TwinTidyJson {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Json)

    $command = Get-Command ConvertFrom-Json -CommandType Cmdlet
    if ($command.Parameters.ContainsKey("DateKind")) {
        return $Json | ConvertFrom-Json -DateKind String
    }
    return $Json | ConvertFrom-Json
}

function Assert-TwinTidyBuildReceipt {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]$Receipt,
        [Parameter(Mandatory = $true)][string]$ExpectedVersion,
        [Parameter(Mandatory = $true)][string]$ExpectedArchitecture,
        [Parameter(Mandatory = $true)][string]$ExpectedSourceDate,
        [string]$ExpectedExecutableSHA256
    )

    $context = "TwinTidy build receipt"
    $schema = Get-TwinTidyRequiredJsonProperty -Object $Receipt -Name "schema" -Context $context
    $product = Get-TwinTidyRequiredJsonProperty -Object $Receipt -Name "product" -Context $context
    $version = Get-TwinTidyRequiredJsonProperty -Object $Receipt -Name "version" -Context $context
    $architecture = Get-TwinTidyRequiredJsonProperty -Object $Receipt -Name "architecture" -Context $context
    $sourceDate = Get-TwinTidyRequiredJsonProperty -Object $Receipt -Name "sourceDate" -Context $context
    $source = Get-TwinTidyRequiredJsonProperty -Object $Receipt -Name "source" -Context $context
    $build = Get-TwinTidyRequiredJsonProperty -Object $Receipt -Name "build" -Context $context
    $executable = Get-TwinTidyRequiredJsonProperty -Object $Receipt -Name "executable" -Context $context

    if ($schema -cne "twintidy.build-receipt/v1") { throw "Unsupported TwinTidy build receipt schema '$schema'." }
    if ($product -cne "TwinTidy") { throw "Build receipt product '$product' is not TwinTidy." }
    if ($version -cne $ExpectedVersion) { throw "Build receipt version '$version' does not match '$ExpectedVersion'." }
    if ($architecture -cne $ExpectedArchitecture) { throw "Build receipt architecture '$architecture' does not match '$ExpectedArchitecture'." }
    if ($sourceDate -cne $ExpectedSourceDate) { throw "Build receipt source date '$sourceDate' does not match '$ExpectedSourceDate'." }

    $sourceKind = Get-TwinTidyRequiredJsonProperty -Object $source -Name "kind" -Context "$context source"
    $sourceCommit = Get-TwinTidyRequiredJsonProperty -Object $source -Name "commit" -Context "$context source"
    $sourceGitTree = Get-TwinTidyRequiredJsonProperty -Object $source -Name "gitTree" -Context "$context source"
    $sourceClean = Get-TwinTidyRequiredJsonProperty -Object $source -Name "clean" -Context "$context source"
    $treeDigestAlgorithm = Get-TwinTidyRequiredJsonProperty -Object $source -Name "treeDigestAlgorithm" -Context "$context source"
    $sourceTreeSHA256 = Get-TwinTidyRequiredJsonProperty -Object $source -Name "treeSHA256" -Context "$context source"
    $sourceFileCount = Get-TwinTidyRequiredJsonProperty -Object $source -Name "fileCount" -Context "$context source"
    if ($sourceKind -notin @("git-commit", "working-tree")) { throw "Unknown build receipt source kind '$sourceKind'." }
    if ($sourceCommit -notmatch '^[0-9a-f]{40,64}$') { throw "Build receipt has an invalid source commit." }
    if ($sourceGitTree -notmatch '^[0-9a-f]{40,64}$') { throw "Build receipt has an invalid Git tree identity." }
    if ($sourceClean -isnot [bool]) { throw "Build receipt source clean flag is not Boolean." }
    if ($treeDigestAlgorithm -cne "sha256-path-length-v1") { throw "Build receipt has an unknown source-tree digest algorithm '$treeDigestAlgorithm'." }
    if ($sourceTreeSHA256 -notmatch '^[0-9a-f]{64}$') { throw "Build receipt has an invalid source-tree SHA-256." }
    if ([int64]$sourceFileCount -le 0) { throw "Build receipt has an invalid source-file count." }

    $goVersion = Get-TwinTidyRequiredJsonProperty -Object $build -Name "goVersion" -Context "$context build"
    $goos = Get-TwinTidyRequiredJsonProperty -Object $build -Name "goos" -Context "$context build"
    $goarch = Get-TwinTidyRequiredJsonProperty -Object $build -Name "goarch" -Context "$context build"
    $cgoEnabled = Get-TwinTidyRequiredJsonProperty -Object $build -Name "cgoEnabled" -Context "$context build"
    $trimpath = Get-TwinTidyRequiredJsonProperty -Object $build -Name "trimpath" -Context "$context build"
    $buildVCS = Get-TwinTidyRequiredJsonProperty -Object $build -Name "buildVCS" -Context "$context build"
    $resourceSHA256 = Get-TwinTidyRequiredJsonProperty -Object $build -Name "resourceSHA256" -Context "$context build"
    if ([string]::IsNullOrWhiteSpace([string]$goVersion)) { throw "Build receipt Go version is empty." }
    if ($goos -cne "windows") { throw "Build receipt target OS '$goos' is not windows." }
    if ($goarch -cne $ExpectedArchitecture) { throw "Build receipt target architecture '$goarch' does not match '$ExpectedArchitecture'." }
    if ($cgoEnabled -isnot [bool] -or $cgoEnabled) { throw "Build receipt must record CGO as disabled." }
    if ($trimpath -isnot [bool] -or -not $trimpath) { throw "Build receipt must record trimpath as enabled." }
    if ($buildVCS -isnot [bool] -or $buildVCS) { throw "Build receipt must record automatic VCS stamping as disabled." }
    if ($resourceSHA256 -notmatch '^[0-9a-f]{64}$') { throw "Build receipt has an invalid resource SHA-256." }

    $executablePath = Get-TwinTidyRequiredJsonProperty -Object $executable -Name "path" -Context "$context executable"
    $executableSHA256 = Get-TwinTidyRequiredJsonProperty -Object $executable -Name "sha256" -Context "$context executable"
    $executableSize = Get-TwinTidyRequiredJsonProperty -Object $executable -Name "size" -Context "$context executable"
    if ($executablePath -cne "TwinTidy.exe") { throw "Build receipt executable path '$executablePath' is not TwinTidy.exe." }
    if ($executableSHA256 -notmatch '^[0-9a-f]{64}$') { throw "Build receipt has an invalid executable SHA-256." }
    if ([int64]$executableSize -le 0) { throw "Build receipt has an invalid executable size." }
    if (-not [string]::IsNullOrWhiteSpace($ExpectedExecutableSHA256) -and $executableSHA256 -cne $ExpectedExecutableSHA256.ToLowerInvariant()) {
        throw "Build receipt executable SHA-256 '$executableSHA256' does not match expected '$($ExpectedExecutableSHA256.ToLowerInvariant())'."
    }

    return [pscustomobject]@{
        SourceKind = $sourceKind
        SourceClean = $sourceClean
        Commit = $sourceCommit
        GitTree = $sourceGitTree
        SourceTreeSHA256 = $sourceTreeSHA256
        ResourceSHA256 = $resourceSHA256
        ExecutableSHA256 = $executableSHA256
        ExecutableSize = [int64]$executableSize
    }
}

function Normalize-TwinTidyCertificateThumbprint {
    param([Parameter(Mandatory = $true)][string]$Thumbprint)

    $normalized = $Thumbprint.Replace(" ", "").ToUpperInvariant()
    if ($normalized -notmatch '^[0-9A-F]{64}$') {
        throw "Certificate SHA-256 '$Thumbprint' is not a 64-character hexadecimal digest."
    }
    return $normalized
}

function Get-TwinTidyCertificateSHA256 {
    param([Parameter(Mandatory = $true)]$Certificate)

    if ($null -eq $Certificate.RawData -or $Certificate.RawData -isnot [byte[]] -or $Certificate.RawData.Length -eq 0) {
        throw "Certificate raw DER bytes are unavailable for SHA-256 verification."
    }
    $algorithm = [System.Security.Cryptography.SHA256]::Create()
    try {
        return (ConvertTo-TwinTidyHex -Bytes $algorithm.ComputeHash($Certificate.RawData)).ToUpperInvariant()
    } finally {
        $algorithm.Dispose()
    }
}

function Get-TwinTidyCertificateEnhancedKeyUsages {
    param([Parameter(Mandatory = $true)]$Certificate)

    $values = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($extension in @($Certificate.Extensions)) {
        if ($null -eq $extension.Oid -or [string]$extension.Oid.Value -cne "2.5.29.37") {
            continue
        }
        if ($extension -isnot [System.Security.Cryptography.X509Certificates.X509EnhancedKeyUsageExtension]) {
            $extension = [System.Security.Cryptography.X509Certificates.X509EnhancedKeyUsageExtension]::new($extension, $extension.Critical)
        }
        foreach ($usage in $extension.EnhancedKeyUsages) {
            [void]$values.Add([string]$usage.Value)
        }
    }
    return @($values)
}

function Assert-TwinTidyAuthenticodeSignature {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$ExpectedSignerSubject,
        [string]$ExpectedSignerThumbprint,
        [ValidateSet("fixed-certificate", "artifact-signing")]
        [string]$SignerVerificationMode = "fixed-certificate",
        [string]$ExpectedSignerIdentityEku,
        [bool]$RequireTimestamp = $true,
        $Signature
    )

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    if ($null -eq $Signature) {
        if (-not [System.IO.File]::Exists($fullPath)) {
            throw "Signed executable is missing: $fullPath"
        }
        $signatureCommand = Get-Command Get-AuthenticodeSignature -CommandType Cmdlet -ErrorAction SilentlyContinue
        if ($null -eq $signatureCommand) {
            throw "Get-AuthenticodeSignature is unavailable; signed releases must be verified on Windows."
        }
        $Signature = Get-AuthenticodeSignature -LiteralPath $fullPath
    }

    if ([string]$Signature.Status -cne "Valid") {
        $statusMessage = [string]$Signature.StatusMessage
        throw "Authenticode signature for '$fullPath' is not valid (status '$($Signature.Status)': $statusMessage)."
    }
    if ($null -eq $Signature.SignerCertificate) {
        throw "Authenticode signature for '$fullPath' has no signer certificate."
    }

    $actualSubject = [string]$Signature.SignerCertificate.Subject
    $actualThumbprint = Get-TwinTidyCertificateSHA256 -Certificate $Signature.SignerCertificate
    if (-not [string]::Equals($actualSubject, $ExpectedSignerSubject, [System.StringComparison]::Ordinal)) {
        throw "Authenticode signer subject '$actualSubject' does not match expected '$ExpectedSignerSubject'."
    }
    $signerIdentityEku = $null
    if ($SignerVerificationMode -ceq "fixed-certificate") {
        if ([string]::IsNullOrWhiteSpace($ExpectedSignerThumbprint)) {
            throw "Fixed-certificate verification requires the expected certificate SHA-256."
        }
        $expectedThumbprint = Normalize-TwinTidyCertificateThumbprint -Thumbprint $ExpectedSignerThumbprint
        if ($actualThumbprint -cne $expectedThumbprint) {
            throw "Authenticode signer certificate SHA-256 '$actualThumbprint' does not match expected '$expectedThumbprint'."
        }
    } else {
        if ([string]::IsNullOrWhiteSpace($ExpectedSignerIdentityEku) -or
            $ExpectedSignerIdentityEku -notmatch '^1\.3\.6\.1\.4\.1\.311\.97\.(?:[0-9]+\.)*[0-9]+$' -or
            $ExpectedSignerIdentityEku -ceq "1.3.6.1.4.1.311.97.1.0") {
            throw "Artifact Signing verification requires the exact durable subscriber identity EKU."
        }
        $ekus = @(Get-TwinTidyCertificateEnhancedKeyUsages -Certificate $Signature.SignerCertificate)
        foreach ($requiredEku in @("1.3.6.1.5.5.7.3.3", "1.3.6.1.4.1.311.97.1.0", $ExpectedSignerIdentityEku)) {
            if ($ekus -cnotcontains $requiredEku) {
                throw "Artifact Signing certificate is missing required EKU '$requiredEku'."
            }
        }
        $signerIdentityEku = $ExpectedSignerIdentityEku
    }

    $timestampSubject = $null
    $timestampThumbprint = $null
    if ($null -ne $Signature.TimeStamperCertificate) {
        $timestampSubject = [string]$Signature.TimeStamperCertificate.Subject
        $timestampThumbprint = Get-TwinTidyCertificateSHA256 -Certificate $Signature.TimeStamperCertificate
    } elseif ($RequireTimestamp) {
        throw "Authenticode signature for '$fullPath' is not timestamped."
    }

    return [pscustomobject]@{
        Status = "Valid"
        SignerSubject = $actualSubject
        SignerThumbprint = $actualThumbprint
        VerificationMode = $SignerVerificationMode
        SignerIdentityEku = $signerIdentityEku
        SignerNotBefore = ([System.DateTimeOffset]$Signature.SignerCertificate.NotBefore).ToUniversalTime().ToString("o")
        SignerNotAfter = ([System.DateTimeOffset]$Signature.SignerCertificate.NotAfter).ToUniversalTime().ToString("o")
        Timestamped = $null -ne $Signature.TimeStamperCertificate
        TimestampSubject = $timestampSubject
        TimestampThumbprint = $timestampThumbprint
    }
}

function Assert-TwinTidySignedProvenance {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]$Provenance,
        [Parameter(Mandatory = $true)][string]$ExpectedVersion,
        [Parameter(Mandatory = $true)][string]$ExpectedArchitecture,
        [Parameter(Mandatory = $true)][string]$ExpectedSourceDate,
        [Parameter(Mandatory = $true)][string]$ExpectedCommit,
        [Parameter(Mandatory = $true)][string]$ExpectedGitTree,
        [Parameter(Mandatory = $true)][string]$ExpectedSourceTreeSHA256,
        [Parameter(Mandatory = $true)][string]$ExpectedUnsignedExecutableSHA256,
        [Parameter(Mandatory = $true)][string]$ExpectedBuildReceiptSHA256,
        [Parameter(Mandatory = $true)][string]$ExpectedSignedExecutableSHA256,
        [Parameter(Mandatory = $true)][string]$ExpectedSignerSubject,
        [string]$ExpectedSignerThumbprint,
        [ValidateSet("fixed-certificate", "artifact-signing")]
        [string]$SignerVerificationMode = "fixed-certificate",
        [string]$ExpectedSignerIdentityEku
    )

    $context = "TwinTidy signed provenance"
    $schema = Get-TwinTidyRequiredJsonProperty -Object $Provenance -Name "schema" -Context $context
    $product = Get-TwinTidyRequiredJsonProperty -Object $Provenance -Name "product" -Context $context
    $version = Get-TwinTidyRequiredJsonProperty -Object $Provenance -Name "version" -Context $context
    $architecture = Get-TwinTidyRequiredJsonProperty -Object $Provenance -Name "architecture" -Context $context
    $sourceDate = Get-TwinTidyRequiredJsonProperty -Object $Provenance -Name "sourceDate" -Context $context
    $source = Get-TwinTidyRequiredJsonProperty -Object $Provenance -Name "source" -Context $context
    $unsigned = Get-TwinTidyRequiredJsonProperty -Object $Provenance -Name "unsigned" -Context $context
    $signed = Get-TwinTidyRequiredJsonProperty -Object $Provenance -Name "signed" -Context $context
    $authenticode = Get-TwinTidyRequiredJsonProperty -Object $Provenance -Name "authenticode" -Context $context

    if ($schema -cne "twintidy.signed-provenance/v1") { throw "Unsupported TwinTidy signed provenance schema '$schema'." }
    if ($product -cne "TwinTidy") { throw "Signed provenance product '$product' is not TwinTidy." }
    if ($version -cne $ExpectedVersion) { throw "Signed provenance version '$version' does not match '$ExpectedVersion'." }
    if ($architecture -cne $ExpectedArchitecture) { throw "Signed provenance architecture '$architecture' does not match '$ExpectedArchitecture'." }
    if ($sourceDate -cne $ExpectedSourceDate) { throw "Signed provenance source date '$sourceDate' does not match '$ExpectedSourceDate'." }
    $sourceCommit = Get-TwinTidyRequiredJsonProperty -Object $source -Name "commit" -Context "$context source"
    $sourceGitTree = Get-TwinTidyRequiredJsonProperty -Object $source -Name "gitTree" -Context "$context source"
    $sourceTreeSHA256 = Get-TwinTidyRequiredJsonProperty -Object $source -Name "treeSHA256" -Context "$context source"
    if ($sourceCommit -cne $ExpectedCommit.ToLowerInvariant() -or
        $sourceGitTree -cne $ExpectedGitTree.ToLowerInvariant() -or
        $sourceTreeSHA256 -cne $ExpectedSourceTreeSHA256.ToLowerInvariant()) {
        throw "Signed provenance source identity does not match the validated release commit, Git tree, and source-tree SHA-256."
    }

    $unsignedPath = Get-TwinTidyRequiredJsonProperty -Object $unsigned -Name "path" -Context "$context unsigned"
    $unsignedHash = Get-TwinTidyRequiredJsonProperty -Object $unsigned -Name "sha256" -Context "$context unsigned"
    $unsignedSize = Get-TwinTidyRequiredJsonProperty -Object $unsigned -Name "size" -Context "$context unsigned"
    $receiptHash = Get-TwinTidyRequiredJsonProperty -Object $unsigned -Name "buildReceiptSHA256" -Context "$context unsigned"
    if ($unsignedPath -cne "TwinTidy.exe") { throw "Signed provenance unsigned executable path '$unsignedPath' is invalid." }
    if ($unsignedHash -cne $ExpectedUnsignedExecutableSHA256.ToLowerInvariant()) { throw "Signed provenance does not bind the expected unsigned executable." }
    if ([int64]$unsignedSize -le 0) { throw "Signed provenance has an invalid unsigned executable size." }
    if ($receiptHash -cne $ExpectedBuildReceiptSHA256.ToLowerInvariant()) { throw "Signed provenance does not bind the expected build receipt." }

    $signedPath = Get-TwinTidyRequiredJsonProperty -Object $signed -Name "path" -Context "$context signed"
    $signedHash = Get-TwinTidyRequiredJsonProperty -Object $signed -Name "sha256" -Context "$context signed"
    $signedSize = Get-TwinTidyRequiredJsonProperty -Object $signed -Name "size" -Context "$context signed"
    if ($signedPath -cne "TwinTidy.exe") { throw "Signed provenance signed executable path '$signedPath' is invalid." }
    if ($signedHash -cne $ExpectedSignedExecutableSHA256.ToLowerInvariant()) { throw "Signed provenance does not bind the expected signed executable." }
    if ([int64]$signedSize -le 0) { throw "Signed provenance has an invalid signed executable size." }
    if ($signedHash -ceq $unsignedHash) { throw "Signed provenance claims identical unsigned and signed executable digests." }

    $status = Get-TwinTidyRequiredJsonProperty -Object $authenticode -Name "status" -Context "$context authenticode"
    $signerSubject = Get-TwinTidyRequiredJsonProperty -Object $authenticode -Name "signerSubject" -Context "$context authenticode"
    $signerThumbprint = Get-TwinTidyRequiredJsonProperty -Object $authenticode -Name "signerThumbprint" -Context "$context authenticode"
    $verificationMode = Get-TwinTidyRequiredJsonProperty -Object $authenticode -Name "verificationMode" -Context "$context authenticode"
    $signerIdentityEku = Get-TwinTidyRequiredJsonProperty -Object $authenticode -Name "signerIdentityEku" -Context "$context authenticode"
    $timestamped = Get-TwinTidyRequiredJsonProperty -Object $authenticode -Name "timestamped" -Context "$context authenticode"
    $timestampSubject = Get-TwinTidyRequiredJsonProperty -Object $authenticode -Name "timestampSubject" -Context "$context authenticode"
    $timestampThumbprint = Get-TwinTidyRequiredJsonProperty -Object $authenticode -Name "timestampThumbprint" -Context "$context authenticode"
    if ($status -cne "Valid") { throw "Signed provenance does not record a valid Authenticode signature." }
    if (-not [string]::Equals([string]$signerSubject, $ExpectedSignerSubject, [System.StringComparison]::Ordinal)) {
        throw "Signed provenance signer subject '$signerSubject' does not match '$ExpectedSignerSubject'."
    }
    $actualSignerSHA256 = Normalize-TwinTidyCertificateThumbprint -Thumbprint ([string]$signerThumbprint)
    if ([string]$verificationMode -cne $SignerVerificationMode) {
        throw "Signed provenance verification mode '$verificationMode' does not match '$SignerVerificationMode'."
    }
    if ($SignerVerificationMode -ceq "fixed-certificate") {
        if ([string]::IsNullOrWhiteSpace($ExpectedSignerThumbprint) -or
            $actualSignerSHA256 -cne (Normalize-TwinTidyCertificateThumbprint -Thumbprint $ExpectedSignerThumbprint)) {
            throw "Signed provenance certificate SHA-256 does not match the expected fixed certificate."
        }
        if ($null -ne $signerIdentityEku -and -not [string]::IsNullOrWhiteSpace([string]$signerIdentityEku)) {
            throw "Fixed-certificate provenance unexpectedly records a managed signer identity EKU."
        }
    } elseif ([string]::IsNullOrWhiteSpace($ExpectedSignerIdentityEku) -or [string]$signerIdentityEku -cne $ExpectedSignerIdentityEku) {
        throw "Signed provenance does not bind the expected Artifact Signing durable identity EKU."
    }
    if ($timestamped -isnot [bool] -or -not $timestamped -or [string]::IsNullOrWhiteSpace([string]$timestampSubject) -or [string]::IsNullOrWhiteSpace([string]$timestampThumbprint)) {
        throw "Signed provenance does not record a timestamp certificate."
    }

    return [pscustomobject]@{
        UnsignedExecutableSHA256 = $unsignedHash
        UnsignedExecutableSize = [int64]$unsignedSize
        BuildReceiptSHA256 = $receiptHash
        SignedExecutableSHA256 = $signedHash
        SignedExecutableSize = [int64]$signedSize
        SignerSubject = [string]$signerSubject
        SignerThumbprint = $actualSignerSHA256
        VerificationMode = [string]$verificationMode
        SignerIdentityEku = if ($null -eq $signerIdentityEku) { $null } else { [string]$signerIdentityEku }
        TimestampSubject = [string]$timestampSubject
        TimestampThumbprint = Normalize-TwinTidyCertificateThumbprint -Thumbprint ([string]$timestampThumbprint)
    }
}

function Assert-TwinTidyMSIXPackageBinding {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$ExpectedPackageName,
        [Parameter(Mandatory = $true)][string]$ExpectedPublisher,
        [Parameter(Mandatory = $true)][string]$ExpectedPackageVersion,
        [ValidateSet("x64", "arm64")]
        [Parameter(Mandatory = $true)][string]$ExpectedArchitecture,
        [Parameter(Mandatory = $true)][string]$ExpectedExecutableSHA256,
        [Parameter(Mandatory = $true)][string]$ExpectedBuildReceiptSHA256,
        [Parameter(Mandatory = $true)][string]$ExpectedSignedProvenanceSHA256,
        [switch]$RequirePackageSignature
    )

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    if (-not [System.IO.File]::Exists($fullPath)) {
        throw "MSIX package is missing: $fullPath"
    }
    foreach ($digest in @($ExpectedExecutableSHA256, $ExpectedBuildReceiptSHA256, $ExpectedSignedProvenanceSHA256)) {
        if ($digest -notmatch '^[0-9a-fA-F]{64}$') {
            throw "MSIX binding expected digest '$digest' is not SHA-256."
        }
    }

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [System.IO.Compression.ZipFile]::OpenRead($fullPath)
    try {
        $allowedEntries = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
        foreach ($name in @(
            "[Content_Types].xml",
            "AppxBlockMap.xml",
            "AppxManifest.xml",
            "AppxMetadata/CodeIntegrity.cat",
            "AppxSignature.p7x",
            "Assets/Square44x44Logo.png",
            "Assets/Square150x150Logo.png",
            "Assets/StoreLogo.png",
            "LICENSE",
            "ReleaseMetadata/TwinTidy.build-receipt.json",
            "ReleaseMetadata/TwinTidy.signed-provenance.json",
            "THIRD_PARTY_NOTICES.txt",
            "TwinTidy.exe"
        )) {
            [void]$allowedEntries.Add($name)
        }

        $entries = @{}
        foreach ($entry in $archive.Entries) {
            $name = $entry.FullName.Replace('\', '/')
            if ([string]::IsNullOrWhiteSpace($name) -or
                $name.StartsWith("/", [System.StringComparison]::Ordinal) -or
                $name -match '^[A-Za-z]:' -or
                @($name.Split('/') | Where-Object { $_ -in @("", ".", "..") }).Count -gt 0) {
                throw "MSIX contains an unsafe entry path '$($entry.FullName)'."
            }
            $key = $name.ToLowerInvariant()
            if ($entries.ContainsKey($key)) {
                throw "MSIX contains a duplicate case-insensitive entry '$name'."
            }
            if (-not $allowedEntries.Contains($name)) {
                throw "MSIX contains unexpected entry '$name'."
            }
            $entries[$key] = $entry
        }

        $requiredEntries = @(
            "[Content_Types].xml",
            "AppxBlockMap.xml",
            "AppxManifest.xml",
            "Assets/Square44x44Logo.png",
            "Assets/Square150x150Logo.png",
            "Assets/StoreLogo.png",
            "LICENSE",
            "ReleaseMetadata/TwinTidy.build-receipt.json",
            "ReleaseMetadata/TwinTidy.signed-provenance.json",
            "THIRD_PARTY_NOTICES.txt",
            "TwinTidy.exe"
        )
        if ($RequirePackageSignature) {
            $requiredEntries += "AppxSignature.p7x"
        }
        foreach ($required in $requiredEntries) {
            if (-not $entries.ContainsKey($required.ToLowerInvariant())) {
                throw "MSIX is missing required entry '$required'."
            }
        }
        if (-not $RequirePackageSignature -and $entries.ContainsKey("appxsignature.p7x")) {
            throw "Unsigned MSIX staging package unexpectedly contains AppxSignature.p7x."
        }

        $manifestEntry = $entries["appxmanifest.xml"]
        if ($manifestEntry.Length -gt 1MB) {
            throw "MSIX AppxManifest.xml is too large."
        }
        $manifestStream = $manifestEntry.Open()
        try {
            $settings = [System.Xml.XmlReaderSettings]::new()
            $settings.DtdProcessing = [System.Xml.DtdProcessing]::Prohibit
            $settings.XmlResolver = $null
            $reader = [System.Xml.XmlReader]::Create($manifestStream, $settings)
            try {
                $document = [System.Xml.XmlDocument]::new()
                $document.XmlResolver = $null
                $document.Load($reader)
            } finally {
                $reader.Dispose()
            }
        } finally {
            $manifestStream.Dispose()
        }
        $namespace = [System.Xml.XmlNamespaceManager]::new($document.NameTable)
        $namespace.AddNamespace("f", "http://schemas.microsoft.com/appx/manifest/foundation/windows10")
        $identity = $document.SelectSingleNode("/f:Package/f:Identity", $namespace)
        $application = $document.SelectSingleNode("/f:Package/f:Applications/f:Application", $namespace)
        if ($null -eq $identity -or $null -eq $application) {
            throw "MSIX manifest is missing its package identity or application."
        }
        if ($identity.GetAttribute("Name") -cne $ExpectedPackageName -or
            $identity.GetAttribute("Publisher") -cne $ExpectedPublisher -or
            $identity.GetAttribute("Version") -cne $ExpectedPackageVersion -or
            $identity.GetAttribute("ProcessorArchitecture") -cne $ExpectedArchitecture) {
            throw "MSIX manifest identity, publisher, version, or architecture does not match the release binding."
        }
        if ($application.GetAttribute("Executable") -cne "TwinTidy.exe" -or
            $application.GetAttribute("EntryPoint") -cne "Windows.FullTrustApplication") {
            throw "MSIX application entry point is not the expected TwinTidy full-trust executable."
        }

        $entryDigests = @{
            "TwinTidy.exe" = $ExpectedExecutableSHA256.ToLowerInvariant()
            "ReleaseMetadata/TwinTidy.build-receipt.json" = $ExpectedBuildReceiptSHA256.ToLowerInvariant()
            "ReleaseMetadata/TwinTidy.signed-provenance.json" = $ExpectedSignedProvenanceSHA256.ToLowerInvariant()
        }
        foreach ($binding in $entryDigests.GetEnumerator()) {
            $stream = $entries[$binding.Key.ToLowerInvariant()].Open()
            try {
                $actual = Get-TwinTidyStreamSHA256 -Stream $stream
            } finally {
                $stream.Dispose()
            }
            if ($actual -cne $binding.Value) {
                throw "MSIX entry '$($binding.Key)' SHA-256 '$actual' does not match '$($binding.Value)'."
            }
        }

        return [pscustomobject]@{
            Path = $fullPath
            PackageName = $ExpectedPackageName
            Publisher = $ExpectedPublisher
            PackageVersion = $ExpectedPackageVersion
            Architecture = $ExpectedArchitecture
            ExecutableSHA256 = $ExpectedExecutableSHA256.ToLowerInvariant()
            Signed = $entries.ContainsKey("appxsignature.p7x")
        }
    } finally {
        $archive.Dispose()
    }
}
