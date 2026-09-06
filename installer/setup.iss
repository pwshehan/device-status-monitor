; Local Device Monitor — Inno Setup script.
;
; Installs two executables into Program Files and registers the engine as a
; Windows service. Data lives in ProgramData and is deliberately never touched
; by an upgrade: binaries and history have different lifetimes, and losing a
; year of uptime records to a patch release would be unforgivable.
;
; Build:  iscc /DAppVersion=1.0.0 installer\setup.iss
; Expects, relative to the repo root:
;   dist\monitor-service.exe
;   dist\monitor-gui.exe
;   dist\MicrosoftEdgeWebview2Setup.exe   (optional; see WebView2 below)

#ifndef AppVersion
  #define AppVersion "0.0.0-dev"
#endif

#define AppName        "Local Device Monitor"
#define AppShortName   "LocalMonitor"
#define Publisher      "Shein.Engineer"
#define ServiceExe     "monitor-service.exe"
#define GuiExe         "monitor-gui.exe"
#define ServiceName    "LocalMonitorSvc"

[Setup]
AppId={{8C3E5B9A-2F41-4E6D-9B72-3A5C1D8E4F60}
AppName={#AppName}
AppVersion={#AppVersion}
AppPublisher={#Publisher}
DefaultDirName={autopf}\{#AppShortName}
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
OutputDir=..\dist
OutputBaseFilename=LocalMonitor-Setup-{#AppVersion}
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern

; A service can only be installed by an administrator, and the 64-bit layout is
; the only one shipped.
PrivilegesRequired=admin
ArchitecturesInstallIn64BitMode=x64compatible
ArchitecturesAllowed=x64compatible
MinVersion=10.0

; Detect a running dashboard through the Restart Manager rather than by naming
; a mutex: the shell's single-instance mutex is named by Tauri from the app
; identifier, so an AppMutex line here would name something nothing creates and
; silently never match. Restart Manager finds it by the file lock instead, and
; asks the user to close it rather than failing halfway through a file copy.
CloseApplications=yes
RestartApplications=no
UninstallDisplayName={#AppName}
UninstallDisplayIcon={app}\{#GuiExe}

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; GroupDescription: "Shortcuts:"; Flags: unchecked

; No "start with Windows" task here on purpose. This installer runs elevated,
; so {userstartup} would be the *administrator's* startup folder rather than
; the person who actually uses the machine — a shortcut that silently does
; nothing for them. The dashboard offers the same option on its Service page,
; where it writes the setting for the user who is really signed in. The
; service itself always starts with the machine, signed in or not.

[Files]
Source: "..\dist\{#ServiceExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\dist\{#GuiExe}"; DestDir: "{app}"; Flags: ignoreversion

; WebView2 is present on Windows 11 and on any updated Windows 10, so the
; bootstrapper is only shipped when the build produced one and only run when
; the runtime is actually missing.
Source: "..\dist\MicrosoftEdgeWebview2Setup.exe"; DestDir: "{tmp}"; \
    Flags: deleteafterinstall skipifsourcedoesntexist; Check: not WebView2Installed

[Icons]
Name: "{group}\{#AppName}"; Filename: "{app}\{#GuiExe}"
Name: "{group}\Uninstall {#AppName}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#GuiExe}"; Tasks: desktopicon

[Run]
Filename: "{tmp}\MicrosoftEdgeWebview2Setup.exe"; Parameters: "/silent /install"; \
    StatusMsg: "Installing the WebView2 runtime..."; \
    Flags: waituntilterminated skipifdoesntexist; Check: not WebView2Installed

; The service registers itself rather than being created with sc.exe: binPath
; quoting is a classic source of installers that appear to succeed and leave a
; service that cannot start, and sc.exe cannot set failure actions in one step.
Filename: "{app}\{#ServiceExe}"; Parameters: "install"; \
    StatusMsg: "Registering the monitoring service..."; Flags: runhidden waituntilterminated
Filename: "{app}\{#ServiceExe}"; Parameters: "start"; \
    StatusMsg: "Starting the monitoring service..."; Flags: runhidden waituntilterminated

Filename: "{app}\{#GuiExe}"; Description: "Open the dashboard"; \
    Flags: postinstall nowait skipifsilent

[UninstallRun]
; Stop and deregister before the files go, or the exe is locked and the service
; is left behind pointing at a path that no longer exists.
Filename: "{app}\{#ServiceExe}"; Parameters: "uninstall"; \
    Flags: runhidden waituntilterminated; RunOnceId: "RemoveService"

[Code]
var
  KeepDataPage: TInputOptionWizardPage;

function WebView2Installed: Boolean;
var
  Version: String;
begin
  // Either key may hold it: per-machine installs land under WOW6432Node, and
  // per-user ones under HKCU.
  Result :=
    RegQueryStringValue(HKEY_LOCAL_MACHINE,
      'SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}',
      'pv', Version) or
    RegQueryStringValue(HKEY_CURRENT_USER,
      'SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}',
      'pv', Version);
  if Result then
    Result := (Version <> '') and (Version <> '0.0.0.0');
end;

function ServiceInstalled: Boolean;
var
  ResultCode: Integer;
begin
  Result := Exec(ExpandConstant('{sys}\sc.exe'), 'query {#ServiceName}', '',
    SW_HIDE, ewWaitUntilTerminated, ResultCode) and (ResultCode = 0);
end;

// An upgrade has to stop the running service first: Windows will not replace a
// file that a running process has open, and the failure looks like a corrupt
// download rather than a locked exe.
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ResultCode: Integer;
begin
  Result := '';
  NeedsRestart := False;
  if not ServiceInstalled then
    Exit;

  Exec(ExpandConstant('{sys}\sc.exe'), 'stop {#ServiceName}', '',
    SW_HIDE, ewWaitUntilTerminated, ResultCode);

  // sc.exe returns as soon as the stop is accepted, so wait for the service to
  // actually let go of its files. The engine flushes its last batch of
  // heartbeats on the way out, which takes a moment.
  Sleep(3000);
end;

procedure InitializeWizard;
begin
  KeepDataPage := CreateInputOptionPage(wpSelectTasks,
    'Monitoring history',
    'What should happen to the recorded history when this is uninstalled?',
    'The database holds every check, incident and daily summary. It is kept in ' +
    'ProgramData and is never touched by an upgrade.',
    True, False);
  KeepDataPage.Add('Keep the history (recommended)');
  KeepDataPage.Add('Delete everything, including the database and logs');
  KeepDataPage.SelectedValueIndex := 0;
end;

// The uninstaller cannot read the install-time wizard, so the choice is
// recorded where it can find it later.
procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
    RegWriteDWordValue(HKEY_LOCAL_MACHINE, 'SOFTWARE\{#Publisher}\{#AppShortName}',
      'KeepDataOnUninstall', Integer(KeepDataPage.SelectedValueIndex = 0));
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Keep: Cardinal;
  DataDir: String;
begin
  if CurUninstallStep <> usPostUninstall then
    Exit;

  DataDir := ExpandConstant('{commonappdata}\{#AppShortName}');

  // Default to keeping: if the registry value is missing for any reason, the
  // safe reading of an ambiguous instruction is not to delete a year of
  // history.
  if not RegQueryDWordValue(HKEY_LOCAL_MACHINE, 'SOFTWARE\{#Publisher}\{#AppShortName}',
      'KeepDataOnUninstall', Keep) then
    Keep := 1;

  // A staged installer goes either way. "Keep the history" means the record of
  // what was up and what was down; it does not mean a download the updater left
  // behind, which is fifteen megabytes of no use to anyone once the thing it
  // would have updated is gone.
  DelTree(DataDir + '\updates', True, True, True);

  if Keep = 0 then
    DelTree(DataDir, True, True, True)
  else if DirExists(DataDir) then
    MsgBox('The monitoring history has been left in:' + #13#10 + DataDir + #13#10#13#10 +
      'Delete that folder by hand if you no longer need it.', mbInformation, MB_OK);

  RegDeleteKeyIncludingSubkeys(HKEY_LOCAL_MACHINE, 'SOFTWARE\{#Publisher}\{#AppShortName}');
end;
