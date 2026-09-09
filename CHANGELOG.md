# Changelog

## 1.0.0

First release. A Windows service that watches devices over TCP, records the
history, and emails when something goes down.

### Monitoring

- TCP probing with one worker per device and a 64-wide semaphore on concurrent
  dials, with the first probe of each device spread randomly across its
  interval so adding forty devices does not make them dial in lockstep for ever.
- A three-state machine — `UNKNOWN`, `UP`, `DOWN` — with configurable failure
  and recovery thresholds. Downtime is measured from the *first* failed probe
  of a streak, not the one that crossed the threshold.
- Error classification: `REFUSED` (something answered), `TIMEOUT` (silence),
  `DNS`, `UNREACHABLE`. A silent host is a different problem from one that
  answered "no", and the alert says which.
- A device never seen working records its outage but sends no email — that is a
  wrong port far more often than an incident.
- Incidents are rows, not derived state, so a service restart mid-outage
  resumes the same incident instead of re-alerting.

### Groups

- One group per device, no nesting. Groups carry probe defaults, alert
  recipients and maintenance windows.
- Four settings resolve `device → group → global default`, with the resolved
  value and its origin returned by the API and shown in the UI as placeholder
  text: `15 (from Warehouse)`.
- Group pause never writes to member rows, so a device paused individually
  stays paused when the window closes.
- Deleting a group never deletes its devices.

### Alerting

- Email over STARTTLS, implicit TLS or none, chosen explicitly rather than
  guessed; `multipart/alternative` with `Date` and `Message-ID` set.
- A durable outbox with backoff (1 m → 5 m → 15 m → 1 h → 4 h), because the
  mail announcing a network outage often cannot be sent over that outage.
- Recipients resolve group-then-global, so a site pages whoever looks after it.
- **Collapsing**: three or more devices in one group failing together become
  one message naming each; ten across unrelated groups become one global
  message. Recovery collapses the same way.
- **An hourly cap** that pauses mail with one message saying so, rather than
  dropping alerts silently.
- The SMTP password is sealed with DPAPI at machine scope and never returned by
  the API.

### History

- Every check kept for `retention.raw_days` (14), daily summaries for
  `retention.rollup_days` (400), and a 90-day availability strip per device and
  per site.
- Daily downtime comes from the incident log clipped to each local day, which
  is correct across restarts, interval changes and midnight — and across
  daylight-saving transitions, where the day is 23 or 25 hours long.
- An hourly janitor aggregates finished days, prunes in chunks so the delete
  never stalls the writer, checkpoints the WAL, and vacuums only when a quarter
  of the file is free.
- Group history is derived from current members on request rather than stored,
  because membership is mutable and a stored group rollup would describe a past
  that never happened.

### Interfaces

- A loopback HTTP API on `127.0.0.1:39215` with a bearer token, `Host`/`Origin`
  pinning and a loopback check — because binding to loopback is not a security
  model on its own.
- Server-sent events for live status, so badges move in milliseconds without
  polling.
- A React dashboard: grouped and flat views, bulk move/pause/delete, device
  detail with a uPlot latency chart and shaded outages, group manager, settings,
  and a service page showing scheduler lag and the last maintenance pass.
- Each row carries a **recent-checks strip** — one block per check, green for
  answered and red for not. It is seeded from the last 40 checks the server
  holds, so it reads correctly the moment the page opens rather than filling in
  over the following minutes, and heartbeats extend it live from there.
- A Tauri desktop shell: tray with up/down counts, close-to-tray, single
  instance, optional autostart for the window, and a locked CSP. It reads the
  API token from disk so nothing has to be pasted.

### Operations

- Runs as `LocalSystem`, starts with the machine, restarts after 5 s / 10 s /
  30 s on a crash.
- Failures to start go to the Windows Event Log; everything else to a rotating
  file under `ProgramData`.
- An Inno Setup installer that stops the service before replacing it and keeps
  the history on uninstall unless told otherwise.
- Binaries in Program Files, data in ProgramData — an upgrade never touches the
  database, and migrations run on start.
- **In-app updates.** The service checks the releases page every six hours,
  downloads the installer and checks it against the published `SHA256SUMS`, and
  then waits. Nothing installs itself: the dashboard offers an Install button
  and applying it is a decision someone makes. It can be turned off entirely,
  and a machine with no internet is unaffected — a failed check is a log line,
  never an alert.

### Known limitations

- **Unsigned.** SmartScreen will warn on first run; `SHA256SUMS` accompanies
  each release. This is a deliberate decision for an internal tool (PLAN.md §11).
- **Any local administrator can read `api.token`** and therefore reconfigure
  monitoring. Acceptable for a single-user workstation; the hardened
  alternative is a named pipe with an explicit DACL (PLAN.md §6).
- **TCP only.** No ICMP, HTTP or SNMP checks. The prober is an interface so
  they can be added without touching the state machine.
- **One machine.** Monitoring several sites means an agent plus a central
  server, which changes the API's trust model (PLAN.md §16).
- **The seven-day soak is manual.** `TestSoak` runs the same shape compressed
  and asserts no lost heartbeats, bounded growth and no goroutine leak, but only
  wall-clock time shows a slow leak.
