[CmdletBinding()]
param(
    [string]$Version,
    [string]$CertificateThumbprint,
    [ValidateSet('CurrentUser', 'LocalMachine')][string]$CertificateStore = 'CurrentUser',
    [string]$SignToolPath,
    [string]$TimestampUrl,
    [string]$IsccPath,
    [string]$ReleaseBaseUrl,
    [string]$PreviousInstallerPath,
    [switch]$PreflightOnly
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$artifactDir = Join-Path $repoRoot 'artifacts'
if (!$ReleaseBaseUrl) {
    $distributionConfig = Join-Path $repoRoot 'distribution\release-config.json'
    if (Test-Path -LiteralPath $distributionConfig -PathType Leaf) {
        $ReleaseBaseUrl = (Get-Content -LiteralPath $distributionConfig -Raw | ConvertFrom-Json).base_url
    }
}

function Test-HttpsUrl([string]$Text) {
    $parsed = $null
    return [Uri]::TryCreate($Text, [UriKind]::Absolute, [ref]$parsed) -and
        $parsed.Scheme -ceq 'https' -and !$parsed.UserInfo -and !$parsed.Query -and !$parsed.Fragment
}
function Get-CertificatePin($Certificate) {
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($Certificate.RawData)).ToLowerInvariant()
}
function Assert-Signed([string]$Path, [string]$Pin) {
    & $SignToolPath verify /pa /all /tw $Path
    if ($LASTEXITCODE -ne 0) { throw 'Authenticode verification or timestamp verification failed.' }
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    if ($signature.Status -ne 'Valid' -or !$signature.SignerCertificate -or !$signature.TimeStamperCertificate -or
        (Get-CertificatePin $signature.SignerCertificate) -cne $Pin) {
        throw 'The signed file must have the selected trusted signer and an RFC3161 timestamp.'
    }
}

$now = Get-Date
$eligible = @(Get-ChildItem -LiteralPath "Cert:\$CertificateStore\My" | Where-Object {
    $_.HasPrivateKey -and $_.NotBefore -le $now -and $_.NotAfter -gt $now -and
    @($_.EnhancedKeyUsageList | ForEach-Object { [string]$_.ObjectId }) -contains '1.3.6.1.5.5.7.3.3'
})
if (!$SignToolPath) {
    $sdkRoot = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\10\bin'
    if (Test-Path -LiteralPath $sdkRoot) {
        $SignToolPath = Get-ChildItem -LiteralPath $sdkRoot -Directory | Sort-Object Name -Descending |
            ForEach-Object { Join-Path $_.FullName 'x64\signtool.exe' } |
            Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Select-Object -First 1
    }
}
$toolAvailable = $SignToolPath -and [IO.Path]::IsPathFullyQualified($SignToolPath) -and
    (Test-Path -LiteralPath $SignToolPath -PathType Leaf)
