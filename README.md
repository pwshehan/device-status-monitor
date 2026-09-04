# Local Device Monitor

Windows-native TCP endpoint monitoring. A Go service probes, alerts and stores;
a React dashboard in a Tauri desktop shell is a thin client over a loopback
REST API; SQLite in WAL mode is the single store. The same UI runs in a plain
browser, which is how it is developed.

See [PLAN.md](PLAN.md) for the full architecture and the phase plan.

## Status

| Phase | | |
|---|---|---|
| 0 | Scaffolding | **done** |
| 1 | Engine core — probe, state machine, incidents, groups, SMTP outbox | **done** |
| 2 | Local REST API + SSE | **done** |
| 3a | UI in the browser | **done** |
| 3b | Tauri shell | **done** |
| 4 | Rollups, retention, hardening | **done** |
| 5 | Windows service + installer | next |
| 6 | Acceptance and docs | |

## Developing on macOS or Linux

The engine is fully portable; only the Windows Service wrapper is behind a build
tag. Everything below works on this machine.

```bash
make seed      # create example groups and devices, then run in the foreground
make run       # run against ./.dev-data
make test      # go test ./...
make test-race # with the race detector
make lint      # vet + gofmt check
```

`make seed` creates two groups and three devices chosen to exercise all three
probe outcomes — one that answers, one that refuses, one that is silent — then
starts probing. Data lands in `./.dev-data`:

```
.dev-data/monitor.db          SQLite, WAL
.dev-data/secret.key          AES key for the SMTP password (non-Windows only)
.dev-data/logs/monitor.log    rotating log
```

Inspect what it recorded:

```bash
sqlite3 .dev-data/monitor.db "SELECT name, status, last_latency_ms FROM devices"
```

## The dashboard

Node 24 LTS. Start the service first — the dev server reads its token file and
attaches it to every proxied request, so nothing has to be pasted anywhere:

```bash
cd ui && npm ci && npm run dev
```

Then open http://localhost:5173. Requests to `/api` are proxied to the service
on 49215, which keeps the browser same-origin and means the token never reaches
the page.

Seven screens: a dashboard that groups devices by site (or goes flat for
triage), device detail with a uPlot latency chart and a 90-day availability
strip, the group manager, group detail, settings, and a service page showing
scheduler lag and where the files are. Status badges update over SSE rather
than polling.

```bash
cd ui && npm test
```

`npm run build` type-checks and produces `ui/dist`, which is what the desktop
shell loads.

## The desktop app

A Tauri v2 shell around the same UI. It needs Rust with the MSVC toolchain,
Visual Studio Build Tools with the C++ workload, and WebView2 (already on
Windows 11).

```bash
cd ui && npm run tauri:dev
```

```bash
cd ui && npm run tauri:build
```

The build produces `ui/src-tauri/target/release/local-monitor-gui.exe` (3.8 MB)
and `bundle/nsis/Local Monitor_0.1.0_x64-setup.exe` (1.7 MB), which installs it
as **Local Monitor**. Idle footprint is around 28 MB, because the window is a
WebView2 host and nothing else — the monitoring all happens in the service.

What the shell adds over the browser:

- **The token, without asking.** It reads `api.token` from the service's data
  folder and injects it before the page's own code runs, so there is nothing to
  paste. A missing token is not fatal — health still answers unauthenticated,
  so the window can say what is wrong.
- **A tray icon** with the up/down counts in its tooltip, pushed by the page
  rather than polled, so the shell adds no load of its own to the API.
- **Closing hides to the tray**, and the Quit item says *"Quit (monitoring
  continues)"* — because on a monitoring app, "quit" otherwise reads as "stop
  watching". Nothing the window does touches the service; use
  `monitor-service stop` for that.
- **One instance.** A second launch raises the existing window instead of
  opening a second event stream.
- **Optional autostart** for the window, on the Service page. The service
  starts with the machine either way, signed in or not.
- **A locked CSP**: `default-src 'self'; connect-src http://127.0.0.1:49215`,
  so the page can reach the monitoring service and nothing else.

## Building for Windows

The Go engine cross-compiles from any host:

```bash
make build-windows
```

The Tauri GUI and the Inno Setup installer **cannot** be cross-compiled; they
are built by the `windows-latest` job in CI. See §0 of the plan.

## The local API

The service listens on `127.0.0.1:49215`. Loopback is not the security model —
every local process can reach a loopback port — so there are three layers:
a bearer token, `Host`/`Origin` pinning, and a loopback check on `RemoteAddr`.

The token is generated on first start and written to `api.token` in the data
directory (`.dev-data/api.token` in dev mode). Reissue it with
`monitor-service rotate-token`, which needs a restart to take effect.

