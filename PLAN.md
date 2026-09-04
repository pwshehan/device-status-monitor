# Local Device Monitor — Implementation Plan

Windows-native TCP endpoint monitor: a Go Windows Service does the probing, alerting and
storage; a Tauri v2 desktop app is a thin client over a loopback REST API; SQLite (WAL) is
the single store.

- **Status:** Phases 0–4 built and tested; Phase 5 (the Windows service and installer) is next
- **Target:** Windows 10/11 x64, single machine, single user
- **Dev host:** Windows 11 x64. The plan was written for a macOS host and §0 still
  reads that way; everything in it holds either way, since the only host-specific
  parts are the Windows-only build tags. Building on Windows has already paid for
  itself once — see the Winsock bug in §14a
- **Estimate:** ~14 focused dev-days across 7 phases

---

## 0. Build reality (read this first)

This is the biggest practical constraint and the source document does not cover it.

| Component | Cross-compiles from macOS? | How we build it |
|---|---|---|
| `monitor-service.exe` (Go) | **Yes** | `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build` — works because `modernc.org/sqlite` is pure Go |
| `monitor-gui.exe` (Tauri v2) | **No** | Needs MSVC + WebView2 headers. Build on `windows-latest` in CI (or a Windows VM) |
| `setup.exe` (Inno Setup) | **No** | Windows-only compiler (`iscc`). Same CI job as the GUI |

**Consequence — the development loop:**

1. The whole Go engine is written and tested **natively on macOS**. Everything except the
   service wrapper is portable (`net.DialTimeout`, `net/smtp`, SQLite are all OS-agnostic).
   The Windows-only bits live behind build tags:
   - `internal/svcrun/run_windows.go` — `golang.org/x/sys/windows/svc`
   - `internal/svcrun/run_other.go` — foreground runner, so `go run ./cmd/monitor-service -dev` works on macOS
2. The React UI is developed in a **plain browser** via `npm run dev` against the Go service
   on `127.0.0.1:49215`. No Rust toolchain needed for 90% of UI work.
3. Rust/Tauri is only needed for the native shell (tray icon, single-instance, autostart,
   window chrome) and the final binary. Installed in Phase 3b: `rustup` with the
   `stable-x86_64-pc-windows-msvc` toolchain. The heavy prerequisite — Visual Studio Build
   Tools with the C++ workload and the Windows SDK — was already on the machine, which is
   the part worth checking before budgeting a day for this phase.
4. Windows artifacts (`monitor-gui.exe`, `setup.exe`) come out of GitHub Actions on every tag.
   A Windows box (or Parallels/UTM VM) is still required once per phase for acceptance testing —
   service install, SYSTEM-account behaviour and SmartScreen cannot be verified in CI.

**Assumptions**, correct me if wrong:
- Go module path `github.com/gkgraphite/device-status-monitor`
- Scale target: up to 200 devices, default 30 s interval (~576k heartbeats/day worst case — the
  retention design in §5 is sized for this)
- Devices are organised into **flat groups** — one group per device, no nesting (§3.1)
- Alerts go to email only in v1; no SMS/webhook/Teams
- The service runs as `LocalSystem`; the GUI runs as the logged-in standard user

---

## 1. Architecture

```
┌───────────────────────── Windows machine ──────────────────────────┐
│                                                                    │
│  monitor-gui.exe (user session)      monitor-service.exe (SYSTEM)  │
│  ┌──────────────────────────┐        ┌──────────────────────────┐  │
│  │ Tauri v2 shell (Rust)    │        │ svc.Handler              │  │
│  │  · tray, autostart       │        │  ├── scheduler           │  │
│  │  · single instance       │        │  │    per-device ticker  │  │
│  │ ─────────────────────────│  HTTP  │  ├── prober (TCP dial)   │  │
│  │ WebView2                 │◄──────►│  ├── evaluator          │  │
│  │  React + Vite + Tailwind │ :49215 │  │    state machine     │  │
│  │  TanStack Query + SSE    │ Bearer │  ├── writer (batched)    │  │
│  │  uPlot                   │        │  ├── notifier (SMTP)    │  │
│  └──────────────────────────┘        │  ├── janitor (rollup)   │  │
│                                      │  └── api (net/http+SSE) │  │
│                                      └────────────┬─────────────┘  │
│                                                   │                │
│   C:\ProgramData\LocalMonitor\                    ▼                │
│     monitor.db  (SQLite, WAL)   ◄─────────────────┘                │
│     api.token                                                      │
│     logs\monitor-YYYY-MM-DD.log                                    │
└────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼  outbound TCP: probes + SMTP
```

Closing the GUI does not stop monitoring. The GUI holds no state and no credentials.

### Goroutine topology inside the service

| Goroutine | Count | Job |
|---|---|---|
| device worker | 1 per enabled device | ticker at `check_interval_sec`, random initial jitter, takes a slot from a 64-wide semaphore, dials, pushes a `Result` |
| evaluator | 1 | drains the result channel, runs the state machine, opens/closes incidents, enqueues alerts. **Single-threaded on purpose** — no locks, no transition races |
| writer | 1 | batches heartbeat inserts into one transaction every 2 s or 200 rows |
| notifier | 1 | drains `alert_outbox` with exponential backoff |
| janitor | 1 | hourly: roll up yesterday, prune raw rows, WAL checkpoint |
| api | 1 + per-request | `net/http` on loopback, SSE fan-out |

Device add/edit/delete posts to a `reload` channel; the scheduler diffs the device set and
starts/cancels per-device contexts. No restart needed to change config.

---

## 2. Technology decisions

| Layer | Choice | Why / what changed from the source doc |
|---|---|---|
| Service | Go 1.25 + `golang.org/x/sys/windows/svc` | ~15 MB RSS, no runtime deps, first-class Windows service support |
| DB | SQLite via `modernc.org/sqlite` | Pure Go → keeps `CGO_ENABLED=0` cross-compilation. `mattn/go-sqlite3` would force a gcc toolchain and kill the macOS build loop |
| Shell | Tauri v2 | ~10 MB installer, ~40 MB idle, uses the WebView2 already on Windows 11 |
| UI | React 19 + Vite + TS + Tailwind + TanStack Query | Forms, tables, live badges |
| Latency chart | **uPlot** (was: Chart.js) | 24 h at 30 s = 2 880 points/device; uPlot is 45 kB and renders that in ~1 ms. Chart.js works but is heavier for a pure time-series |
| Uptime bars | **Plain CSS grid of divs** (was: Chart.js bars) | 90 coloured blocks is markup, not a chart. Removes a dependency |
| Live updates | **SSE** `GET /api/events` (was: polling) | Sub-second badge updates, one connection, trivial in `net/http` |
| Secrets | **Windows DPAPI** machine scope | SMTP password must not sit in plaintext in `monitor.db` |
| Installer | Inno Setup 6 | Registers the service, WebView2 bootstrap, upgrade-safe |
| Logs | `log/slog` + `lumberjack` rotation + Event Log for start/stop failures | Service has no console |

---

## 3. Database schema

`C:\ProgramData\LocalMonitor\monitor.db`, opened with:

