[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$InstallerPath,
    [string]$PreviousInstallerPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$artifactDir = Join-Path $repoRoot 'artifacts'
$logsDir = Join-Path $artifactDir 'logs'
$builtExe = Join-Path $artifactDir 'Pokegents.exe'
$manifestPath = Join-Path $artifactDir 'build-manifest.json'
$appId = '{B2075B47-8E23-4A2A-9D74-4B1DA9BC68C1}'
$uninstallKeyPath = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\${appId}_is1"
$programsPath = [Environment]::GetFolderPath([Environment+SpecialFolder]::Programs)
$shortcutPath = Join-Path $programsPath 'Pokegents.lnk'

if (-not [System.IO.Path]::IsPathFullyQualified($InstallerPath) -or
    [System.IO.Path]::GetExtension($InstallerPath) -cne '.exe' -or
    -not (Test-Path -LiteralPath $InstallerPath -PathType Leaf)) {
    throw 'InstallerPath must be an absolute path to an existing installer .exe.'
}
if ($PreviousInstallerPath -and (-not [System.IO.Path]::IsPathFullyQualified($PreviousInstallerPath) -or
    [System.IO.Path]::GetExtension($PreviousInstallerPath) -cne '.exe' -or
    -not (Test-Path -LiteralPath $PreviousInstallerPath -PathType Leaf))) {
    throw 'PreviousInstallerPath must be an absolute path to an existing installer .exe.'
}
if (Test-Path -LiteralPath $uninstallKeyPath -PathType Container) {
    throw "The Pokegents uninstall entry already exists; no smoke-test files were changed: $uninstallKeyPath"
}
if (Test-Path -LiteralPath $shortcutPath -PathType Leaf) {
    throw "The Pokegents Start Menu shortcut already exists; no smoke-test files were changed: $shortcutPath"
}
foreach ($required in @($builtExe, $manifestPath)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Required built artifact is missing: $required"
    }
}

$testId = [guid]::NewGuid().ToString('N')
$testRoot = Join-Path $artifactDir "installer-smoke-$testId"
$installDir = Join-Path $testRoot 'アプリ 日本語 &()[]'
$profileDir = Join-Path $testRoot 'profile-data'
$sentinelPath = Join-Path $profileDir 'keep-after-uninstall.txt'
$installLog = Join-Path $testRoot 'install.log'
$upgradeLog = Join-Path $testRoot 'upgrade.log'
$uninstallLog = Join-Path $testRoot 'uninstall.log'
$resultPath = Join-Path $logsDir "installer-smoke-$testId.json"
if (Test-Path -LiteralPath $testRoot) {
    throw "Generated smoke-test root unexpectedly already exists: $testRoot"
}

function Get-NormalizedPath {
    param([Parameter(Mandatory)][string]$Path)
    return [System.IO.Path]::GetFullPath($Path).TrimEnd([char[]]@('\', '/'))
}

function Test-PathWithin {
    param(
        [Parameter(Mandatory)][string]$Candidate,
        [Parameter(Mandatory)][string]$Parent
    )
    $candidateFull = Get-NormalizedPath $Candidate
    $parentFull = Get-NormalizedPath $Parent
    $parentPrefix = $parentFull + [System.IO.Path]::DirectorySeparatorChar
    return $candidateFull.StartsWith($parentPrefix, [System.StringComparison]::OrdinalIgnoreCase)
}

function Invoke-OwnedProcess {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$Arguments,
        [Parameter(Mandatory)][string]$LogPath,
        [int]$TimeoutSeconds = 60
    )

    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $FilePath
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Hidden
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($argument in $Arguments) {
        $startInfo.ArgumentList.Add($argument)
    }

    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    try {
        if (-not $process.Start()) {
            throw "Could not start helper process: $FilePath"
        }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
            try { $process.Kill() } catch { }
            [void]$process.WaitForExit(5000)
            throw "Timed out after $TimeoutSeconds seconds; only the owned helper process was terminated: $FilePath"
        }
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        [System.IO.File]::WriteAllText($LogPath + '.process.log', $stdout + $stderr, [System.Text.UTF8Encoding]::new($false))
        if ($process.ExitCode -ne 0) {
            throw "Helper process failed with exit code $($process.ExitCode): $FilePath; see $LogPath"
        }
        return [pscustomobject]@{ ExitCode = $process.ExitCode; StdOut = $stdout; StdErr = $stderr }
    }
    finally {
        $process.Dispose()
    }
}

