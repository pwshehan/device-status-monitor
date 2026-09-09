# Contributing

Forks, bug reports and pull requests are all welcome. This file is the short
version of what the project expects, and — more usefully — what it is trying to
be, so you can tell early whether a change belongs here.

## What this project is, and is not

It watches TCP endpoints from **one Windows machine** and emails when one stops
answering. That scope is deliberate, and two limits fall out of it:

- **TCP only.** No ICMP, HTTP or SNMP checks today. The prober is an interface
  (`internal/probe`) precisely so they can be added without touching the state
  machine, so a new prober is a welcome change.
- **One machine.** Watching several sites means an agent plus a central server,
  which changes the API's trust model entirely. That is a different program;
  see PLAN.md §16 before proposing it.

[PLAN.md](PLAN.md) carries the architecture and the reasoning behind it. If a
change contradicts something written there, that is worth discussing in an
issue first — the reasoning may simply be stale, but it is usually there for a
reason.

## Getting set up

You need **Go 1.25**, **Node 24 LTS**, and — for the desktop shell — **Rust
with the MSVC toolchain** plus Visual Studio Build Tools (C++ workload).
**Inno Setup 6** builds the installer.

```bash
go test ./...                  # the engine
cd ui && npm ci && npm test    # the dashboard
```

The `Makefile` wraps the common tasks (`make check` runs everything CI does
except the cross-builds), but it **assumes a POSIX shell** — it calls `cp`,
`rm -rf` and `tr`. On Windows either run it under Git Bash or invoke the
underlying `go` / `npm` / `ISCC` commands directly, mirroring
[.github/workflows/release.yml](.github/workflows/release.yml).

To run the engine against a throwaway database in `./.dev-data` rather than
ProgramData:

```bash
make run     # foreground
make seed    # same, plus example groups and devices
```

`make seed` deliberately creates one device that answers, one that refuses and
one that is silent, so all three probe outcomes are reachable without breaking
anything real.

For the dashboard in a plain browser — which is how it is developed — start the
engine first, then `cd ui && npm run dev`. The dev server proxies `/api` and
attaches the token from `.dev-data/api.token`, so nothing needs pasting.

## The Windows problem, which is the important part

This ships as a Windows service, but **the Go tests run on `ubuntu-latest` in
CI**. That gap has already cost a real defect: `probe.classify` matched
`syscall.ECONNREFUSED`, which Winsock never returns, so on Windows every
refused connection was classified `OTHER` instead of `REFUSED` — wrong in the
dashboard badge and in the alert subject, with Linux CI green the whole time.

So: **if your change touches sockets, credentials, the filesystem or the
service wrapper, run it on Windows before opening the PR**, and say in the PR
that you did. Green CI is not evidence about Windows.

`go test -race` needs cgo and therefore a C compiler; if you have none locally,
plain `go test ./...` is fine — CI runs the race detector on Linux.

Anything that needs a real machine to install on — the service registering with
the SCM, running as `LocalSystem`, surviving a reboot, the installer itself —
is not covered by any test. It is written out as a checklist in
[installer/ACCEPTANCE.md](installer/ACCEPTANCE.md), which takes about fifteen
minutes. A change to the installer or the service wrapper should be accompanied
by a run of it, and please say which sections you actually ran. Claiming a
reboot test you did not perform is worse than skipping it.

## What CI checks

- **Go** (ubuntu): `gofmt`, `go vet`, `go test -race ./...`, and a Windows
  cross-build
- **UI** (ubuntu): `npm run typecheck`, `npm run lint`, `npm test`, `npm run build`
- **Shell** (windows): `cargo fmt --check` and `cargo clippy --all-targets -D warnings`

All of it must pass. `gofmt` is worth a warning for Windows contributors: with
`core.autocrlf=true` your working tree is CRLF and `gofmt -l` will flag every
file, including ones you never touched. Check formatting against LF-normalised
copies; CI checks out LF and is unaffected.

## Style

Match the code around you. A few conventions that are not obvious:

- **`internal/state` is pure** — no I/O, no clock reads. The state machine
  takes inputs and returns decisions so it can be tested exhaustively. Keep it
  that way.
- Errors returned to the API are always `{"error":{"code","message","field"}}`.
- Comments explain *why*, not *what*. The codebase leans on this heavily; a
  comment restating the line above it will get flagged in review.

**Commit messages** are imperative, sentence case, and describe the effect
rather than the file — `Move the default API port out of the Windows ephemeral
range`, not `fix(api): update port`. No prefixes and no Conventional Commits.
If the change needs justifying, the body is the place.

## Pull requests

Open an issue first for anything structural; go straight to a PR for bugs,
docs and small changes. In the PR, say what the change does, why, and what you
ran to convince yourself — especially on which OS.

Releases are cut by the maintainer: `VERSION`, a matching `CHANGELOG.md`
section and a tag, gated by
[.github/workflows/release.yml](.github/workflows/release.yml). Please do not
bump `VERSION` in a PR; it will conflict with whatever the next release is.

## Licensing of contributions

This project is licensed under the **Apache License 2.0**. Contributions are
accepted under the same terms — inbound equals outbound, per section 5 of the
licence, which says that anything you deliberately submit for inclusion is
under the licence unless you say otherwise in the PR. There is no CLA to sign.

If you add a file, no per-file copyright header is required; the top-level
[LICENSE](LICENSE) and [NOTICE](NOTICE) cover the repository.