```
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA busy_timeout=5000;
PRAGMA foreign_keys=ON;
```

Two handles: a **writer** (`SetMaxOpenConns(1)` — SQLite has one writer, so serialise in Go
rather than fighting `SQLITE_BUSY`) and a **reader** pool for the API.

> **Deliberate change from the source doc:** timestamps are `INTEGER` unix-epoch-seconds UTC,
> not `DATETIME` text. Text datetimes in SQLite are timezone-ambiguous, sort lexically and
> index 3× larger. `strftime('%Y-%m-%d', checked_at, 'unixepoch', 'localtime')` gives the same
> grouping the original SQL wanted.

```sql
-- 0001_init.sql

-- Groups: the structural home of a device. One group per device, no nesting.
CREATE TABLE groups (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  name                TEXT    NOT NULL UNIQUE,
  description         TEXT    NOT NULL DEFAULT '',
  color               TEXT    NOT NULL DEFAULT '',  -- swatch for the dashboard
  sort_order          INTEGER NOT NULL DEFAULT 0,
  -- Probe defaults for members. NULL = fall through to the global defaults.
  check_interval_sec  INTEGER,
  timeout_sec         INTEGER,
  failure_threshold   INTEGER,
  recovery_threshold  INTEGER,
  -- Group-level alerting
  notify              INTEGER NOT NULL DEFAULT 1,
  recipients          TEXT,        -- NULL = use alert.recipients
  paused_until        INTEGER,     -- pause a whole site for maintenance
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
);

CREATE TABLE devices (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  name                  TEXT    NOT NULL,
  ip_address            TEXT    NOT NULL,
  port                  INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535),
  group_id              INTEGER REFERENCES groups(id) ON DELETE SET NULL,
  -- NULL on any of these four = inherit from the group, then from the global default.
  -- Nullable rather than sentinel values, so "inherited" is representable.
  check_interval_sec    INTEGER CHECK(check_interval_sec >= 5),
  timeout_sec           INTEGER CHECK(timeout_sec BETWEEN 1 AND 60),
  failure_threshold     INTEGER,      -- strikes before DOWN + alert
  recovery_threshold    INTEGER,      -- successes before UP + recovery mail
  enabled               INTEGER NOT NULL DEFAULT 1,
  notify                INTEGER NOT NULL DEFAULT 1,
  paused_until          INTEGER,                      -- unix sec; NULL = not paused
  tags                  TEXT    NOT NULL DEFAULT '',  -- comma separated, cross-cutting
  status                TEXT    NOT NULL DEFAULT 'UNKNOWN'
                          CHECK(status IN ('UP','DOWN','UNKNOWN')),
  consecutive_failures  INTEGER NOT NULL DEFAULT 0,
  consecutive_successes INTEGER NOT NULL DEFAULT 0,
  first_failure_at      INTEGER,     -- start of the current failure streak (accurate downtime)
  last_check_at         INTEGER,
  last_latency_ms       INTEGER,
  last_error            TEXT,
  last_status_change_at INTEGER,
  created_at            INTEGER NOT NULL,
  updated_at            INTEGER NOT NULL,
  UNIQUE(ip_address, port)
);

-- Raw time series. Pruned to retention.raw_days (default 14).
CREATE TABLE heartbeats (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  device_id  INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  status     TEXT    NOT NULL CHECK(status IN ('UP','DOWN')),
  latency_ms INTEGER NOT NULL,
  error_msg  TEXT,
  checked_at INTEGER NOT NULL
);
CREATE INDEX idx_heartbeats_device_time ON heartbeats(device_id, checked_at);

-- Pre-aggregated history so 90-day views never touch raw rows.
CREATE TABLE rollups_daily (
  device_id      INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  day            TEXT    NOT NULL,          -- 'YYYY-MM-DD' local time
  checks_total   INTEGER NOT NULL,
  checks_up      INTEGER NOT NULL,
  uptime_pct     REAL    NOT NULL,
  avg_latency_ms REAL,
  p95_latency_ms INTEGER,
  downtime_sec   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(device_id, day)
) WITHOUT ROWID;

-- One row per outage episode. Source of truth for "total downtime" in recovery emails.
CREATE TABLE incidents (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  device_id     INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  started_at    INTEGER NOT NULL,           -- = first_failure_at, not the 3rd strike
  detected_at   INTEGER NOT NULL,           -- when the threshold tripped
  resolved_at   INTEGER,
  duration_sec  INTEGER,
  cause         TEXT,                       -- last dial error
  alert_sent    INTEGER NOT NULL DEFAULT 0,
  recovery_sent INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_incidents_open ON incidents(device_id) WHERE resolved_at IS NULL;

-- Durable email queue: if the network is down, the alert about the network being down
-- cannot be sent. Queue it and retry with backoff instead of losing it.
CREATE TABLE alert_outbox (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  incident_id     INTEGER REFERENCES incidents(id) ON DELETE SET NULL,
  kind            TEXT    NOT NULL CHECK(kind IN ('DOWN','RECOVERY','TEST','DIGEST')),
  subject         TEXT    NOT NULL,
  body_text       TEXT    NOT NULL,
  body_html       TEXT,
  recipients      TEXT    NOT NULL,
  attempts        INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL,
  last_error      TEXT,
  created_at      INTEGER NOT NULL,
  sent_at         INTEGER
);
CREATE INDEX idx_outbox_pending ON alert_outbox(next_attempt_at) WHERE sent_at IS NULL;

CREATE INDEX idx_devices_group ON devices(group_id, name);

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
```

### 3.1 Groups, and why they are not just tags

Both mechanisms exist and they do different jobs:

| | `groups` | `tags` |
|---|---|---|
| Cardinality | exactly one per device (or none) | many per device |
| Purpose | structural home: rollups, alert routing, maintenance windows, dashboard sections | cross-cutting filters — `critical`, `customer-facing`, `after-hours` |
| Has settings | yes — probe defaults, recipients, pause | no |

One group per device is a deliberate constraint, not a simplification. Group uptime has to mean
something, and a device counted in three groups is counted three times. Nesting is the same
argument one level up: it buys a tree widget and recursive queries, and for a 200-device single
site it earns neither. If nesting is ever needed, the migration is one nullable `parent_id`
column plus a recursive CTE — no data reshaping. Not adding the column now, because an unused
column is a lie about what the code supports.

### 3.2 Effective values

Four probe settings now resolve down a three-tier chain:

```
device.check_interval_sec  ??  group.check_interval_sec  ??  settings['default.check_interval_sec']
```

**Resolve once, in Go, at scheduler load — never with `COALESCE` scattered across queries.**

```go
// internal/scheduler/effective.go
type Effective struct {
    DeviceID                        int64
    Addr                            string
    Interval, Timeout               time.Duration
    FailureThreshold, RecoveryThreshold int
    Notify                          bool          // device.notify AND group.notify
    Recipients                      []string      // group.recipients ?? global
    PausedUntil                     time.Time     // max(device, group)
    Source                          map[string]string // "interval" -> "group:Warehouse"
}
```

