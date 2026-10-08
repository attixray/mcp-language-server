; Compile through scripts/package_windows_installer.py. Keep AppId unchanged
; between releases: Inno uses it to reuse the install directory/uninstall entry.
#ifndef PayloadDir
  #error PayloadDir is required
#endif
#ifndef ReleaseVersion
  #error ReleaseVersion is required
#endif
#ifndef NumericVersion
  #error NumericVersion is required
#endif
#ifndef InstallerName
  #error InstallerName is required
#endif
#ifndef AppGuid
  #define AppGuid "DFDF80E9-710A-4CDF-BC82-B5AF3D54F037"
#endif

[Setup]
AppId={{{#AppGuid}}
AppName=MCP Language Server (attixray)
AppVersion={#ReleaseVersion}
AppPublisher=attixray
AppPublisherURL=https://github.com/attixray/mcp-language-server
AppSupportURL=https://github.com/attixray/mcp-language-server/issues
DefaultDirName={localappdata}\Programs\mcp-language-server
DisableProgramGroupPage=yes
DisableDirPage=auto
UsePreviousAppDir=yes
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
OutputBaseFilename={#InstallerName}
VersionInfoVersion={#NumericVersion}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
LicenseFile={#PayloadDir}\LICENSE
UninstallDisplayIcon={app}\mcp-language-server.exe
; Never terminate agents/brokers, restart a headless server, or schedule a
; locked executable for replacement at reboot. PrepareToInstall checks first.
CloseApplications=no
RestartApplications=no
SetupMutex=attixray-mcp-language-server-{#AppGuid}-setup
UninstallDisplayName=MCP Language Server (attixray)

[Files]
Source: "{#PayloadDir}\mcp-language-server.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#PayloadDir}\LICENSE"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#PayloadDir}\README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#PayloadDir}\BUILD-INFO.json"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#PayloadDir}\DesignTimeIsolation.targets"; DestDir: "{app}"; Flags: ignoreversion

[Code]
// Inno Setup 6 runs a 32-bit installer, even for an x64 payload.
function CreateFileW(Name: String; Access, Share: Cardinal;
  Security: Integer; Disposition, Flags: Cardinal; Template: Integer): Integer;
  external 'CreateFileW@kernel32.dll stdcall';
function CloseHandle(Handle: Integer): Boolean;
  external 'CloseHandle@kernel32.dll stdcall';

function CheckExecutable: String;
var
  Filename: String;
  Handle: Integer;
begin
  Result := '';
  Filename := ExpandConstant('{app}\mcp-language-server.exe');
  if not FileExists(Filename) then exit;
  // GENERIC_WRITE + no sharing rejects a mapped/running EXE as well as other
  // file locks. Opening an existing file does not modify its contents.
  Handle := CreateFileW(Filename, $40000000, 0, 0, 3, $80, 0);
  if Handle = -1 then
    Result := 'Cannot replace the installed MCP executable. Stop its MCP ' +
      'connections and wait for the shared broker to exit (normally within ' +
      'two minutes), then retry. No processes have been stopped by Setup. ' +
      'Also check directory write permissions.' + #13#10 + Filename
  else
    CloseHandle(Handle);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  Result := CheckExecutable;
end;

function InitializeUninstall: Boolean;
var
  Error: String;
begin
  Error := CheckExecutable;
  Result := Error = '';
  if not Result then
    SuppressibleMsgBox(Error, mbError, MB_OK, IDOK);
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  if CurPageID = wpFinished then
    WizardForm.FinishedLabel.Caption := 'MCP Language Server is installed.' + #13#10#13#10 +
      'Use this executable as your MCP command:' + #13#10 +
      ExpandConstant('{app}\mcp-language-server.exe') + #13#10#13#10 +
      'Restart the MCP connection after upgrading. Install your chosen ' +
      'language server separately.';
end;
