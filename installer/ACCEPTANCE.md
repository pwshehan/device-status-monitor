# Windows acceptance checklist

Everything in this repository is covered by automated tests **except the things
that need a real Windows machine to install on**: the service registering with
the SCM, running as `LocalSystem`, surviving a reboot, and the installer's own
behaviour. This is that gap, written out so it can be run in about fifteen
minutes.

Run it on a **clean Windows 11 VM** if one exists. A working machine will cover
everything except the reboot step honestly — you cannot reboot a machine you are
using and call the result a controlled test.

Build the installer first (needs Go, Node 24, Rust and Inno Setup 6):

```
make release-local VERSION=0.5.0
```

It lands in `dist\LocalMonitor-Setup-0.5.0.exe`.

---

## 1. Install

Run the installer as an administrator. SmartScreen will say **"Windows
protected your PC"** — that is expected, the build is deliberately unsigned
(PLAN.md §11). Choose *More info → Run anyway*.

- [ ] The wizard offers the **"Monitoring history"** page, defaulting to *Keep*
- [ ] It finishes without an error dialog
- [ ] `C:\Program Files\LocalMonitor\` holds `monitor-service.exe` and `monitor-gui.exe`
- [ ] `C:\ProgramData\LocalMonitor\` holds `monitor.db`, `api.token` and `logs\`

## 2. The service

```
sc.exe qc LocalMonitorSvc
sc.exe query LocalMonitorSvc
```

- [ ] `START_TYPE` is `AUTO_START`
- [ ] `SERVICE_START_NAME` is `LocalSystem`
- [ ] `BINARY_PATH_NAME` points at `C:\Program Files\LocalMonitor\monitor-service.exe`
      — quoted correctly despite the space in the path
- [ ] `STATE` is `RUNNING`

```
sc.exe qfailure LocalMonitorSvc
```

- [ ] Restart after 5 s, then 10 s, then 30 s; reset counter after one day

## 3. It is actually monitoring

```
curl http://127.0.0.1:49215/api/health
```

- [ ] Answers without a token, `"ok": true`, `"db_ok": true`
- [ ] `"maintenance"` is **not null** — the janitor ran its startup pass

As your **normal (non-administrator) user**:

- [ ] `type C:\ProgramData\LocalMonitor\api.token` succeeds — this is what lets
      the dashboard authenticate without being elevated
- [ ] The Start Menu entry opens the dashboard, and it shows devices rather than
      the "service is not running" banner

## 4. An outage, end to end

Add a device pointing at something you can unplug or block, wait for it to be
seen UP, then break it.

- [ ] A `[DOWN]` email arrives in about `failure_threshold × interval` plus the
      collapse window — ~105 s on the defaults
- [ ] The dashboard badge turns red within a second or two of the transition
- [ ] Restore it: an `[UP]` email arrives with a plausible downtime
- [ ] The incident appears on the device page with that duration

## 5. Reboot

```
shutdown /r /t 0
```

- [ ] After the machine comes back, **without anyone signing in**, the service is
      `RUNNING` — check with `sc.exe query LocalMonitorSvc` from another machine
      or after signing in
- [ ] `/api/health` shows a fresh `uptime_sec` and the device history from before
      the reboot is still there
- [ ] No `LocalMonitorSvc` errors in Event Viewer → Windows Logs → Application

## 6. Upgrade

Build a second installer with a higher version and run it over the top.

- [ ] The installer stops the running service rather than failing on a locked file
- [ ] It completes, and the service is running again afterwards
- [ ] **The database survived**: the device list and history are unchanged
- [ ] `monitor-service.exe version` reports the new version

## 6a. Upgrade from inside the dashboard

The other half of §6, and the one nothing in CI can reach: the service
downloading a release, checking it, and running the installer as `LocalSystem`
with nobody watching.

Needs a real published release newer than the installed version.

- [ ] With the older version installed, the **Service** page shows the newer one
      as available, then as downloaded and checked
- [ ] `C:\ProgramData\LocalMonitor\updates\` holds one
      `LocalMonitor-Setup-<version>.exe` and nothing else
- [ ] Its ACL lists **only** SYSTEM and Administrators — check with
      `icacls C:\ProgramData\LocalMonitor\updates`. A standard user able to write
      there could hand themselves a SYSTEM shell
- [ ] Pressing **Install now** returns immediately and the page says it is
      installing
- [ ] The dashboard loses the service for a few seconds, then reconnects on its
      own — no restart of the window, no reboot
- [ ] `sc.exe query LocalMonitorSvc` reports RUNNING again
- [ ] `monitor-service.exe version` reports the new version
- [ ] The Service page shows the new version with no update pending
- [ ] The staged installer is **gone** from `updates\` — the new service tidies
      up after the old one's work
- [ ] **The database survived**: the device list and history are unchanged
- [ ] `C:\ProgramData\LocalMonitor\logs\update-install.log` records a clean
      silent install
- [ ] Turning both settings off under **Settings → Updates** stops the checking:
      nothing new appears on the Service page and nothing lands in `updates\`

## 7. Uninstall

Uninstall from Settings → Apps.

- [ ] The service is gone: `sc.exe query LocalMonitorSvc` reports "does not exist"
- [ ] `C:\Program Files\LocalMonitor\` is gone
- [ ] `C:\ProgramData\LocalMonitor\` **is still there**, and a message said so —
      the history outliving the software is the intended behaviour
- [ ] Reinstalling picks the existing database back up

---

## If something fails

The service has no console, so start with:

- **Event Viewer → Windows Logs → Application**, source `LocalMonitorSvc` —
  where a failure to start is reported
- `C:\ProgramData\LocalMonitor\logs\monitor.log` — everything else
- `%TEMP%\Setup Log*.txt` — Inno Setup's own log, if the installer failed
- `C:\ProgramData\LocalMonitor\logs\update-install.log` — the installer the
  service launched on its own, for §6a