Three consequences worth designing for up front:

- **A group edit reloads every member.** `PATCH /api/groups/{id}` recomputes the effective set
  and pushes it down the same `reload` channel a device edit uses, so changing a group's interval
  restarts exactly its members' tickers and nothing else.
- **`Source` is carried to the UI.** The single most likely support question about inheritance is
  "why is this device checking every 60 seconds?", so the API returns both the resolved value and
  where it came from, and the form shows `30 (from Warehouse)` as placeholder text.
- **Effective pause is a maximum, not an assignment.** Pausing a group must not write to member
  rows — otherwise unpausing the group cannot restore the device that was individually paused
  before the maintenance window started.

### 3.3 Group uptime is derived, never stored

There is no `rollups_daily_group` table, on purpose: **group membership is mutable, so group
history must be derived.** Move a device between groups and any stored group rollup instantly
describes a past that never happened. Aggregating 90 days × 20 members is a few hundred rows —
cheap enough to compute per request.

```sql
-- Aggregate availability for one group: share of all member checks that succeeded.
SELECT r.day,
       ROUND(SUM(r.checks_up) * 100.0 / SUM(r.checks_total), 3) AS uptime_pct,
       MIN(r.uptime_pct)                                        AS worst_device_pct,
       SUM(r.downtime_sec)                                      AS member_downtime_sec
FROM rollups_daily r
JOIN devices d ON d.id = r.device_id
WHERE d.group_id = ?1 AND r.day >= date('now','localtime', ?2)
GROUP BY r.day
ORDER BY r.day;
```

Two numbers, because they answer different questions: `uptime_pct` is aggregate availability
across the site, `worst_device_pct` is the member that had the worst day. A site of 20 devices
where one was dead all day still reads 95% aggregate — the group strip is coloured by the
aggregate and the tooltip names the worst member.

### Settings keys

| Key | Default | Note |
|---|---|---|
| `schema_version` | `1` | migration bookkeeping |
| `smtp.host` / `smtp.port` | — / `587` | |
| `smtp.security` | `starttls` | `starttls` \| `tls` \| `none` |
| `smtp.username` | — | |
| `smtp.password_enc` | — | base64 DPAPI blob; **never** returned by the API |
| `smtp.from` | — | |
| `default.check_interval_sec` | `30` | bottom tier of the chain in §3.2 |
| `default.timeout_sec` | `3` | |
| `default.failure_threshold` | `3` | |
| `default.recovery_threshold` | `1` | |
| `alert.recipients` | — | comma separated; group `recipients` overrides |
| `alert.reminder_sec` | `0` | 0 = no repeat while an incident is open |
| `retention.raw_days` | `14` | |
| `retention.rollup_days` | `400` | |

### Uptime query (post-rollup)

```sql
SELECT day, uptime_pct, avg_latency_ms, downtime_sec
FROM rollups_daily
WHERE device_id = ?1 AND day >= date('now','localtime', ?2)   -- ?2 = '-90 days'
ORDER BY day;
```

Today's partial day comes from raw rows and is merged in Go:

```sql
SELECT COUNT(*)                                       AS checks_total,
       SUM(status='UP')                               AS checks_up,
       AVG(CASE WHEN status='UP' THEN latency_ms END) AS avg_latency
FROM heartbeats
WHERE device_id = ?1
  AND checked_at >= unixepoch('now','localtime','start of day');
```

---

## 4. State machine

Three states — `UNKNOWN`, `UP`, `DOWN` — evaluated only in the single evaluator goroutine.

```
                  probe ok, prev=UNKNOWN            (silent: no "recovered" mail
      UNKNOWN ──────────────────────────────► UP     for a device we just added)
         │                                    ▲ │
         │ fail × failure_threshold           │ │ ok × recovery_threshold
         │ (silent: first-ever check          │ │ → close incident,
         │  failing is not a "went down")     │ │   enqueue RECOVERY
         ▼                                    │ ▼
       DOWN ◄─────────────────────────────────┘
              fail × failure_threshold
              → open incident, enqueue DOWN
```

Rules:

- **Failure:** `consecutive_failures++`, `consecutive_successes = 0`. Set `first_failure_at`
  if unset. When `consecutive_failures >= failure_threshold` and `status != DOWN`: transition
  to `DOWN`, insert an `incidents` row with `started_at = first_failure_at`, enqueue a `DOWN`
  mail (only if `notify = 1`, and only if the open incident has `alert_sent = 0`).
- **Success:** `consecutive_successes++`, `consecutive_failures = 0`, clear `first_failure_at`.
  When `status = DOWN` and `consecutive_successes >= recovery_threshold`: transition to `UP`,
  set `resolved_at`/`duration_sec` on the open incident, enqueue a `RECOVERY` mail carrying
  the human-readable downtime.
- **Anti-flap:** with defaults (3 strikes @ 30 s) a device must be unreachable for ~90 s before
  anyone is emailed. A blip inside one interval produces a `DOWN` heartbeat row (visible in the
  chart) but no state change and no mail.
- **Accurate downtime:** the incident starts at the first failed probe, not at the third. The
  source doc's "compute downtime from heartbeats" approach breaks whenever the service restarts
  mid-outage; the `incidents` table survives restarts.
- **Paused / disabled:** the worker skips the dial entirely — no heartbeat row, no state change,
  no counter movement. On unpause the device re-enters at its current stored state. A device is
  paused when *either* its own `paused_until` or its group's is in the future (§3.2), so a
  maintenance window on a site suppresses the whole site's alerts without touching member rows.
- **Restart recovery:** on boot the service re-reads open incidents so a device that went down
  before a reboot stays down and does not double-alert.
- **Reminder:** if `alert.reminder_sec > 0`, re-enqueue a `DOWN` mail that often while the
  incident stays open.

### Probe

```go
// internal/probe/probe.go
type Prober interface {
    Probe(ctx context.Context, addr string, timeout time.Duration) Result
}

type Result struct {
    OK        bool
    LatencyMS int64
    Err       error   // wrapped; classified into TIMEOUT / REFUSED / DNS / UNREACHABLE
}
```

`net.DialTimeout` via `(&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", addr)` so a
service shutdown cancels in-flight dials instead of waiting out the timeout. Latency is measured
around the dial and the connection is closed immediately (`SetLinger(0)` to avoid TIME_WAIT
buildup at 200 devices). The interface exists so the state machine is testable with a fake.

Error classification matters for the UI badge: `TIMEOUT` (host silent) reads differently from
`REFUSED` (host up, service down) — both are `DOWN` for alerting but the badge and the email
body say which.

---

## 5. Retention and rollups

Not in the source doc, and without it the DB grows unbounded — 200 devices at 30 s is
576 000 rows/day, ~40 MB/day.

Hourly janitor pass:

1. For each device × each complete local day not yet in `rollups_daily`, aggregate from
   `heartbeats` and upsert. `downtime_sec` comes from `incidents` clipped to the day, not from
   counting `DOWN` rows (correct across service restarts and interval changes).
2. `DELETE FROM heartbeats WHERE checked_at < now - retention.raw_days` in 10 000-row chunks so
   a single delete never blocks the writer.
