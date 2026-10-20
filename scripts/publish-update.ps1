[CmdletBinding()]
param(
    [string]$ArtifactDirectory,
    [string]$WranglerPath,
    [switch]$Publish
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
if (!$ArtifactDirectory) { $ArtifactDirectory = Join-Path $repoRoot 'artifacts' }
if (![IO.Path]::IsPathFullyQualified($ArtifactDirectory)) { throw 'ArtifactDirectory must be absolute.' }
$config = Get-Content -LiteralPath (Join-Path $repoRoot 'distribution\release-config.json') -Raw | ConvertFrom-Json
$baseUrl = [string]$config.base_url
if ($config.schema_version -ne 1 -or $config.provider -cne 'cloudflare-pages' -or
    $config.project_name -cne 'pokegents-windows-updates' -or $config.production_branch -cne 'main' -or
    $baseUrl -cne 'https://pokegents-windows-updates.pages.dev' -or $config.max_file_bytes -ne 26214400) {
    throw 'Unexpected distribution configuration.'
}
function Read-ReleaseJson([string]$Path) {
    $info = Get-Item -LiteralPath $Path
    if ($info.Length -gt 65536 -or ($info.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw 'Invalid release metadata file.'
    }
    return Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json -AsHashtable
}
function Assert-Hash([string]$Path, [string]$Expected) {
    if ($Expected -cnotmatch '^[0-9a-f]{64}$' -or
        (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() -cne $Expected) {
        throw 'Release file SHA256 mismatch.'
    }
}
function Assert-Signature([string]$Path, [string]$ExpectedPin) {
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    if ($signature.Status -ne 'Valid' -or !$signature.SignerCertificate -or !$signature.TimeStamperCertificate) {
        throw 'Release publication requires trusted timestamped Authenticode signatures.'
    }
    $pin = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($signature.SignerCertificate.RawData)).ToLowerInvariant()
    if ($ExpectedPin -cnotmatch '^[0-9a-f]{64}$' -or $pin -cne $ExpectedPin) {
        throw 'Release signer does not match the verified build.'
    }
}
$manifest = Read-ReleaseJson (Join-Path $ArtifactDirectory 'release-manifest.json')
$build = Read-ReleaseJson (Join-Path $ArtifactDirectory 'build-manifest.json')
foreach ($key in @('schema_version', 'product', 'target', 'executable_sha256', 'installer', 'rollback')) {
    if (!$manifest.ContainsKey($key)) { throw "Release metadata requires $key." }
}
if ($manifest.schema_version -ne 1 -or $manifest.product -cne 'Pokegents' -or
    $manifest.target -cne 'windows/amd64' -or !$manifest.rollback -or
    !$build.ContainsKey('authenticode') -or $build.authenticode -cne 'valid-timestamped' -or
    !$build.ContainsKey('signer_sha256')) { throw 'A signed release and signed rollback installer are required.' }
$version = [string]$manifest.installer.version
$oldVersion = [string]$manifest.rollback.version
$versionPattern = '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
if ($version -cnotmatch $versionPattern -or $oldVersion -cnotmatch $versionPattern -or
    [version]$version -le [version]$oldVersion -or $build.app_version -cne $version) { throw 'Release versions do not match.' }
$exe = Join-Path $ArtifactDirectory 'Pokegents.exe'
Assert-Hash $exe $build.sha256
Assert-Hash $exe $manifest.executable_sha256
Assert-Signature $exe $build.signer_sha256
$files = @()
foreach ($artifact in @($manifest.installer, $manifest.rollback)) {
    $name = 'Pokegents-Setup-' + $artifact.version + '-x64.exe'
    if ($artifact.url -cne ($baseUrl + '/' + $name) -or $artifact.size -le 0 -or
        $artifact.size -gt $config.max_file_bytes) { throw 'Release URL/size is outside the configured host limits.' }
    $path = Join-Path $ArtifactDirectory $name
    $info = Get-Item -LiteralPath $path
    if ($info.Length -ne $artifact.size -or ($info.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw 'Invalid installer input.'
    }
    Assert-Hash $path $artifact.sha256
    Assert-Signature $path $build.signer_sha256
    if ($info.VersionInfo.ProductName -cne 'Pokegents' -or
        @($artifact.version, ($artifact.version + '.0')) -cnotcontains $info.VersionInfo.ProductVersion) {
        throw 'Installer product/version does not match its signed metadata.'
    }
    $files += @{ name = $name; path = $path; sha256 = $artifact.sha256 }
}
# Copy only the public allowlist into a fresh staging directory. Never deploy
# the repository, artifacts directory, profile data, logs or signing material.
$site = Join-Path $repoRoot 'distribution\site'
$allowed = @('index.html', '404.html', 'distribution-status.json', 'robots.txt', '_headers')
$siteFiles = @(Get-ChildItem -LiteralPath $site -Force)
if ($siteFiles.Count -ne $allowed.Count -or @($siteFiles | Where-Object {
    $_.PSIsContainer -or $_.Name -cnotin $allowed -or ($_.Attributes -band [IO.FileAttributes]::ReparsePoint)
}).Count) { throw 'Unexpected public site files.' }
$stage = Join-Path $ArtifactDirectory ('publish-stage-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $stage | Out-Null
foreach ($file in $siteFiles) { Copy-Item -LiteralPath $file.FullName -Destination (Join-Path $stage $file.Name) }
foreach ($file in $files) {
    $destination = Join-Path $stage $file.name
    Copy-Item -LiteralPath $file.path -Destination $destination
    Assert-Hash $destination $file.sha256
    Assert-Signature $destination $build.signer_sha256
}
$manifest | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $stage 'release-manifest.json') -Encoding utf8NoBOM
@{ schema_version = 1; product = 'Pokegents'; status = 'available'; published_release = $version } |
    ConvertTo-Json | Set-Content -LiteralPath (Join-Path $stage 'distribution-status.json') -Encoding utf8NoBOM
$page = [IO.File]::ReadAllText((Join-Path $stage 'index.html'))
$page.Replace('現在はコード署名の準備中です。公開リリースはまだありません。', "公開リリース: $version") |
    Set-Content -LiteralPath (Join-Path $stage 'index.html') -Encoding utf8NoBOM
if ($Publish) {
    if (!$WranglerPath -or ![IO.Path]::IsPathFullyQualified($WranglerPath) -or
        !(Test-Path -LiteralPath $WranglerPath -PathType Leaf)) { throw 'An absolute Wrangler CLI path is required to publish.' }
    & $WranglerPath pages deploy $stage --project-name $config.project_name --branch $config.production_branch `
        --commit-dirty=true --commit-message "Publish signed Pokegents $version"
    if ($LASTEXITCODE -ne 0) { throw 'Release deployment failed; retain the staged inputs for diagnosis.' }
}
[ordered]@{ version = $version; stage = $stage; manifest_url = $config.manifest_url; published = [bool]$Publish } | ConvertTo-Json
