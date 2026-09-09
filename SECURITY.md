# Security policy

## Supported versions

| Version | Supported |
|---|---|
| 1.0.x   | Yes |
| < 1.0   | No — pre-release, please upgrade |

Only the latest released version is patched. There are no long-term support
branches.

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Use GitHub's private vulnerability reporting: go to the
[Security tab](https://github.com/pwshehan/local-device-monitor/security)
and choose **Report a vulnerability**. That opens a private advisory visible
only to the maintainer.

Please include what you were able to do, the version (`monitor-service.exe
version`), and enough detail to reproduce it. A proof of concept is welcome but
not required if the reasoning is clear.

Expect an acknowledgement within about a week. This is a small project
maintained in spare time — if a fix is going to take a while, you will be told
that rather than left waiting. Once a fix ships you will be credited in the
advisory and the changelog unless you would rather not be.

## Known and accepted limitations

These are documented design decisions, not oversights. Reporting them is
welcome only if you can show the consequence is worse than described here.

- **Any local administrator can read `api.token`.** The token lives in
  `C:\ProgramData\LocalMonitor\api.token` with an ACL granting
  `BUILTIN\Users` read access, which is what lets the dashboard authenticate
  without being elevated. A local administrator can therefore reconfigure
  monitoring. This is acceptable for a single-user workstation; the hardened
  alternative is a named pipe with an explicit DACL (PLAN.md §6).
- **Loopback is not the security model.** Every local process can reach a
  loopback port, so the API layers a bearer token, `Host`/`Origin` pinning and
  a `RemoteAddr` loopback check on top of it. A finding that relies only on
  "the port is reachable locally" is not by itself a vulnerability.
- **Releases are unsigned.** SmartScreen warns on first run. `SHA256SUMS` is
  published with each release and the in-app updater re-checks the hash in the
  moment before running the installer. Note precisely what that proves: the
  checksum travels alongside the file it describes, so it is *not* evidence the
  release is genuine — TLS to GitHub is what that rests on. It proves the file
  on disk is the file GitHub served, and that what runs is what was checked.
- **The SMTP password is sealed with DPAPI at machine scope**
  (`internal/secret`) and never returned by the API. Machine scope means any
  process running as an administrator on that machine can unseal it. That is
  the same trust boundary as `api.token` above.

## In scope

Anything that lets a **non-administrator** local user, or any remote party,
read or change monitoring data, read the SMTP password, escalate privilege, or
cause the service to execute code it should not — including:

- privilege escalation through the service, the installer or the update path
- a staged installer being writable by a standard user (the `updates\`
  directory should be SYSTEM and Administrators only)
- authentication or origin-pinning bypass on the local API
- SQL injection, path traversal, or unbounded resource consumption reachable
  through the API

## Out of scope

- Anything requiring local administrator rights already, unless it crosses a
  boundary the limitations above do not already concede
- SmartScreen or antivirus warnings on the unsigned build
- Vulnerabilities in dependencies with no demonstrated path through this code —
  report those upstream, though a heads-up here is appreciated
- Denial of service by pointing the monitor at a host that misbehaves; the
  prober's job is to survive that, so a crash *is* in scope, but slow probes
  are not