3. `DELETE FROM rollups_daily WHERE day < now - retention.rollup_days`.
4. `PRAGMA wal_checkpoint(TRUNCATE)` and a monthly `VACUUM` when idle.

Steady state: ~600 MB raw at 14 days for 200 devices, plus a trivial rollup table that keeps
90-day and yearly views instant.

---

## 6. Local API

`http.Server` bound to `127.0.0.1:49215` (loopback only — never `0.0.0.0`).

### Auth and hardening

The source doc treats "binds to 127.0.0.1" as the security model. It is not: any process of
any user on the machine can reach a loopback port, and a malicious web page can attempt
DNS-rebinding against it. Three cheap layers:

1. **Bearer token.** On first start the service generates 32 random bytes and writes
   `C:\ProgramData\LocalMonitor\api.token` with a DACL granting `SYSTEM` + `Administrators`
   full control and `Users` read. The GUI reads the file and sends
   `Authorization: Bearer <token>`. Rotated on demand via `monitor-service.exe rotate-token`.
2. **Origin/Host pinning.** Reject any request whose `Host` is not `127.0.0.1:49215` or whose
   `Origin` header is present and not `http://127.0.0.1:*` / `tauri://localhost`. Kills
   DNS-rebinding.
3. **RemoteAddr check.** Reject anything not from `127.0.0.1`/`::1` even if the bind slipped.

Honest limitation: `Users`-readable token means any local user can read/modify the monitor
config. Acceptable for a single-user workstation, and the SMTP password stays DPAPI-sealed
either way. If that is not acceptable, the hardened alternative is a **named pipe with an
explicit DACL**, proxied to the webview by Tauri's Rust side — same handlers, ~1 day of extra
work, deferred to a follow-up.

### Endpoints

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/health` | `{version, uptime_sec, db_ok, devices, up, down, scheduler_lag_ms}` — unauthenticated, so the GUI can tell "service down" from "token wrong" |
| GET | `/api/summary` | dashboard tiles: counts, worst offenders, open incidents, per-group breakdown |
| GET | `/api/devices` | list with live status |
| POST | `/api/devices` | create; validates host/port, rejects duplicates |
| GET | `/api/devices?group_id=&tag=&status=` | filtered list |
| GET | `/api/devices/{id}` | one device + open incident |
| PATCH | `/api/devices/{id}` | partial update; triggers scheduler reload |
| DELETE | `/api/devices/{id}` | cascade-deletes history |
| POST | `/api/devices/{id}/pause` | `{minutes}` or `{until}`; `null` = resume |
| POST | `/api/devices/{id}/check` | force one probe now, return the result |
| GET | `/api/devices/{id}/heartbeats?from&to&max_points` | raw series, server-side decimated to `max_points` |
| GET | `/api/devices/{id}/uptime?days=90` | daily rollups + today's partial |
| GET | `/api/devices/{id}/incidents?limit=50` | outage log |
| POST | `/api/devices/bulk` | `{ids, op: "move"\|"pause"\|"resume"\|"delete", group_id?, minutes?}` — one transaction, one reload. This is what makes grouping usable after adding 40 devices |
| GET | `/api/groups` | list with member counts, up/down/paused tallies, today's aggregate uptime; ends with an `id: null` **Ungrouped** bucket when one is non-empty |
| GET | `/api/groups/{id}` | one group with the same tallies — the group-detail screen's header |
| POST | `/api/groups` | create |
| PATCH | `/api/groups/{id}` | rename, recolour, change defaults or recipients — reloads every member |
| DELETE | `/api/groups/{id}` | **devices survive** and become ungrouped (`ON DELETE SET NULL`). Never cascade to devices; the response says how many were orphaned |
| POST | `/api/groups/reorder` | `{ids: [...]}` → writes `sort_order` |
| POST | `/api/groups/{id}/pause` | `{minutes}` or `{until}`; `null` = resume. Maintenance window for the whole group |
| GET | `/api/groups/{id}/uptime?days=90` | derived aggregate + worst member per day (§3.3) |
| GET | `/api/settings` | secrets redacted to `"***"` / `has_password: true` |
| PUT | `/api/settings` | write-through; empty password field = leave unchanged |
| POST | `/api/settings/test-email` | sends immediately, bypasses the outbox, returns the SMTP error verbatim |
| GET | `/api/events` | SSE stream: `device_status`, `group_status`, `heartbeat`, `incident`, `settings`. Filter with `?types=`. Bearer header only, so the client is `fetch()`, not `EventSource` |

Conventions: JSON only, `application/problem+json`-shaped errors
(`{error: {code, message, field}}`), `409` on duplicate device, `422` on validation,
`503` while the DB is unavailable. Every write is one transaction. Bodies capped at 64 kB.

---

## 7. Email

`net/smtp` is thin and does *not* pick a transport for you — the source doc's `smtp.SendMail`
call only works for STARTTLS-on-587 servers. Handle all three modes explicitly:

```go
switch cfg.Security {
case "tls":       // implicit TLS, port 465
    conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host})
    c, err := smtp.NewClient(conn, cfg.Host)
case "starttls":  // port 587
    c, err := smtp.Dial(addr)
    err = c.StartTLS(&tls.Config{ServerName: cfg.Host})