function Get-InstalledLocation {
    if (-not (Test-Path -LiteralPath $uninstallKeyPath -PathType Container)) {
        return $null
    }
    return (Get-ItemProperty -LiteralPath $uninstallKeyPath -Name InstallLocation -ErrorAction Stop).InstallLocation
}

function Assert-InstalledIdentity {
    param([Parameter(Mandatory)][string]$ExpectedInstallDir)
    if (-not (Test-Path -LiteralPath $uninstallKeyPath -PathType Container)) {
        throw 'Expected Pokegents uninstall registry entry is missing.'
    }
    $registryLocation = Get-InstalledLocation
    if ((Get-NormalizedPath $registryLocation) -cne (Get-NormalizedPath $ExpectedInstallDir)) {
        throw "Uninstall registry InstallLocation mismatch: $registryLocation"
    }
    $exe = Join-Path $ExpectedInstallDir 'Pokegents.exe'
    if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) {
        throw "Installed executable is missing: $exe"
    }
    if (-not (Test-Path -LiteralPath $shortcutPath -PathType Leaf)) {
        throw "Installed Start Menu shortcut is missing: $shortcutPath"
    }
    $shell = New-Object -ComObject WScript.Shell
    try {
        $shortcut = $shell.CreateShortcut($shortcutPath)
        if ((Get-NormalizedPath $shortcut.TargetPath) -cne (Get-NormalizedPath $exe)) {
            throw "Shortcut target is not the installed executable: $($shortcut.TargetPath)"
        }
    }
    finally {
        [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($shell)
    }
}

function Get-InstallerVersion {
    param([Parameter(Mandatory)][string]$Path)
    $fileName = [System.IO.Path]::GetFileName($Path)
    if ($fileName -cnotmatch '^Pokegents-Setup-(?<version>(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*))-x64\.exe$') {
        throw "Installer filename must include a release version: Pokegents-Setup-x.y.z-x64.exe ($fileName)"
    }
    return $Matches.version
}

function Assert-ProfileRetained {
    param(
        [Parameter(Mandatory)][string]$DatabasePath,
        [Parameter(Mandatory)][string]$Sentinel,
        [Parameter(Mandatory)][string]$UnownedFile
    )
    foreach ($path in @($DatabasePath, $Sentinel, $UnownedFile)) {
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "Version transition removed retained data: $path"
        }
    }
}

$result = [ordered]@{
    schema_version = 1
    test_id = $testId
    started_at_utc = (Get-Date).ToUniversalTime().ToString('o')
    installer = [System.IO.Path]::GetFullPath($InstallerPath)
    test_root = $testRoot
    install_dir = $installDir
    profile_dir = $profileDir
    status = 'running'
    completed_stages = @()
    cleanup = 'not-needed'
    error = $null
}
$installed = $false
$installationAttempted = $false
$uninstalled = $false
$failure = $null

