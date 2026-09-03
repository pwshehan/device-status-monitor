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
| 2 | Local REST API + SSE | next |
| 3a | UI in the browser | |
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

## Service commands (Windows only)

```
monitor-service.exe install       register the service (auto-start, LocalSystem)
monitor-service.exe uninstall     stop and remove it
monitor-service.exe start | stop
monitor-service.exe status
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
internal/core           wiring: evaluator, writer, refresh loop
internal/svcrun         Windows Service vs. foreground
```

## Configuration

Settings live in the `settings` table, seeded with defaults on first start.
Probe settings resolve `device ?? group ?? default.*`; see §3.2 of the plan.

To send mail, set `smtp.host`, `smtp.port`, `smtp.security`
(`starttls` | `tls` | `none`), `smtp.from` and `alert.recipients`. The password
is sealed before storage and never written as plaintext. Until Phase 2 there is
no UI for this, so set it directly:

```bash
sqlite3 .dev-data/monitor.db \
  "INSERT INTO settings(key,value) VALUES('smtp.host','smtp.example.com')
   ON CONFLICT(key) DO UPDATE SET value=excluded.value"
```