case "none":      // port 25, internal relay, no auth
    c, err := smtp.Dial(addr)
}
if cfg.Username != "" {
    err = c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host))
}
```

Messages are `multipart/alternative` (plain + HTML) with `From`, `To`, `Subject`
(RFC 2047-encoded), `Date`, `Message-ID` and `MIME-Version` set — missing `Date`/`Message-ID`
is a reliable spam-score penalty.

Templates:

- **DOWN** — `[DOWN] {name} ({ip}:{port})` · first failure time, detection time, consecutive
  failures, classified error, last known latency.
- **RECOVERY** — `[UP] {name} recovered after 4m 12s` · downtime from the incident row, current
  latency, 24 h uptime %.
- **TEST** — proves the credentials work.

**Routing.** Recipients resolve `group.recipients ?? alert.recipients`, so the warehouse switch
can page whoever looks after the warehouse. This is the main reason to want groups at all, and it
is why `recipients` lives on the group rather than being a per-device field nobody will maintain.

**Group-scoped digest.** Grouping makes the collapse rule meaningfully better. When 3 or more
members of one group trip inside 120 s, they collapse into a single per-group digest —
`[DOWN] Warehouse — 6 of 8 devices unreachable` — because a site outage is one event, not six.
Recovery collapses the same way. Devices that fail across unrelated groups still get individual
mails; the digest key is `(group_id, window)`. Ungrouped devices never digest.

Delivery: `notifier` polls `alert_outbox` every 30 s, backoff `1m → 5m → 15m → 1h → 4h`, max 8
attempts, then marks the row failed and logs at `ERROR`. Rate limit: at most 20 mails/hour
globally, and a global fallback digest if more than 10 devices go down inside 60 s regardless of
group — a core switch outage should not send 60 emails.

---

## 8. Desktop app

`ui/` — Vite + React 19 + TS + Tailwind; `ui/src-tauri/` — Rust shell.

**Screens**

1. **Dashboard** — tiles (total / up / down / paused, open incidents), then the device list in one
   of two modes:
   - **Grouped** (default) — a collapsible section per group, ordered by `sort_order`. The section
     header carries the group name and colour, `6/8 up`, today's aggregate uptime, one 90-day
     strip for the whole group, and *pause group* / *collapse*. A group with any member down sorts
     to the top and its header turns red. `Ungrouped` renders last and only when non-empty.
   - **Flat** — one table, sorted by status then name. Keep this: when everything is on fire,
     grouping is in the way, and triage wants one list of what is down.
   Either mode shows name, `ip:port`, live badge (`UP` green, `DOWN` red, `TIMEOUT` amber,
   `PAUSED` grey, `UNKNOWN`), last check, last latency, sparkline, and row actions (check now,
   pause, edit, delete). Filter by group, tag or status; collapsed-section state persists locally.
   Row checkboxes enable bulk **Move to group…**, pause and delete via `/api/devices/bulk`.
2. **Device detail** — 24 h uPlot latency line with `DOWN` spans shaded; 90-day uptime strip as
   a CSS grid of 90 divs coloured by `uptime_pct` (green ≥99.9, amber ≥95, red below, hatched
   for no-data), tooltip per day; incident table.
3. **Add/Edit device modal** — group picker, name, host, port, interval, timeout, thresholds,
   notify, tags; inline "test connection now" before saving. The four inheritable fields are
   blank by default with the resolved value as placeholder text — `30 (from Warehouse)` — so
   leaving a field alone means *inherit* and typing in it means *override*. A small "reset to
   inherited" affordance clears an override back to `NULL`.
4. **Groups** — create, rename, recolour, drag to reorder, set group probe defaults and
   recipients, pause for maintenance, see member count and aggregate uptime. Deleting a group
   asks for confirmation and states plainly that its devices will move to `Ungrouped`, not be
   deleted.
5. **Group detail** — the group's 90-day aggregate strip, a stacked member view, and the
   combined incident log for the site.
6. **Settings** — SMTP (host, port, security dropdown, username, password, from, recipients),
   *Send test email* with the raw server response shown on failure; retention; the global
   `default.*` tier that groups and devices inherit from.
7. **Service status** — version, uptime, DB size, scheduler lag, log folder shortcut,
   rotate-token button.

**Behaviours**

- SSE-driven badges; TanStack Query for everything else with SSE-triggered invalidation.
- Hard-fail banner when `/api/health` is unreachable: "Monitoring service is not running" with a
  copyable `sc start LocalMonitorSvc` and a link to the log folder. Retries with backoff.
- Tray icon (Rust): show/hide, quit, and up/down count in the tooltip. Closing the window
  minimises to tray; quitting the GUI never touches the service.
- Optional autostart via `tauri-plugin-autostart`; single-instance via `tauri-plugin-single-instance`.
- Tauri CSP locked to `default-src 'self'; connect-src http://127.0.0.1:49215`.
- Dark/light following the OS.

---

## 9. Repository layout

```
device-status-monitor/
├─ cmd/monitor-service/main.go     # install|uninstall|start|stop|status|rotate-token|-dev
├─ internal/
│  ├─ model/        # domain types + Resolve (the effective-value chain)
│  ├─ appdir/       # ProgramData paths, dev-mode override
│  ├─ core/         # wiring: evaluator, batched writer, refresh loop
│  ├─ secret/       # DPAPI seal/unseal (+ no-op stub for macOS dev)
│  ├─ store/        # sqlite open, migrations/, queries, batched writer
│  ├─ probe/        # Prober iface, TCP impl, error classification
│  ├─ scheduler/    # per-device workers, semaphore, reload diffing
│  ├─ state/        # state machine, incident lifecycle  ← densest tests live here
│  ├─ notify/       # smtp client, templates/, outbox worker
│  ├─ rollup/       # janitor: aggregate, prune, checkpoint
│  ├─ api/          # handlers, auth+origin middleware, SSE hub
│  ├─ svcrun/       # run_windows.go (svc.Handler) | run_other.go (foreground)
│  └─ logx/         # slog handler, lumberjack, Event Log sink
├─ ui/
│  ├─ src/{pages,components,api,hooks}/
│  └─ src-tauri/
├─ installer/setup.iss
├─ .github/workflows/{ci.yml,release.yml}
├─ Makefile
└─ PLAN.md
```

---

## 10. Service integration

`monitor-service.exe` is one binary with subcommands, so the installer never needs `sc.exe`
string-quoting gymnastics:

| Command | Effect |
|---|---|
| `install` | `mgr.Connect` → `CreateService` (auto-start, `LocalSystem`), sets description and failure actions: restart after 5 s / 10 s / 30 s, reset counter daily |
| `uninstall` | stop, wait, delete |
| `start` / `stop` / `status` | wrappers over the SCM, used by the GUI and the installer |
| `rotate-token` | regenerates `api.token` |
| `-dev` | foreground run against `./.dev-data`, verbose logging — this is the macOS loop |

```go
// internal/svcrun/run_windows.go
func (m *monitorService) Execute(args []string, r <-chan svc.ChangeRequest,
    changes chan<- svc.Status) (bool, uint32) {

    changes <- svc.Status{State: svc.StartPending}
    ctx, cancel := context.WithCancel(context.Background())

    app, err := core.Start(ctx)          // db, migrations, scheduler, api, notifier, janitor
    if err != nil {
        logx.EventLogError(err)           // the only place SYSTEM can shout before logs exist
        changes <- svc.Status{State: svc.Stopped}
        return true, 1
    }

    changes <- svc.Status{State: svc.Running,
        Accepts: svc.AcceptStop | svc.AcceptShutdown}

    for req := range r {
        switch req.Cmd {
        case svc.Interrogate:
            changes <- req.CurrentStatus
        case svc.Stop, svc.Shutdown:
            changes <- svc.Status{State: svc.StopPending}
            cancel()
            app.Wait(10 * time.Second)    // flush writer, checkpoint WAL, close listener
            return false, 0
        }
    }
    return false, 0
}
```

Two details the source snippet omits and that cause real bugs: **startup errors must be
reported to the Event Log and exit non-zero** (otherwise the SCM reports "running" on a broken
service), and **shutdown must wait for the writer to flush** (otherwise the last batch of
heartbeats and a possibly-open incident are lost).

`svc.IsWindowsService()` picks service vs. console mode automatically.

---

## 11. Packaging

**Layout on disk**

```
C:\Program Files\LocalMonitor\      monitor-service.exe, monitor-gui.exe
C:\ProgramData\LocalMonitor\        monitor.db (+ -wal, -shm), api.token, logs\
```

Binaries and data are separated so upgrades never touch the database and the service (as
SYSTEM) and the GUI (as user) share one data path.

**`installer/setup.iss` outline**

- `PrivilegesRequired=admin`, `ArchitecturesInstallIn64BitMode=x64`, `AppMutex` to catch a
  running GUI.
- `[Code] InitializeSetup`: if the service exists, `monitor-service.exe stop` and wait — file
  replacement fails otherwise on upgrade.
