# Local Device Monitor

Windows-native TCP endpoint monitoring. A Go service probes, alerts and stores;
a Tauri desktop app (not yet built) is a thin client over a loopback REST API;
SQLite in WAL mode is the single store.

See [PLAN.md](PLAN.md) for the full architecture and the phase plan.

## Status

| Phase | | |
|---|---|---|
| 0 | Scaffolding | **done** |
| 1 | Engine core — probe, state machine, incidents, groups, SMTP outbox | **done** |
| 2 | Local REST API + SSE | **done** |
| 3a | UI in the browser | next |
| 3b | Tauri shell | |
| 4 | Rollups, retention, hardening | |
| 5 | Windows service + installer | |
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
internal/api            HTTP handlers, auth + origin middleware, SSE hub
internal/core           wiring: evaluator, writer, refresh loop, API
internal/svcrun         Windows Service vs. foreground
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