```bash
TOKEN=$(cat .dev-data/api.token)

curl -s localhost:49215/api/health | jq                       # no token needed
curl -s -H "Authorization: Bearer $TOKEN" localhost:49215/api/summary | jq
curl -s -H "Authorization: Bearer $TOKEN" localhost:49215/api/devices | jq

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

Every nullable probe setting is `null` when the row inherits, and each response
carries an `effective` block with the resolved value and where it came from, so
a form can show `30 (from Warehouse)` as placeholder text:

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

## History, retention and alert volume

Two hundred devices on a 30-second interval write about 576 000 rows a day, so
the service aggregates and prunes on an hourly pass:

- A **finished** day becomes one row per device in `rollups_daily`. Today is
  never aggregated — a half-finished day would be frozen as the whole day's
  numbers — so today's figures come from raw rows and everything older comes
  from summaries.
- **Downtime comes from the incident log**, clipped to each local day, not from
  counting DOWN rows. Counting rows assumes the interval never changed, loses
  the gap around a restart, and cannot split an outage that spans midnight.
- **Raw rows are pruned in 10 000-row chunks** past `retention.raw_days`
  (default 14). Their summaries survive, so the 90-day strip keeps working.
  Rollups and resolved incidents age out at `retention.rollup_days` (400) —
  but an **open** incident is never pruned, whatever its age.
- The WAL is checkpointed every pass; `VACUUM` only runs when a quarter of the
  file is free and at most daily, because it blocks everything.

`GET /api/health` reports the last pass, and the Service page shows it. A
`maintenance: null` after the service has been up a while means retention is
not running and the database is growing.

Alert volume is bounded in two ways:

- **A site outage is one email.** When three or more devices in one group fail
  inside the collapse window, they become one message — `[DOWN] Warehouse — 6
  of 8 devices unreachable`, naming each one. Recovery collapses the same way.
  Failures spread across unrelated groups collapse into a global digest at ten
  devices, because that has one cause upstream of all of them. Ungrouped
  devices never form a group digest.
- **A cap of `alert.max_per_hour`** (default 20). At the cap one message says
  mail is paused and the rest is withheld — never dropped silently, because
  mail that stops without explanation reads as "all clear".

The collapse costs latency: an alert arrives one window later than the state
change, so ~105 s on the defaults rather than ~90 s. A group whose every member
has failed flushes at once, and `alert.collapse_sec: 0` restores immediate
per-device mail.

## Service commands (Windows only)

```
monitor-service.exe install       register the service (auto-start, LocalSystem)
monitor-service.exe uninstall     stop and remove it
monitor-service.exe start | stop
monitor-service.exe status
monitor-service.exe rotate-token  issue a new API token
```

## Layout

```
cmd/monitor-service     entry point and subcommands
internal/model          domain types + the effective-value chain (Resolve)
internal/store          SQLite: migrations and every query
internal/probe          TCP dialer and error classification
internal/scheduler      one worker per device, bounded concurrency
internal/state          the state machine — pure, no I/O
internal/notify         SMTP transports, templates, outbox worker
internal/secret         DPAPI (Windows) / AES-GCM (elsewhere)
internal/rollup         the janitor: aggregate, prune, checkpoint, vacuum
internal/api            HTTP handlers, auth + origin middleware, SSE hub
internal/core           wiring: evaluator, writer, refresh loop, API
internal/svcrun         Windows Service vs. foreground
ui/src/api              typed client, SSE parser
ui/src/hooks            TanStack Query hooks, the event stream
ui/src/components       table, badges, uptime strip, sparkline, uPlot chart
ui/src/pages            dashboard, device, groups, group, settings, service
```

## Configuration

Settings live in the `settings` table, seeded with defaults on first start.
Probe settings resolve `device ?? group ?? default.*`; see §3.2 of the plan.

To send mail, set `smtp.host`, `smtp.port`, `smtp.security`
(`starttls` | `tls` | `none`), `smtp.from` and `alert.recipients`. The password
is sealed before storage and never returned by the API — `GET /api/settings`
reports `has_password: true` and nothing more, and a `PUT` that omits the
password field leaves the stored one alone.

```bash
TOKEN=$(cat .dev-data/api.token)

curl -s -X PUT -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' localhost:49215/api/settings \
  -d '{"smtp":{"host":"smtp.example.com","port":587,"security":"starttls",
               "username":"monitor","password":"app-password",
               "from":"monitor@example.com"},
       "alerts":{"recipients":"ops@example.com"}}' | jq

curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  localhost:49215/api/settings/test-email | jq
```

`test-email` bypasses the outbox and returns the SMTP server's own error text,
which is what tells you whether the provider wants an app password.