- `[Files]`: both exes; WebView2 bootstrapper (`MicrosoftEdgeWebview2Setup.exe`,
  `/silent /install`) run only when the runtime is missing — needed for Windows 10, already
  present on 11.
- `[Run]`: `monitor-service.exe install` then `start`.
- `[Icons]`: Start Menu + optional desktop shortcut to `monitor-gui.exe`.
- `[UninstallRun]`: `monitor-service.exe uninstall`; a task-page checkbox decides whether
  `ProgramData\LocalMonitor` is deleted (default: keep the history).
- Migrations run on service start, so upgrade is "replace exe, restart service".

**Code signing** — unsigned, SmartScreen will show "Windows protected your PC" and some AV will
quarantine a service-installing binary. Budget an OV/EV code-signing certificate; the release
workflow signs both exes and the installer when `CERT_PFX`/`CERT_PASSWORD` secrets exist and
skips signing (with a warning) when they do not.

**CI**

- `ci.yml` (push/PR): `ubuntu-latest` → `go vet`, `golangci-lint`, `go test ./... -race`,
  `GOOS=windows go build` smoke; `npm ci`, `tsc --noEmit`, `eslint`, `vitest`.
- `release.yml` (tag `v*`): `windows-latest` → build Go exe, `npm ci && npm run tauri build`,
  `choco install innosetup`, `iscc installer\setup.iss`, sign, attach
  `LocalMonitor-Setup-x.y.z.exe` + raw exes + `SHA256SUMS` to the GitHub release.

---

## 12. Testing