if ($PreflightOnly) {
    [ordered]@{ eligible_code_signing_certificates = $eligible.Count; signtool_available = [bool]$toolAvailable;
        artifact_changes = $false; certificate_store_changes = $false } | ConvertTo-Json
    return
}
if (!$toolAvailable -or [IO.Path]::GetFileName($SignToolPath) -ine 'signtool.exe') { throw 'An absolute Windows SDK signtool.exe path is required.' }
if ((Get-AuthenticodeSignature -LiteralPath $SignToolPath).Status -ne 'Valid') { throw 'SignTool itself must have a valid OS-trusted signature.' }
if ($CertificateThumbprint -cnotmatch '^[0-9a-fA-F]{40}$') { throw 'Select one certificate by its exact store thumbprint.' }
$cert = @($eligible | Where-Object Thumbprint -eq $CertificateThumbprint)
if ($cert.Count -ne 1) { throw 'The selected certificate must be present, time-valid, have a private key and Code Signing EKU.' }
$chain = [Security.Cryptography.X509Certificates.X509Chain]::new()
try {
    $chain.ChainPolicy.RevocationMode = 'Online'
    $chain.ChainPolicy.UrlRetrievalTimeout = [TimeSpan]::FromSeconds(20)
    $chain.ChainPolicy.ApplicationPolicy.Add([Security.Cryptography.Oid]::new('1.3.6.1.5.5.7.3.3'))
    if (!$chain.Build($cert[0])) { throw 'The selected certificate chain must be OS-trusted and pass online revocation checks.' }
} finally { $chain.Dispose() }
if (!(Test-HttpsUrl $TimestampUrl)) { throw 'An explicit HTTPS RFC3161 timestamp service URL is required.' }
if (!(Test-HttpsUrl $ReleaseBaseUrl)) { throw 'An explicit HTTPS release base URL without query/fragment is required.' }
if ($Version -cnotmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') { throw 'A stable numeric release Version is required.' }
$exe = Join-Path $artifactDir 'Pokegents.exe'
$manifestPath = Join-Path $artifactDir 'build-manifest.json'
$manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json -AsHashtable
if ($manifest.app_version -cne $Version -or
    (Get-FileHash -LiteralPath $exe -Algorithm SHA256).Hash.ToLowerInvariant() -cne $manifest.sha256) {
    throw 'Build the requested version first; executable and build manifest must match.'
}
$pin = Get-CertificatePin $cert[0]
if ($PreviousInstallerPath) {
    if (![IO.Path]::IsPathFullyQualified($PreviousInstallerPath)) { throw 'PreviousInstallerPath must be absolute.' }
    Assert-Signed $PreviousInstallerPath $pin
    $rawVersion = (Get-Item -LiteralPath $PreviousInstallerPath).VersionInfo.ProductVersion
    if ($rawVersion -match '^([0-9]+\.[0-9]+\.[0-9]+)\.0$') { $oldVersion = $Matches[1] } else { $oldVersion = $rawVersion }
    if ((Get-Item -LiteralPath $PreviousInstallerPath).VersionInfo.ProductName -cne 'Pokegents' -or
        $oldVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+$' -or [version]$oldVersion -ge [version]$Version) {
        throw 'Rollback input must be an older signed Pokegents installer.'
    }
}
$signArgs = @('sign', '/s', 'My', '/sha1', $CertificateThumbprint, '/fd', 'SHA256', '/tr', $TimestampUrl, '/td', 'SHA256')
if ($CertificateStore -ceq 'LocalMachine') { $signArgs += '/sm' }
$releaseDir = Join-Path $artifactDir ('signing-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $releaseDir | Out-Null
$signedExe = Join-Path $releaseDir 'Pokegents.exe'
Copy-Item -LiteralPath $exe -Destination (Join-Path $releaseDir 'unsigned-Pokegents.exe')
Copy-Item -LiteralPath $manifestPath -Destination (Join-Path $releaseDir 'unsigned-build-manifest.json')
Copy-Item -LiteralPath $exe -Destination $signedExe
& $SignToolPath @signArgs $signedExe
if ($LASTEXITCODE -ne 0) { throw 'Executable signing failed; original build retained.' }
Assert-Signed $signedExe $pin

# The compiler signs Setup, its temporary copies and the uninstaller. $f and
# $q are Inno substitutions, never user-supplied script fragments.
foreach ($text in @($SignToolPath, $TimestampUrl)) {
    if ($text -match '["$\r\n]') { throw 'Unsupported characters in Inno signing parameters.' }
}
$signCommand = '$q' + $SignToolPath + '$q sign /s My /sha1 ' + $CertificateThumbprint +
    ' /fd SHA256 /tr $q' + $TimestampUrl + '$q /td SHA256'
if ($CertificateStore -ceq 'LocalMachine') { $signCommand += ' /sm' }
$signCommand += ' $f'
Copy-Item -LiteralPath $signedExe -Destination $exe -Force
$manifest.unsigned_sha256 = $manifest.sha256
$manifest.sha256 = (Get-FileHash -LiteralPath $exe -Algorithm SHA256).Hash.ToLowerInvariant()
$manifest.signer_sha256 = $pin
$manifest.authenticode = 'valid-timestamped'
$manifest | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $manifestPath -Encoding utf8NoBOM
try {
    & (Join-Path $PSScriptRoot 'package.ps1') -Version $Version -IsccPath $IsccPath -ReleaseSignCommand $signCommand
    $installer = Join-Path $artifactDir "Pokegents-Setup-$Version-x64.exe"
    Assert-Signed $installer $pin
} catch {
    Copy-Item -LiteralPath (Join-Path $releaseDir 'unsigned-Pokegents.exe') -Destination $exe -Force
    Copy-Item -LiteralPath (Join-Path $releaseDir 'unsigned-build-manifest.json') -Destination $manifestPath -Force
    throw
}
function New-Artifact([string]$Path, [string]$ArtifactVersion) {
    return [ordered]@{ version = $ArtifactVersion; url = $ReleaseBaseUrl.TrimEnd('/') + '/' + [IO.Path]::GetFileName($Path);
        sha256 = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant(); size = (Get-Item -LiteralPath $Path).Length }
}
$release = [ordered]@{ schema_version = 1; product = 'Pokegents'; target = 'windows/amd64'; executable_sha256 = $manifest.sha256; installer = (New-Artifact $installer $Version) }
if ($PreviousInstallerPath) {
    $release.rollback = New-Artifact $PreviousInstallerPath $oldVersion
}
$release | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $artifactDir 'release-manifest.json') -Encoding utf8NoBOM
[ordered]@{ version = $Version; signer_sha256 = $pin; setup = $installer; unsigned_backup = $releaseDir;
    release_manifest = (Join-Path $artifactDir 'release-manifest.json'); published = $false } | ConvertTo-Json
