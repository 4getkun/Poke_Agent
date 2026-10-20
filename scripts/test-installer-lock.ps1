[CmdletBinding()]
param([Parameter(Mandatory)][string]$IsccPath)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$artifacts = Join-Path $repo 'artifacts'
if (-not [IO.Path]::IsPathFullyQualified($IsccPath) -or
    [IO.Path]::GetFileName($IsccPath) -cne 'ISCC.exe' -or
    -not (Test-Path -LiteralPath $IsccPath -PathType Leaf)) {
    throw 'Supply an absolute path to ISCC.exe.'
}
$manifest = Get-Content -LiteralPath (Join-Path $artifacts 'build-manifest.json') -Raw | ConvertFrom-Json
$version = [string]$manifest.app_version
if ($version -cnotmatch '^\d+\.\d+\.\d+$') { throw 'A numeric release version is required.' }
$expectedHash = (Get-FileHash -LiteralPath (Join-Path $artifacts 'Pokegents.exe')).Hash
if ($expectedHash -ine $manifest.sha256) { throw 'Build hash does not match the manifest.' }
$root = Join-Path $artifacts ('installer-lock-' + [guid]::NewGuid().ToString('N'))
$install = Join-Path $root 'アプリ 日本語 &()[]'
$profile = Join-Path $root 'profile'
$id = [guid]::NewGuid().ToString().ToUpperInvariant()
$key = "HKCU:/Software/Microsoft/Windows/CurrentVersion/Uninstall/{$id}_is1"
$productionKey = 'HKCU:/Software/Microsoft/Windows/CurrentVersion/Uninstall/{B2075B47-8E23-4A2A-9D74-4B1DA9BC68C1}_is1'
$shortcut = Join-Path ([Environment]::GetFolderPath('Programs')) 'Pokegents.lnk'
function Get-ProductionIdentity {
    $registration = if (Test-Path -LiteralPath $productionKey) {
        Get-ItemProperty -LiteralPath $productionKey | Select-Object InstallLocation,DisplayVersion,UninstallString
    } else { $null }
    $shortcutHash = if (Test-Path -LiteralPath $shortcut) { (Get-FileHash -LiteralPath $shortcut).Hash } else { $null }
    @{ registration=$registration; shortcut=$shortcutHash } | ConvertTo-Json -Compress
}
function Invoke-FixtureProcess([string]$File, [string[]]$Arguments) {
    $info = [Diagnostics.ProcessStartInfo]::new($File)
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.WindowStyle = 'Hidden'
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    foreach ($argument in $Arguments) { $info.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::Start($info)
    try {
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(120000)) { $process.Kill($true); throw "Fixture timed out: $File" }
        [pscustomobject]@{ Code=$process.ExitCode; Output=$stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult() }
    } finally { $process.Dispose() }
}
function Assert-FixtureIdentity {
    if (-not (Test-Path -LiteralPath $key)) { throw 'Fixture uninstall registration is missing.' }
    $registered = (Get-ItemProperty -LiteralPath $key).InstallLocation
    if ([IO.Path]::GetFullPath($registered).TrimEnd('\') -ine [IO.Path]::GetFullPath($install)) {
        throw 'Fixture registration does not match the isolated directory.'
    }
}
function Get-InstalledHashes {
    @(Get-ChildItem -LiteralPath $install -File -Recurse | Sort-Object FullName | ForEach-Object {
        $_.FullName + ':' + (Get-FileHash -LiteralPath $_.FullName).Hash
    }) -join "`n"
}
$before = Get-ProductionIdentity
$app = $null
$attempted = $false
$passed = $false
New-Item -ItemType Directory -Path $root | Out-Null
try {
    # Only identity, default directory and shortcut location differ from production.
    $source = Get-Content -LiteralPath (Join-Path $repo 'installer/pokegents.iss') -Raw
    $oldId = 'AppId={{B2075B47-8E23-4A2A-9D74-4B1DA9BC68C1}'
    $oldDir = 'DefaultDirName={localappdata}\Programs\Pokegents'
    $oldIcon = 'Name: "{autoprograms}\Pokegents"'
    foreach ($expected in @($oldId,$oldDir,$oldIcon)) {
        if ([regex]::Matches($source,[regex]::Escape($expected)).Count -ne 1) { throw 'Fixture substitution does not match production ISS.' }
    }
    $source = $source.Replace($oldId,"AppId={{$id}").Replace($oldDir,"DefaultDirName=$install").Replace($oldIcon,'Name: "{app}\Pokegents-test"')
    $iss = Join-Path $root 'fixture.iss'
    [IO.File]::WriteAllText($iss,$source,[Text.UTF8Encoding]::new($false))
    $compiled = Invoke-FixtureProcess $IsccPath @("/DMyAppVersion=$version","/DArtifactsDir=$artifacts","/DRepoRoot=$repo","/O$root",$iss)
    $compiled.Output | Set-Content -LiteralPath (Join-Path $root 'compile.log')
    if ($compiled.Code -ne 0) { throw 'Fixture installer compilation failed; see compile.log.' }
    $setup = Join-Path $root "Pokegents-Setup-$version-x64.exe"
    $setupArgs = @('/VERYSILENT','/SUPPRESSMSGBOXES','/NORESTART','/LANG=japanese',"/DIR=$install")
    $attempted = $true
    if ((Invoke-FixtureProcess $setup ($setupArgs + "/LOG=$root/install.log")).Code -ne 0) { throw 'Fresh fixture installation failed.' }
    Assert-FixtureIdentity
    $exe = Join-Path $install 'Pokegents.exe'
    if ((Get-FileHash -LiteralPath $exe).Hash -ine $expectedHash) { throw 'Installed fixture hash mismatch.' }
    $info = [Diagnostics.ProcessStartInfo]::new($exe)
    $info.UseShellExecute = $false
    $info.WindowStyle = 'Normal'
    $info.ArgumentList.Add('--data-dir'); $info.ArgumentList.Add($profile)
    $app = [Diagnostics.Process]::Start($info)
    if (-not $app.WaitForInputIdle(15000)) { throw 'Fixture app did not become idle.' }
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    $database = Join-Path $profile 'db/pokegents.db'
    while (-not (Test-Path -LiteralPath $database) -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 100 }
    if ($app.HasExited -or -not (Test-Path -LiteralPath $database)) { throw 'Fixture app failed to start with its isolated profile.' }
    $hashes = Get-InstalledHashes
    $blocked = Invoke-FixtureProcess $setup ($setupArgs + "/LOG=$root/blocked.log")
    $blockedLog = Get-Content -LiteralPath (Join-Path $root 'blocked.log') -Raw
    if ($blocked.Code -ne 7 -or $blockedLog -notmatch 'Executable update preflight failed' -or $blockedLog -notmatch 'Pokegents.exe が使用中') {
        throw "Running app was not rejected during preflight (exit $($blocked.Code))."
    }
    if ($app.HasExited -or (Get-InstalledHashes) -cne $hashes) { throw 'Blocked installation terminated the app or changed installed files.' }
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    do {
        $app.Refresh()
        if ($app.MainWindowHandle -ne 0) { break }
        Start-Sleep -Milliseconds 100
    } while ([DateTime]::UtcNow -lt $deadline -and -not $app.HasExited)
    if (-not $app.CloseMainWindow() -or -not $app.WaitForExit(15000)) { throw 'Fixture app did not close normally.' }
    $dbHash = (Get-FileHash -LiteralPath $database).Hash
    $sentinel = Join-Path $profile 'keep.txt'
    'retain synthetic profile' | Set-Content -LiteralPath $sentinel
    if ((Invoke-FixtureProcess $setup ($setupArgs + "/LOG=$root/retry.log")).Code -ne 0) { throw 'Installation after closing the app failed.' }
    if ((Get-FileHash -LiteralPath $exe).Hash -ine $expectedHash -or
        (Get-FileHash -LiteralPath $database).Hash -ine $dbHash -or
        (Get-Content -LiteralPath $sentinel -Raw).Trim() -cne 'retain synthetic profile') { throw 'Retry changed executable identity or profile data.' }
    $passed = $true
} finally {
    if ($null -ne $app) {
        if (-not $app.HasExited) { $app.Kill($true); $app.WaitForExit() }
        $app.Dispose()
    }
    if ($attempted -and (Test-Path -LiteralPath $key)) {
        Assert-FixtureIdentity
        $resolved = [IO.Path]::GetFullPath($install)
        if (-not $resolved.StartsWith([IO.Path]::GetFullPath($root) + [IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe fixture uninstall path.' }
        $uninstall = Join-Path $resolved 'unins000.exe'
        if ((Invoke-FixtureProcess $uninstall @('/VERYSILENT','/SUPPRESSMSGBOXES','/NORESTART',"/LOG=$root/uninstall.log")).Code -ne 0) { throw 'Fixture uninstall failed.' }
        if (Test-Path -LiteralPath $key) { throw 'Fixture uninstall registration remains.' }
    }
    if ((Get-ProductionIdentity) -cne $before) { throw 'Production installation identity or shortcut changed.' }
}
if ($passed) {
    @{ passed=$true; running_app_exit=7; closed_app_exit=0; installed_files_unchanged_when_blocked=$true; profile_retained=$true; production_identity_unchanged=$true; evidence=$root } |
        ConvertTo-Json | Tee-Object -FilePath (Join-Path $root 'result.json')
}
exit 0
