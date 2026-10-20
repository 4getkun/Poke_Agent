[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$artifactDir = Join-Path $repoRoot 'artifacts'
$fixture = Join-Path $artifactDir ('release-gate-test-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $fixture | Out-Null
$exe = Join-Path $artifactDir 'Pokegents.exe'
if ((Get-AuthenticodeSignature -LiteralPath $exe).Status -ne 'NotSigned') {
    throw 'This negative fixture requires the current unsigned development executable.'
}
Copy-Item -LiteralPath $exe -Destination (Join-Path $fixture 'Pokegents.exe')
$hash = (Get-FileHash -LiteralPath $exe -Algorithm SHA256).Hash.ToLowerInvariant()
$base = (Get-Content -LiteralPath (Join-Path $repoRoot 'distribution\release-config.json') -Raw | ConvertFrom-Json).base_url
$fakeBuild = @{app_version='0.1.1';sha256=$hash;authenticode='valid-timestamped';signer_sha256=('0'*64)}
$fakeRelease = @{
    schema_version=1;product='Pokegents';target='windows/amd64';executable_sha256=$hash
    installer=@{version='0.1.1';url="$base/Pokegents-Setup-0.1.1-x64.exe";size=1;sha256=('0'*64)}
    rollback=@{version='0.1.0';url="$base/Pokegents-Setup-0.1.0-x64.exe";size=1;sha256=('0'*64)}
}
$fakeBuild | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $fixture 'build-manifest.json') -Encoding utf8NoBOM
$fakeRelease | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $fixture 'release-manifest.json') -Encoding utf8NoBOM
$rejected = $false
try {
    & (Join-Path $PSScriptRoot 'publish-update.ps1') -ArtifactDirectory $fixture -Publish
} catch {
    if ($_.Exception.Message -notmatch 'trusted timestamped Authenticode') { throw }
    $rejected = $true
}
if (!$rejected -or @(Get-ChildItem -LiteralPath $fixture -Directory -Filter 'publish-stage-*').Count) {
    throw 'Unsigned release passed publication or staging gate.'
}
if ((Get-FileHash -LiteralPath $exe -Algorithm SHA256).Hash.ToLowerInvariant() -cne $hash) {
    throw 'Release preflight modified the build.'
}
$result = [ordered]@{unsigned_release_rejected=$true;false_signed_metadata_rejected=$true;no_staging_before_verification=$true;no_publish=$true;original_unchanged=$true;fixture=$fixture}
$result | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $artifactDir 'logs\release-publication-gate.json') -Encoding utf8NoBOM
$result | ConvertTo-Json
