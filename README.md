# Local Device Monitor

Watches TCP endpoints on your network and emails you when one stops answering.

A Windows service does the probing, alerting and record-keeping; a desktop
dashboard is a thin client over a loopback API. Closing the dashboard — or
never opening it — changes nothing about what is being watched.

- **Probes** any TCP endpoint: a switch on `:22`, a NAS on `:445`, a printer on
  `:9100`, a database on `:1433`.
- **Alerts by email** when a device goes down and again when it recovers, with
  the real downtime measured from an incident record rather than guessed.
- **Groups devices by site**, so probe settings and alert recipients are set
  once per site and a whole site failing is *one* email rather than eight.
- **Keeps history**: every check for two weeks, daily summaries for a year, and
  a 90-day availability strip per device and per site.
- **Stays bounded**: aggregates and prunes itself, so the database does not grow
  without limit.

Built for a single Windows machine watching a single site's worth of kit — up
to a few hundred devices. See [PLAN.md](PLAN.md) for the architecture and the
reasoning behind it.

---

## Installing

Download `LocalMonitor-Setup-<version>.exe` from the
[releases page](https://github.com/pwshehan/device-status-monitor/releases) and
run it as an administrator.

Builds are **unsigned** — this is an internal tool, so SmartScreen will say
*"Windows protected your PC"*. Choose **More info → Run anyway**, and check the
download against the `SHA256SUMS` attached to the release if you want to be
certain of what you got.

The installer:

- puts `monitor-service.exe` and `monitor-gui.exe` in `C:\Program Files\LocalMonitor`
- registers **`LocalMonitorSvc`** to start with the machine, as `LocalSystem`
- starts it, and adds **Local Device Monitor** to the Start Menu

Requires Windows 10 or 11, 64-bit. WebView2 is needed for the dashboard and
ships with Windows 11; the installer adds it if it is missing.

**Upgrading** is the same installer over the top: it stops the service,
replaces the two executables and starts it again. Your history is not touched.

**Uninstalling** removes the service and the programs, and *keeps* the history
unless you chose otherwise during setup.

---

## Using it

Open **Local Device Monitor** from the Start Menu. Closing the window hides it
to the tray; the tray's Quit does not stop monitoring — only
`monitor-service stop` does that.

### Adding a device

**Add device**, then a name, an address and a port. Everything else is
optional: a device with no settings of its own inherits them, which is the
point of the next section.

Use **Test connection now** on an existing device to probe it immediately
without touching its history — the quickest way to find a wrong port.

### Groups, and why settings are usually blank

A device belongs to at most one group, and a group is a site: *Head Office*,
*Warehouse*. Four probe settings resolve down a chain:

```
device  →  its group  →  the global default
```

A blank field means *inherit*. The form shows what it would inherit as
placeholder text — `15 (from Warehouse)` — so you can always see which tier a
value is coming from, and the device page spells it out in full.

That is why groups are worth using even for a handful of devices:

- **Alert routing.** Give *Warehouse* its own recipients and everything in it
  pages whoever looks after the warehouse.
- **Maintenance windows.** Pause a group and nothing in it is probed or
  alerted on. A device you had paused individually stays paused afterwards.
- **One email per site.** Three or more devices in a group failing together
  become a single message naming each one.

Deleting a group never deletes its devices — they become *Ungrouped* and keep
all of their history.

### How a device is decided to be down

A device is only *DOWN* after `failure_threshold` consecutive failed probes —
three by default, so a single dropped packet is not an outage. Recovery is the
mirror image, and defaults to one success.

Two consequences worth knowing:

- **The clock starts at the first failure, not the third.** Reported downtime
  is the real downtime.
- **A device that has never been seen working does not alert.** It records the
  outage and shows red on the dashboard, but no email — because a device you
  just added with the wrong port is a typo, not an incident.

With the defaults, an outage is emailed about 105 seconds after the device
stops answering: 90 to confirm it, then the collapse window (below).

### Email

**Settings** → SMTP. Providers with multi-factor authentication almost always
need an *app password* rather than the account password.

| Port | Encryption |
|---|---|
| 587 | STARTTLS |
| 465 | TLS |
| 25 | None — an internal relay |

**Send test email** delivers immediately, bypassing the retry queue, and shows
you exactly what the mail server said. `535 5.7.8 Username and Password not
accepted` means it wants an app password. Save before testing: it uses the
stored settings, not what is on screen.

Alerts are queued and retried (1 m → 5 m → 15 m → 1 h → 4 h, then given up on
and logged), because the mail announcing a network outage often cannot be sent
over that outage.

---

## Alert volume

Two mechanisms stop a bad day producing a hundred emails.

**Collapsing.** Three or more devices in one group failing inside the collapse
window become one message — `[DOWN] Warehouse — 6 of 8 devices unreachable` —
naming every device. Recovery collapses the same way. Ten devices failing
across *unrelated* groups collapse into one global message, because that has a
single cause upstream of all of them.

This is the one place speed is traded for readability: an alert waits out the
window (15 s by default) before going out. A group whose every member has
already failed goes at once, since nothing more can arrive. Set
**Group failures together** to `0` for immediate per-device email.

**The hourly cap** (20 by default). At the cap, one message says mail is paused
and the rest is withheld — never dropped silently, because email that stops
without explanation reads as *all clear*. Every incident is still recorded and
still on the dashboard.

---

## Where things are

```
C:\Program Files\LocalMonitor\
    monitor-service.exe          the engine — probes, alerts, API
    monitor-gui.exe              the dashboard

C:\ProgramData\LocalMonitor\
    monitor.db                   SQLite (plus -wal and -shm while running)
    api.token                    what the dashboard authenticates with
    logs\monitor.log             rotating, 10 MB × 7, compressed
```

Binaries and data are deliberately separate: an upgrade replaces the first and
never touches the second.

Failures to *start* also go to **Event Viewer → Windows Logs → Application**,
source `LocalMonitorSvc` — a service has no console, and that is where an
administrator looks first. Only warnings and errors go there; the routine
detail is in the log file.

### History and retention

Two hundred devices on a 30-second interval write about 576 000 rows a day, so
an hourly pass aggregates and prunes:

- A **finished** day becomes one summary row per device. Today is never
  aggregated — a half-finished day would be frozen as the whole day's numbers —
  so today comes from raw checks and everything older from summaries.
- **Downtime comes from the incident log**, clipped to each local day, not from
  counting failed checks. Counting assumes the interval never changed, loses
  the gap around a restart, and cannot split an outage that spans midnight.
- Raw checks are pruned past **`retention.raw_days`** (14) in chunks, so the
  delete never stalls the writer. Their summaries survive, so the 90-day strip
  keeps working. Summaries and resolved incidents age out at
  **`retention.rollup_days`** (400) — but an **open** incident is never pruned.

The **Service** page shows the last maintenance pass. If it says *not yet run*
after the service has been up a while, retention is not running and the
database is growing — see [troubleshooting](docs/TROUBLESHOOTING.md).

---

## When something is wrong

[docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) covers the failures this
system actually has: the service not starting, email not arriving, the
dashboard saying the service is down, a database that will not open, and what
each log will tell you.

---

## Service commands

Run from an administrator prompt, in `C:\Program Files\LocalMonitor`:

```
monitor-service.exe status        report the service state
monitor-service.exe start | stop
monitor-service.exe install       register the service (auto-start, LocalSystem)
monitor-service.exe uninstall     stop and remove it
monitor-service.exe rotate-token  issue a new API token, then restart
monitor-service.exe version
```

The service registers itself rather than being created with `sc.exe`: `binPath`
quoting is a classic source of installers that appear to succeed and leave a
service that cannot start. It restarts after 5 s, 10 s and 30 s on a crash,
with the counter reset daily.

---

## The local API

The service listens on `127.0.0.1:49215`. Loopback is not the security model —
every local process can reach a loopback port — so there are three layers: a
bearer token, `Host`/`Origin` pinning, and a loopback check on `RemoteAddr`.

The token is generated on first start and lives in `api.token` in the data
directory. Any local administrator can read it, which is the honest limitation
of this design; the SMTP password is sealed separately and never leaves the
service.

```bash
TOKEN=$(cat "C:/ProgramData/LocalMonitor/api.token")

curl -s localhost:49215/api/health | jq                       # no token needed
curl -s -H "Authorization: Bearer $TOKEN" localhost:49215/api/summary | jq

curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' localhost:49215/api/devices \
  -d '{"name":"Core switch","ip_address":"10.0.0.1","port":22,"group_id":1}' | jq

# Move devices between groups in one transaction and one scheduler reload
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' localhost:49215/api/devices/bulk \
  -d '{"ids":[1,2,3],"op":"move","group_id":2}' | jq

# Live transitions. curl, not EventSource: the stream needs the auth header,
# which the browser EventSource API cannot set — the UI uses fetch().
curl -N -H "Authorization: Bearer $TOKEN" \
  'localhost:49215/api/events?types=device_status,incident'
```

A nullable probe setting is `null` when the row inherits, and every response
carries an `effective` block with the resolved value and where it came from:

```json
"check_interval_sec": null,
"effective": {
  "check_interval_sec": 15,
  "source": {"interval": "group:Warehouse", "timeout": "global"}
}
```

Errors are always `{"error":{"code","message","field"}}`: `409` on a duplicate
device, `422` on validation with the offending field named, `413` past the
64 kB body cap.

---

## Building from source

Needs Go 1.25, Node 24 LTS, and — for the desktop shell — Rust with the MSVC
toolchain plus Visual Studio Build Tools (C++ workload). Inno Setup 6 builds
the installer.

```bash
make check          # vet, gofmt, Go tests, UI typecheck, lint and tests
make run            # engine in the foreground against ./.dev-data
make seed           # same, with example groups and devices
```

`make seed` creates two groups and three devices chosen to cover all three
probe outcomes — one that answers, one that refuses, one that is silent — and
writes to `./.dev-data` rather than ProgramData.

The dashboard in a plain browser, which is how it is developed:

```bash
cd ui && npm ci && npm run dev
```

Start the service first: the dev server proxies `/api` to it and attaches the
token from `.dev-data/api.token`, so nothing has to be pasted anywhere.

The desktop shell natively, and the installer:

```bash
cd ui && npm run tauri:dev
```

```bash
make release-local VERSION=1.0.0
```

Everything is covered by automated tests except what needs a real machine to
install on — the service registering with the SCM, running as `LocalSystem`,
and surviving a reboot. That gap is written out as a checklist in
[installer/ACCEPTANCE.md](installer/ACCEPTANCE.md).

### Layout

```
cmd/monitor-service     entry point and subcommands
internal/model          domain types + the effective-value chain (Resolve)
internal/store          SQLite: migrations and every query
internal/probe          TCP dialer and error classification
internal/scheduler      one worker per device, bounded concurrency
internal/state          the state machine — pure, no I/O
internal/notify         SMTP transports, templates, digests, outbox worker
internal/rollup         the janitor: aggregate, prune, checkpoint, vacuum
internal/secret         DPAPI (Windows) / AES-GCM (elsewhere)
internal/api            HTTP handlers, auth + origin middleware, SSE hub
internal/core           wiring: evaluator, writer, refresh loop, API
internal/svcrun         Windows Service vs. foreground
internal/logx           slog to a rotating file and the Event Log
ui/src                  React dashboard
ui/src-tauri            the desktop shell (Rust)
installer               Inno Setup script and the acceptance checklist
```

## Status

Phases 0–4 are done and tested. Phase 5 (service and installer) is built and
compiling; its install-and-reboot acceptance run is outstanding — see
[installer/ACCEPTANCE.md](installer/ACCEPTANCE.md) and PLAN.md §16.