try {
    New-Item -ItemType Directory -Path $testRoot | Out-Null
    New-Item -ItemType Directory -Path $installDir | Out-Null
    New-Item -ItemType Directory -Path $logsDir -Force | Out-Null

    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    if ($manifest.target -cne 'windows/amd64' -or $manifest.executable -cne 'Pokegents.exe' -or
        $manifest.app_version -notmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' -or
        $manifest.sha256 -cnotmatch '^[0-9a-fA-F]{64}$') {
        throw 'Build manifest does not describe the expected Windows x64 executable.'
    }
    $currentVersion = Get-InstallerVersion -Path $InstallerPath
    if ($currentVersion -cne $manifest.app_version) {
        throw 'Installer filename version does not match build-manifest.json app_version.'
    }
    $previousVersion = $null
    if ($PreviousInstallerPath) {
        $previousVersion = Get-InstallerVersion -Path $PreviousInstallerPath
        if ($previousVersion -ceq $currentVersion) {
            throw 'PreviousInstallerPath must use a different version from InstallerPath.'
        }
    }
    $expectedHash = (Get-FileHash -LiteralPath $builtExe -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($expectedHash -cne $manifest.sha256.ToLowerInvariant()) {
        throw 'Build manifest SHA256 does not match artifacts/Pokegents.exe.'
    }
    $result.expected_executable_sha256 = $expectedHash
    $result.installer_version = $currentVersion
    $result.previous_installer_version = $previousVersion
    $result.completed_stages += 'preflight'

    $installArgs = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/LANG=japanese', "/DIR=$installDir", "/LOG=$installLog")
    $installationAttempted = $true
    if ($PreviousInstallerPath) {
        $previousInstallLog = Join-Path $testRoot 'previous-install.log'
        $previousArgs = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/LANG=japanese', "/DIR=$installDir", "/LOG=$previousInstallLog")
        [void](Invoke-OwnedProcess -FilePath ([System.IO.Path]::GetFullPath($PreviousInstallerPath)) -Arguments $previousArgs -LogPath $previousInstallLog)
        $installed = $true
        Assert-InstalledIdentity $installDir
        $installedExe = Join-Path $installDir 'Pokegents.exe'
        $previousHash = (Get-FileHash -LiteralPath $installedExe -Algorithm SHA256).Hash.ToLowerInvariant()
        $result.previous_executable_sha256 = $previousHash
        $previousVerify = Invoke-OwnedProcess -FilePath $installedExe -Arguments @('--verify', '--data-dir', $profileDir) -LogPath (Join-Path $testRoot 'previous-verify.log')
        if ($previousVerify.StdOut -notmatch '"database":"ok"' -or $previousVerify.StdOut -notmatch '"embedded_ui":"ok"') {
            throw "Previous installed executable verification output was unexpected: $($previousVerify.StdOut)"
        }
        $profileDatabase = Join-Path $profileDir 'db\pokegents.db'
        [System.IO.File]::WriteAllText($sentinelPath, "preserve $testId`n", [System.Text.UTF8Encoding]::new($false))
        $unownedInstallFile = Join-Path $installDir 'user-created-keep.txt'
        [System.IO.File]::WriteAllText($unownedInstallFile, "unowned $testId`n", [System.Text.UTF8Encoding]::new($false))
        $result.completed_stages += 'previous-version-install'
        [void](Invoke-OwnedProcess -FilePath ([System.IO.Path]::GetFullPath($InstallerPath)) -Arguments $installArgs -LogPath $installLog)
        Assert-InstalledIdentity $installDir
        if ((Get-FileHash -LiteralPath $installedExe -Algorithm SHA256).Hash.ToLowerInvariant() -cne $expectedHash) {
            throw 'New-version upgrade executable SHA256 does not match the current build.'
        }
        Assert-ProfileRetained -DatabasePath $profileDatabase -Sentinel $sentinelPath -UnownedFile $unownedInstallFile
        $result.completed_stages += 'version-upgrade-preserved-data'
    }
    else {
        [void](Invoke-OwnedProcess -FilePath ([System.IO.Path]::GetFullPath($InstallerPath)) -Arguments $installArgs -LogPath $installLog)
        $installed = $true
        Assert-InstalledIdentity $installDir
        $result.completed_stages += 'install'
    }

    $installedExe = Join-Path $installDir 'Pokegents.exe'
    $installedHash = (Get-FileHash -LiteralPath $installedExe -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($installedHash -cne $expectedHash) {
        throw 'Installed executable SHA256 does not match artifacts/Pokegents.exe.'
    }
    $result.installed_executable_sha256 = $installedHash
    foreach ($relative in @(
        'LICENSE',
        'THIRD_PARTY_NOTICES.md',
        'notices\license-inventory.json',
        'notices\sbom-npm.json',
        'notices\go-module-inventory.json',
        'notices\go-build-info.txt',
        'notices\fonts\mplus1p\OFL.txt',
        'notices\fonts\zenkakugothicnew\OFL.txt',
        'notices\fonts\notosansjp\OFL.txt'
    )) {
        if (-not (Test-Path -LiteralPath (Join-Path $installDir $relative) -PathType Leaf)) {
            throw "Installed license/notice file is missing: $relative"
        }
    }
    $sourceNotices = Join-Path $artifactDir 'notices'
    foreach ($sourceFile in Get-ChildItem -LiteralPath $sourceNotices -Recurse -File) {
        $relative = [System.IO.Path]::GetRelativePath($sourceNotices, $sourceFile.FullName)
        if (-not (Test-Path -LiteralPath (Join-Path (Join-Path $installDir 'notices') $relative) -PathType Leaf)) {
            throw "A recursive third-party notice was not installed: $relative"
        }
    }
    $result.installed_notice_file_count = (Get-ChildItem -LiteralPath (Join-Path $installDir 'notices') -Recurse -File).Count
    $result.completed_stages += 'licenses-and-notices'

    $verifyResult = Invoke-OwnedProcess -FilePath $installedExe -Arguments @('--verify', '--data-dir', $profileDir) -LogPath (Join-Path $testRoot 'verify.log')
    if ($verifyResult.StdOut -notmatch '"database":"ok"' -or $verifyResult.StdOut -notmatch '"embedded_ui":"ok"') {
        throw "Installed executable verification output was unexpected: $($verifyResult.StdOut)"
    }
    $profileDatabase = Join-Path $profileDir 'db\pokegents.db'
    if (-not (Test-Path -LiteralPath $profileDatabase -PathType Leaf)) {
        throw "Synthetic profile database was not created: $profileDatabase"
    }
    if (-not (Test-Path -LiteralPath $sentinelPath -PathType Leaf)) {
        [System.IO.File]::WriteAllText($sentinelPath, "preserve $testId`n", [System.Text.UTF8Encoding]::new($false))
    }
    $unownedInstallFile = Join-Path $installDir 'user-created-keep.txt'
    if (-not (Test-Path -LiteralPath $unownedInstallFile -PathType Leaf)) {
        [System.IO.File]::WriteAllText($unownedInstallFile, "unowned $testId`n", [System.Text.UTF8Encoding]::new($false))
    }
    $result.completed_stages += 'installed-executable-verify'

    $upgradeArgs = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/LANG=japanese', "/DIR=$installDir", "/LOG=$upgradeLog")
    [void](Invoke-OwnedProcess -FilePath ([System.IO.Path]::GetFullPath($InstallerPath)) -Arguments $upgradeArgs -LogPath $upgradeLog)
    Assert-InstalledIdentity $installDir
    if ((Get-FileHash -LiteralPath $installedExe -Algorithm SHA256).Hash.ToLowerInvariant() -cne $expectedHash) {
        throw 'Executable hash changed unexpectedly after reinstall.'
    }
    if (-not (Test-Path -LiteralPath $sentinelPath -PathType Leaf) -or -not (Test-Path -LiteralPath $profileDatabase -PathType Leaf)) {
        throw 'Reinstall did not preserve the sibling synthetic profile and sentinel.'
    }
    $result.completed_stages += 'reinstall-preserved-profile'

    if ($PreviousInstallerPath) {
        $rollbackLog = Join-Path $testRoot 'rollback.log'
        $rollbackArgs = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/LANG=japanese', "/DIR=$installDir", "/LOG=$rollbackLog")
        [void](Invoke-OwnedProcess -FilePath ([System.IO.Path]::GetFullPath($PreviousInstallerPath)) -Arguments $rollbackArgs -LogPath $rollbackLog)
        Assert-InstalledIdentity $installDir
        $rollbackHash = (Get-FileHash -LiteralPath $installedExe -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($rollbackHash -cne $previousHash) {
            throw 'Rollback executable SHA256 does not match the previous installed version.'
        }
        Assert-ProfileRetained -DatabasePath $profileDatabase -Sentinel $sentinelPath -UnownedFile $unownedInstallFile
        $result.rollback_executable_sha256 = $rollbackHash
        $result.completed_stages += 'rollback-to-previous-version-preserved-data'

        $restoreLog = Join-Path $testRoot 'restore-current-version.log'
        $restoreArgs = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/LANG=japanese', "/DIR=$installDir", "/LOG=$restoreLog")
        [void](Invoke-OwnedProcess -FilePath ([System.IO.Path]::GetFullPath($InstallerPath)) -Arguments $restoreArgs -LogPath $restoreLog)
        Assert-InstalledIdentity $installDir
        if ((Get-FileHash -LiteralPath $installedExe -Algorithm SHA256).Hash.ToLowerInvariant() -cne $expectedHash) {
            throw 'Current-version restoration after rollback failed SHA256 verification.'
        }
        Assert-ProfileRetained -DatabasePath $profileDatabase -Sentinel $sentinelPath -UnownedFile $unownedInstallFile
        $result.completed_stages += 'current-version-restored-after-rollback'
    }

    $resolvedRoot = Get-NormalizedPath $testRoot
    $resolvedInstall = Get-NormalizedPath $installDir
    $registryInstall = Get-InstalledLocation
    $uninstaller = Join-Path $resolvedInstall 'unins000.exe'
    if (-not (Test-PathWithin -Candidate $resolvedInstall -Parent $resolvedRoot) -or
        (Get-NormalizedPath $registryInstall) -cne $resolvedInstall -or
        -not (Test-PathWithin -Candidate $uninstaller -Parent $resolvedRoot) -or
        -not (Test-PathWithin -Candidate $uninstaller -Parent $resolvedInstall) -or
        -not (Test-Path -LiteralPath $uninstaller -PathType Leaf)) {
        throw 'Refusing uninstall because the resolved app, uninstaller, test root, and registry location do not match.'
    }
    $uninstallArgs = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/LANG=japanese', "/LOG=$uninstallLog")
    [void](Invoke-OwnedProcess -FilePath $uninstaller -Arguments $uninstallArgs -LogPath $uninstallLog)
    # The uninstaller removes its recorded payload, retaining unowned files.
    # The launcher can exit just before its cleanup worker has finished.
    $cleanupDeadline = [DateTime]::UtcNow.AddSeconds(5)
    while ([DateTime]::UtcNow -lt $cleanupDeadline -and
        ((Test-Path -LiteralPath $installedExe) -or
         (Test-Path -LiteralPath $shortcutPath -PathType Leaf) -or
         (Test-Path -LiteralPath $uninstallKeyPath -PathType Container))) {
        Start-Sleep -Milliseconds 50
    }
    if ((Test-Path -LiteralPath $installedExe) -or
        (Test-Path -LiteralPath $shortcutPath -PathType Leaf) -or
        (Test-Path -LiteralPath $uninstallKeyPath -PathType Container)) {
        throw 'Uninstall left the app executable, shortcut, or uninstall registry entry behind.'
    }
    $uninstalled = $true
    if (-not (Test-Path -LiteralPath $profileDatabase -PathType Leaf) -or
        -not (Test-Path -LiteralPath $sentinelPath -PathType Leaf) -or
        -not (Test-Path -LiteralPath $unownedInstallFile -PathType Leaf)) {
        throw 'Uninstall removed the synthetic profile or unowned user-created files.'
    }
    $result.completed_stages += 'uninstall-preserved-profile'
    $result.status = 'passed'
}
catch {
    $failure = $_.Exception.Message
    $result.status = 'failed'
    $result.error = $failure
}
finally {
    if (-not $uninstalled -and $installationAttempted) {
        try {
            $resolvedRoot = Get-NormalizedPath $testRoot
            $resolvedInstall = Get-NormalizedPath $installDir
            $registryInstall = Get-InstalledLocation
            $uninstaller = Join-Path $resolvedInstall 'unins000.exe'
            $safeToUninstall = $registryInstall -and
                (Get-NormalizedPath $registryInstall) -ceq $resolvedInstall -and
                (Test-PathWithin -Candidate $resolvedInstall -Parent $resolvedRoot) -and
                (Test-PathWithin -Candidate $uninstaller -Parent $resolvedRoot) -and
                (Test-PathWithin -Candidate $uninstaller -Parent $resolvedInstall) -and
                (Test-Path -LiteralPath $uninstaller -PathType Leaf)
            if ($safeToUninstall) {
                $cleanupLog = Join-Path $testRoot 'failure-cleanup-uninstall.log'
                $cleanupArgs = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/LANG=japanese', "/LOG=$cleanupLog")
                [void](Invoke-OwnedProcess -FilePath $uninstaller -Arguments $cleanupArgs -LogPath $cleanupLog)
                $result.cleanup = 'owned-install-uninstalled; profile retained'
            }
            else {
                $result.cleanup = 'refused: ownership/path checks did not prove this test created the installation'
            }
        }
        catch {
            $result.cleanup = "failed: $($_.Exception.Message)"
        }
    }
    $result.finished_at_utc = (Get-Date).ToUniversalTime().ToString('o')
    $result.result_path = $resultPath
    ConvertTo-Json -InputObject $result -Depth 6 | Set-Content -LiteralPath $resultPath -Encoding utf8
}

if ($failure) {
    throw "Installer smoke failed; test data and diagnostics retained under $testRoot. $failure"
}
Write-Host "Installer smoke passed. Test data retained under $testRoot. Result: $resultPath"
