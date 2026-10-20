#ifndef MyAppVersion
  #error MyAppVersion must be supplied by scripts/package.ps1.
#endif
#ifndef ArtifactsDir
  #error ArtifactsDir must be supplied by scripts/package.ps1.
#endif
#ifndef RepoRoot
  #error RepoRoot must be supplied by scripts/package.ps1.
#endif

[Setup]
AppId={{B2075B47-8E23-4A2A-9D74-4B1DA9BC68C1}
AppName=Pokegents
AppVersion={#MyAppVersion}
AppPublisher=4getkun
DefaultDirName={localappdata}\Programs\Pokegents
DefaultGroupName=Pokegents
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputBaseFilename=Pokegents-Setup-{#MyAppVersion}-x64
VersionInfoVersion={#MyAppVersion}.0
VersionInfoProductName=Pokegents
VersionInfoDescription=Pokegents for Windows installer
UninstallDisplayName=Pokegents
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
DisableProgramGroupPage=yes
CloseApplications=no
RestartApplications=no
#ifdef ReleaseSigning
SignTool=pokegents_release_sign
SignedUninstaller=yes
#endif

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"
Name: "japanese"; MessagesFile: "compiler:Languages\Japanese.isl"

[CustomMessages]
english.AppCannotBeUpdated=Pokegents.exe is in use or cannot be opened for updating. Close Pokegents, including any background instance, then try again. If it is already closed, check write permissions for the installation folder. No application files have been replaced.
japanese.AppCannotBeUpdated=Pokegents.exe が使用中、または更新用に開けません。バックグラウンドの起動分も含めて Pokegents を終了し、再試行してください。終了済みの場合は、インストール先の書き込み権限を確認してください。アプリのファイルはまだ置き換えていません。

[Files]
Source: "{#ArtifactsDir}\Pokegents.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#RepoRoot}\LICENSE"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#RepoRoot}\THIRD_PARTY_NOTICES.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#ArtifactsDir}\notices\*"; DestDir: "{app}\notices"; Flags: ignoreversion recursesubdirs createallsubdirs
Source: "{#ArtifactsDir}\license-inventory.json"; DestDir: "{app}\notices"; Flags: ignoreversion
Source: "{#ArtifactsDir}\sbom-npm.json"; DestDir: "{app}\notices"; Flags: ignoreversion
Source: "{#ArtifactsDir}\go-module-inventory.json"; DestDir: "{app}\notices"; Flags: ignoreversion
Source: "{#ArtifactsDir}\go-build-info.txt"; DestDir: "{app}\notices"; Flags: ignoreversion
Source: "{#RepoRoot}\dashboard\web\src\assets\fonts\mplus1p\OFL.txt"; DestDir: "{app}\notices\fonts\mplus1p"; Flags: ignoreversion
Source: "{#RepoRoot}\dashboard\web\src\assets\fonts\zenkakugothicnew\OFL.txt"; DestDir: "{app}\notices\fonts\zenkakugothicnew"; Flags: ignoreversion
Source: "{#RepoRoot}\dashboard\web\src\assets\fonts\notosansjp\OFL.txt"; DestDir: "{app}\notices\fonts\notosansjp"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\Pokegents"; Filename: "{app}\Pokegents.exe"; WorkingDir: "{app}"; AppUserModelID: "4getkun.Pokegents"

[Code]
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  AppPath: String;
  Probe: TFileStream;
begin
  Result := '';
  AppPath := ExpandConstant('{app}\Pokegents.exe');
  if not FileExists(AppPath) then
    Exit;

  { Opening an existing file does not truncate or write it. Windows refuses
    write access to a running executable, including older app versions. }
  try
    Probe := TFileStream.Create(AppPath, fmOpenReadWrite or fmShareDenyNone);
    Probe.Free;
  except
    Log('Executable update preflight failed: ' + GetExceptionMessage);
    Result := CustomMessage('AppCannotBeUpdated');
  end;
end;