| Area | Approach |
|---|---|
| Effective values | Table-driven over `scheduler/effective.go`: every combination of device NULL / group NULL / global; `Notify` as an AND; `PausedUntil` as a max; `Source` strings correct. Cheap tests that prevent the whole class of "why is this device on 60 s?" bugs |
| Groups | Group delete orphans devices instead of deleting them; a group `PATCH` reloads exactly its members' tickers; bulk move is one transaction; derived group uptime matches a hand-computed fixture; group pause suppresses alerts without mutating member rows |
| State machine | Table-driven over `internal/state` with a fake `Prober` and an injected clock: flap inside threshold, exactly-at-threshold, `UNKNOWN` first-fail silence, recovery duration, pause mid-outage, restart with an open incident, `notify=0`. **This is where the bugs will be** — aim for full branch coverage |
| Probe | `net.Listen("tcp","127.0.0.1:0")` for UP; a closed port for REFUSED; `192.0.2.1` (TEST-NET-1) for TIMEOUT; assert error classification and that latency ≈ timeout on timeout |
| Store | temp-file DB per test; migrate-up idempotency; cascade delete; concurrent reader-while-writer under WAL; batched-writer flush on cancel |
| Rollup | seed synthetic heartbeats + incidents across a DST boundary, assert `uptime_pct`/`downtime_sec`/pruning |
| API | `httptest` + real DB: auth pass/fail, non-loopback rejection, bad `Origin`, validation errors, SSE receives an event within 1 s of a transition |
| Notify | stub SMTP server (`emersion/go-smtp`) for all three security modes; outbox backoff with a fake clock; digest collapsing |
| UI | `vitest` + Testing Library on the table/badge/uptime-strip components; MSW for the API |
| Desktop shell | `cargo test` on the pure parts — tray tooltip wording, and that the injected token is escaped so a token file written by anything on the machine cannot run code in the window. The window itself is verified by launching the built exe against a running service and checking the API sees an authenticated SSE client and rejects nothing |
| Windows acceptance | Manual checklist per release: install → service auto-starts → survives reboot → unplug a monitored device → DOWN mail in ~90 s **plus the collapse window** (105 s on the defaults; §7's digest cannot collapse without waiting) → replug → RECOVERY mail with correct duration → GUI shows the incident → upgrade preserves DB → uninstall removes the service |
| Rollups & retention | `internal/rollup`: a finished day is aggregated and today is not; downtime comes from incidents clipped to the day, including an outage that spans midnight; raw rows are pruned in chunks while their summaries survive; rollups and resolved incidents age out but an open incident never does; a day already past the rollup window is skipped rather than aggregated and immediately deleted |
| Alert collapsing | `internal/core`: a whole site failing produces exactly one digest naming every member, and one on recovery; two devices still get their own mails; the hourly cap pauses mail, says so once, and leaves every incident recorded |
| Soak | `TestSoak`: 50 devices in 6 groups, ~1 400 probes, then a restart, then a janitor pass — asserts no heartbeat is lost, history survives pruning as summaries, and goroutines return to baseline after two full lifecycles. The **seven-day** soak stays manual: only wall-clock time shows a slow leak |

---

## 13. Phases

Each phase ends on something demonstrable. Estimates are focused dev-days.

| # | Phase | Work | Done when | Days |
|---|---|---|---|---|
| 0 | ✅ Scaffolding | repo, module, `Makefile`, CI skeleton, `appdir`, `logx`, `store` open + migrations | `go test ./...` green on macOS; `GOOS=windows go build` produces an exe | 0.5 |
| 1 | ✅ Engine core | `probe`, `scheduler`, `state`, `incidents`, batched writer, **effective-value resolution**, `notify` + outbox | `-dev` mode on macOS monitors 3 real endpoints in 2 groups, logs heartbeats, emails DOWN and RECOVERY with correct downtime; a group interval change reloads only its members | 3.25 |
| 2 | ✅ API | `net/http` handlers, auth + origin middleware, SSE hub, `health`, `summary`, **group + bulk endpoints** | `curl` drives the full CRUD including group create/move/pause/delete; SSE prints a transition live; API tests green | 2.5 |
| 3a | ✅ UI (browser) | Vite/React/Tailwind, grouped + flat dashboard, group manager, bulk move, device modal with inheritance placeholders, detail + uPlot + uptime strip, settings | Full UI working in Chrome on macOS against `-dev` | 3.75 |
| 3b | ✅ Tauri shell | install `rustup`, Tauri v2 init, tray, single-instance, autostart, CSP, service-down banner | `npm run tauri dev` runs the same UI natively | 1 |
| 4 | ✅ Rollups & hardening | janitor, retention, DPAPI secrets, rate limit + **group-scoped digest**, restart recovery, graceful shutdown | 7-day soak with 50 fake devices across 6 groups: flat memory, DB bounded, no lost heartbeats across restarts, a simulated site outage produces one digest | 1.75 |
| 5 | Windows service + installer | `svcrun`, subcommands, Event Log, `setup.iss`, `release.yml` | Tagged build yields a signed `Setup.exe` that installs, starts and survives reboot on a clean Windows 11 VM | 2 |
| 6 | Acceptance & docs | manual checklist, README, troubleshooting, log/DB locations | Checklist in §12 passes end to end; v1.0.0 released | 1 |

**~15.5 days**, of which grouping accounts for about 1.5 — a quarter-day of schema and
resolution, half a day of API and digest routing, three quarters of a day of UI. Cheap because it
went in at design time; retrofitting inheritance onto non-nullable columns and a flat dashboard
later would cost several times that.

Phase 3a is the long pole and depends only on Phase 2's API contract — freeze that contract at
the end of Phase 2 and 3a can run in parallel with 4.

---

## 14. Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Tauri cannot cross-compile from macOS | Cannot produce the GUI locally | Windows CI job from day one (Phase 0), not day 40; UI developed in a plain browser meanwhile |
| No Windows machine for acceptance | Service/SYSTEM/SmartScreen bugs ship unseen | Confirm a Windows 11 VM or box is available before Phase 5 — CI cannot substitute |
| `modernc.org/sqlite` write throughput | Missed heartbeats at high device counts | Batched single-writer transactions + WAL; benchmark 200 devices @ 10 s in Phase 4. Fallback (`mattn/go-sqlite3`) costs CGO and the macOS loop — avoid |
| Alerting depends on the same network being monitored | The outage mail never arrives | Durable outbox with backoff (§7); a switch outage sends one digest once connectivity returns |
| Gmail/M365 need app passwords or OAuth | "Send test email" fails confusingly | Return the raw SMTP error to the UI; document app-password setup per provider |
| Unsigned service binary flagged by AV/SmartScreen | Users cannot install | Budget a code-signing cert; sign in CI |
| Loopback API readable by any local user | Local config tampering | Bearer token + origin pinning now; named-pipe option documented as the hardened path |
| DST / clock changes skew daily rollups | Wrong uptime % twice a year | Store UTC epochs, roll up with a single explicit `localtime` conversion, DST test case |
| Three-tier inheritance confuses users | "Why is this device checking every 60 s?" support load | The API returns the resolved value *and* its `Source`; the form shows `30 (from Warehouse)` as placeholder text and offers "reset to inherited" |
| A group delete taking its devices with it | Silent, unrecoverable data loss | `ON DELETE SET NULL`, never cascade; the confirmation dialog states devices move to `Ungrouped`; a test asserts it |
| Scope creep (ICMP ping, HTTP checks, SNMP, webhooks, nested groups) | Slips v1 | v1 is TCP-only, email-only. `Prober` is an interface precisely so HTTP/ICMP checks drop in later without touching the state machine |

---

## 14a. Deviations taken during the build

Places where the implementation departs from this plan, all deliberate:

| Plan said | Built as | Why |
|---|---|---|
| `core.Start` starts the API (§10), and the layout puts handlers in `internal/api` | Same, but the dependency points **api ← core**: `api.Engine` is an interface (`Reload`, `CheckNow`, `Uptime`, `Running`, `SchedulerLagMS`, `SendTestEmail`, `SaveSMTPPassword`) that `*core.App` satisfies | The alternative is an import cycle. It also means the handlers test against a stub with no scheduler, database writer or clock — the API tests are fast and deterministic because of it |
| Host pinned to `127.0.0.1:49215` exactly | Host pinned by **hostname** (`127.0.0.1`, `localhost`, `::1`), any port | The rebinding attack turns on the hostname — an attacker's page arrives carrying their DNS name. The port is configurable (`-api-addr`) and is whatever the OS handed out under `httptest`, so pinning it would only break the tests, not an attacker |
| Bearer token in the `Authorization` header | Same, with no query-parameter fallback | Which rules out the browser `EventSource` API for `/api/events`, since it cannot set headers — the UI reads the stream with `fetch()`. A token in a URL ends up in logs and history, and that is worse than one extra line of client code |
| `heartbeats?max_points` returns decimated raw rows | Returns **buckets**: `{t, avg_latency_ms, max_latency_ms, checks, downs}`, decimated by SQLite | A 90-day window at 10 s is 777 000 rows for a chart that asked for a thousand points, so the thinning has to happen in SQL. Carrying max and a down count per bucket is what keeps a single 900 ms spike or a two-minute outage from being averaged into invisibility |
| Uptime endpoints read `rollups_daily` | Read rollups **and** fill any day the janitor has not aggregated from raw heartbeats, tagging each day `source: rollup \| raw \| mixed` | The janitor is Phase 4, so without this every uptime strip is empty until then and Phase 3a has nothing to build against. `source` is what stops the UI reading a raw day as an authoritative one — and once rollups exist they win, so the endpoint does not change shape |
| Explicit DACL on `api.token` (`SYSTEM` + `Administrators` full, `Users` read) | Written `0600`, which on Windows means it inherits the `ProgramData` ACL — the same effective grants | Setting a DACL explicitly needs a Windows host to verify on, which is Phase 5. Noted as a compromise in `internal/api/token.go`, not silently skipped |
| — | `store.ErrDuplicate` / `ErrConstraint`, classified by matching SQLite's constraint message text | The alternative is importing the driver's error type into the store's public error contract. A duplicate device is the user's mistake — a 409, not a 500 — and something has to make that call |
| Nothing about CORS | `allowCORS` middleware for the already-pinned origins, answering preflights | The UI is never same-origin with the service: the Tauri webview is `tauri://localhost` and the dev server `http://localhost:5173`, and both send an `Authorization` header, which is not CORS-safelisted — so every request is preceded by a preflight that carries no credentials. It gives nothing away: the origin allowlist is the same one that stops DNS rebinding, and a page on a loopback origin still needs the token |
| UI reads the API directly in dev | Vite proxies `/api` and injects the bearer token from `.dev-data/api.token` | A browser cannot read the token file, so the alternative is pasting a token into the app after every `rotate-token`. Proxying also makes the dev loop same-origin, so it does not depend on the CORS path above. The UI keeps its own token handling for the Tauri shell and for a browser pointed straight at the service |
| The GUI window declared in `tauri.conf.json` | Built in `setup()` with `WebviewWindowBuilder` | A statically-declared window cannot carry an `initialization_script`, and the API token has to be in the page *before* its bundle evaluates — the client reads `window.__MONITOR_TOKEN__` at module scope. Injecting it afterwards would flash the "not authorised" banner on every launch |
| — | The tray tooltip is pushed from the page (`set_tray_status`), not polled by Rust | The window is already subscribed to the event stream, so a poller in the shell would double the load on the API to learn what the page knows already. The cost is that the tooltip stops updating if the webview dies — acceptable, because the tray is a convenience and the service is unaffected either way |
| CSP `connect-src http://127.0.0.1:49215` | Same, plus `http://localhost:49215` | Both names resolve to the same loopback interface and either can end up in a URL; allowing only one turns a working configuration into a blank screen with a console error. The origin allowlist on the service side is the check that actually matters |
| Autostart, unqualified | Autostart applies to the *window* only, and the UI says so | The service already starts with the machine, signed in or not. Without the distinction on screen, unticking the box reads as "stop monitoring at boot", which would be the opposite of what it does |
| Collapse window unstated; §7 describes a 120 s grouping window | `alert.collapse_sec`, default **15 s**, with a 120 s ceiling | Collapsing cannot be done without waiting, and the wait is added to every alert — so the default is the smallest window that still catches a site failing together, not the widest. Two things keep the cost down: a group whose every member has already failed flushes immediately, since nothing more can arrive; and 0 restores immediate per-device mail. §12's acceptance figure moves from ~90 s to ~105 s because of this, which is the honest accounting |
| Mail rate limit, behaviour unspecified | At the cap, one notice goes out saying mail is paused, and the rest is withheld | The alternative is dropping alerts silently, and mail that stops without explanation reads as "all clear" — the exact failure this system exists to prevent. Nothing is lost either way: the incidents are in the database and on the dashboard, and only email is capped. The count comes from the outbox rather than memory, so a restart cannot be used to reset the budget |
| Janitor prunes on a schedule | Also skips aggregating days already past the rollup window, and never touches today | Writing a summary that the same pass then deletes is pure waste — and a service that has been off longer than the retention window would do it hundreds of times. Today is excluded because a half-finished day would be frozen as the whole day's numbers and never revisited |
| Node version unstated | Node 24 LTS, pinned in CI and in `ui/package.json` engines | `jsdom` 30 requires ≥24.15, and pinning the major keeps a developer's machine and CI on one runtime. TypeScript is held at 6.0.3 rather than the current 7.x because `typescript-eslint` supports `<6.1` — type-aware linting is worth more here than being on the newest compiler |
| `scheduler/effective.go` owns the resolution chain | `model/effective.go` (`model.Resolve`) | `state` and `notify` both need the resolved values; putting the type in `scheduler` would have made the state machine import the scheduler, which is backwards. `model` has no dependencies, so nothing gains one |
| DPAPI secret sealing in Phase 4 | Built in Phase 1 (`internal/secret`) | The notifier needs the SMTP password to send anything, and there is no acceptable interim state where that password sits in the database as plaintext. Windows uses DPAPI at machine scope; elsewhere AES-GCM under a 0600 key file, so the dev loop is not plaintext either. **Exercised on Windows in Phase 2**: `PUT /api/settings` with a password stores `smtp.password_enc = dpapi:AQAAANCMnd8…` and `POST /api/settings/test-email` unseals it and reaches the SMTP dial, so both directions now have a real run behind them (still unverified under `LocalSystem`, which is Phase 5) |

**Three bugs the Phase 3a work caught, all in code written the same day.** The
device form dereferenced `device.group_id` after checking `device === undefined`
on a prop typed `Device | null`, so *Add device* crashed the page — now guarded,
and an error boundary means a render fault costs one screen rather than blanking
a monitoring dashboard, which reads exactly like "everything is fine". The
dashboard built its sections from the groups list alone, so a device whose group
was missing from that list vanished silently; leftovers now land in an Ungrouped
section. And `@custom-variant dark` had been redefined to a `.dark` class that
nothing ever sets, which would have left every `dark:` utility in the app
permanently inert — Tailwind 4 already follows `prefers-color-scheme`, so the
override is gone. The first two were found by driving the real UI against the
real service; no unit test had covered either path.

**A Phase 1 bug the first Windows run caught.** `probe.classify` matched
`syscall.ECONNREFUSED`, and Winsock does not reuse the POSIX errno numbers — so
on Windows, the only platform this ships on, *every refused connection was
classified `OTHER`*. The dashboard badge and the alert subject would have read
"OTHER" where they should read "REFUSED", losing precisely the distinction §4
says the classification exists to draw: a silent host is a different problem
from a host that answered "no". Fixed with `platformClass` in
`probe/classify_windows.go` (Winsock numbers from `golang.org/x/sys/windows`)
and a no-op `classify_other.go` elsewhere. CI runs on Linux, where the POSIX
branch is correct and `TestProbeRefused` passes, so nothing caught it until the
suite ran on Windows. The lesson for Phase 5: **CI green on Linux is not
evidence about the target platform**, and the acceptance checklist in §12 needs
to be run, not assumed.

One bug the earlier tests caught, worth recording because the shape of it will recur:
`state.Apply` clears the snapshot's open-incident id as part of recovering, so
the evaluator had nothing left to close the incident with — outages resolved in
memory but stayed open in the database. Fixed by carrying `IncidentID` on the
`Transition` rather than reading it back off the snapshot. The rule this implies:
**anything the caller must act on belongs on the transition, not on the state the
transition just mutated.**

---

## 15. Deliberate deviations from the source document

| # | Source doc | This plan | Why |
|---|---|---|---|
| 1 | `DATETIME` text timestamps | `INTEGER` unix epoch UTC | Unambiguous timezone, smaller index, correct sorting |
| 2 | Downtime derived from `heartbeats` | `incidents` table | Survives service restarts mid-outage; recovery email needs an exact duration |
| 3 | Loopback bind as the security model | Bearer token + origin pinning + remote-addr check | A loopback port is reachable by every local process and by DNS-rebinding |
| 4 | SMTP password in `settings` | DPAPI-sealed blob, never returned by the API | Plaintext mail credentials in a world-readable DB are the worst bug in the original design |
| 5 | `smtp.SendMail` only | Explicit STARTTLS / implicit-TLS / plain paths | `SendMail` silently fails against port-465 servers |
| 6 | Fire-and-forget email | Durable `alert_outbox` with backoff + digest | The alert about a network outage cannot be sent over that outage |
| 7 | No retention story | Raw pruning + `rollups_daily` | 200 devices @ 30 s is ~40 MB/day, unbounded |
| 8 | GUI polls the API | SSE `/api/events` | Sub-second badges, one connection |
| 9 | Chart.js for latency + uptime bars | uPlot for latency, CSS grid for uptime | 2 880 points/device renders faster; 90 blocks is markup |
| 10 | Installer calls `sc.exe` | `monitor-service.exe install` via `svc/mgr` | Avoids `binPath=` quoting bugs; sets failure/restart actions properly |
| 11 | Worker pool | One goroutine per device + a 64-wide semaphore | Per-device intervals are the natural unit; the semaphore is what actually needs bounding |
| 12 | No pause/maintenance fields | `enabled`, `paused_until`, `notify` | The doc's UI has a Pause button with nothing behind it |
| 13 | Flat device list, no grouping | First-class `groups` with inherited probe defaults, group alert routing, group maintenance windows, derived group uptime | A 200-device flat table is unreadable, and per-device recipients are a field nobody maintains. Groups are also the natural digest key for a site outage |

---

## 16. Open questions

1. **Windows acceptance host** — VM, spare machine, or none? Blocks Phase 5 sign-off, nothing earlier.
2. **Device count** — sized for 200. If it is closer to 1 000, the writer becomes a real
   bottleneck and heartbeats should be downsampled at write time.
3. **Code-signing certificate** — buy one, or ship unsigned and accept the SmartScreen warning?
4. **Multi-machine later?** v1 is one machine. If several sites need monitoring, the split is
   agent (Go) + central server, which changes the API's trust model — worth knowing now.
5. **Retention default** — 14 days raw. Longer if you ever need per-probe forensics beyond two weeks.
6. **Group shape** — I have planned flat, one-group-per-device (§3.1). Say so now if you actually
   need either **nesting** (site → rack → device, roughly +1 day for the tree UI and recursive
   queries) or **many-to-many** membership, because the latter changes what group uptime can mean
   and is not a later migration.
