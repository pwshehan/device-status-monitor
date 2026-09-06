# Troubleshooting

Organised by what you can see, because that is what you have when something is
wrong. Each entry says how to tell the causes apart rather than listing every
possibility at once.

**The three places to look**, in the order they are usually useful:

| Where | What it holds |
|---|---|
| `C:\ProgramData\LocalMonitor\logs\monitor.log` | everything the service did, rotating at 10 MB |
| Event Viewer → Windows Logs → Application, source `LocalMonitorSvc` | failures to *start*, and warnings and errors only |
| `http://127.0.0.1:39215/api/health` | the service's own view of itself; needs no token |

Health is the fastest first check:

```
curl http://127.0.0.1:39215/api/health
```

---

## The dashboard says "The monitoring service is not running"

The window could not reach the API at all. Confirm which of the two it is:

```
sc.exe query LocalMonitorSvc
```

**`STATE: STOPPED` or the service does not exist** — see *the service will not
start*, below.

**`STATE: RUNNING` but the banner persists** — check the service is actually
listening:

```
netstat -ano | findstr 39215
```

If something *else* holds the port, the service would not have started at all
(below), so a running service with nothing listening is unusual — worth
reporting with the `api listening` line from `monitor.log`.

## The dashboard says "This window is not authorised"

The service is running but rejected the window's token, which almost always
means the token was rotated and the dashboard is still holding the old one.
Restart the dashboard — it re-reads `api.token` at startup.

If it persists, the file and the running service have diverged. Restart the
service so it re-reads the file:

```
monitor-service.exe stop
monitor-service.exe start
```

Note that `rotate-token` writes a *new* file but the running service keeps the
old token until it restarts. That is why the command tells you to restart.

## The service will not start

Look in **Event Viewer** first: a start failure is reported there because at
that point the log file may not exist yet.

**"start api on 127.0.0.1:39215: bind: Only one usage of each socket
address..."** — something already holds the API port, and the engine refuses to
start rather than run beside it. That is deliberate: the port doubles as the
single-instance guard, and two copies probing the same devices would double
every check and every alert.

Almost always it is another copy of the engine — most often a
`monitor-service.exe -dev` left running from a development session, which uses
the same port even though its database is elsewhere:

```
tasklist | findstr monitor-service
```

Stop it, or give it a different port with `-api-addr 127.0.0.1:39216`.

**"bind: An attempt was made to access a socket in a way forbidden by its
access permissions"** — this reads like a permissions problem and is not one.
Windows has reserved the port. Anything using WinNAT — Hyper-V, WSL2, Docker
Desktop, Windows Sandbox — reserves blocks of the ephemeral range (49152 and
up) at boot, and the blocks move between reboots, so a service that has worked
for months can fail to start after an unrelated restart.

The default port is 39215 precisely to stay out of that range, so you should
only see this if you have moved it with `-api-addr`. Check what is reserved:

```
netsh interface ipv4 show excludedportrange protocol=tcp
```

Then pick a port below 49152 that is not listed.

**"open database: ... locked" or "unable to open database file"** — the data
directory is not writable, or the file is held by something else. Usually an
antivirus quarantine or a locked-down ProgramData ACL.

**"prepare data directory"** — `C:\ProgramData\LocalMonitor` cannot be created
at all. The service runs as `LocalSystem`, so this means something is actively
blocking it rather than a permissions gap.

**Nothing in the Event Log at all** — the source may not be registered, which
happens if the service was created by hand rather than by
`monitor-service install`. Re-register it:

```
monitor-service.exe uninstall
monitor-service.exe install
monitor-service.exe start
```

**The service starts and immediately stops** — check the exit code with
`sc.exe query LocalMonitorSvc`. The engine exits non-zero rather than pretending
to run, so the SCM will restart it after 5 s, 10 s and 30 s before giving up.
A crash loop with nothing in the Event Log is worth reporting with the last
hundred lines of `monitor.log`.

---

## No email is arriving

Work down this list; each step rules out the one above it.

**1. Is SMTP configured and working?** Settings → **Send test email**. It
bypasses the queue and shows the server's own words:

| What the server said | What it means |
|---|---|
| `535 5.7.8 Username and Password not accepted` | Wrong password, or the provider wants an *app password* rather than the account one |
| `no such host` | The server name is wrong, or DNS is not resolving from this machine |
| `connection refused` / a hang | Wrong port for the encryption mode — 587 is STARTTLS, 465 is TLS, 25 is usually an internal relay |
| `STARTTLS is not offered by the server` | The port is not a STARTTLS port; try 465 with TLS |
| `550 5.7.1 ... not allowed to relay` | The relay does not accept mail from this machine, or `smtp.from` is not an address it will send as |

**2. Are there recipients?** An alert with nowhere to go is logged as
`alert suppressed: no recipients configured` and dropped. Set them globally in
Settings, or per group.

**3. Is the device configured to alert?** A group with alerts switched off
silences every member regardless of the member's own setting. The device page's
**Effective settings** shows the answer as `Alerts on` or `off`.

**4. Had the device ever been seen working?** A device that has never
succeeded records its outage but sends no email — it is far more likely to be a
wrong port than an incident. Fix the address and it will alert normally from
then on.

**5. Is mail paused?** At the hourly cap the service sends one *"Alert mail
paused"* message and withholds the rest. Raise **Maximum emails per hour** in
Settings if the limit is too low for what is happening.

**6. Is it queued but not delivered?** `/api/health` reports
`pending_alerts`. A number that keeps climbing means delivery is failing;
`monitor.log` has the reason on each attempt, and the retry schedule is
1 m → 5 m → 15 m → 1 h → 4 h before it gives up and logs at ERROR.

## The email arrived late

Expected: `failure_threshold × check_interval` to confirm the outage, then the
collapse window. On the defaults that is 90 s + 15 s.

To make it faster, lower the interval or the threshold for that device — but a
threshold of 1 means a single dropped packet is an outage. Set
**Group failures together** to 0 to remove the collapse delay, at the cost of
one email per device when a site goes down.

## One outage produced several emails

Devices only collapse together if they are **in the same group** and fail
within the window of each other. Ungrouped devices never collapse. If a site
sent six separate messages, its devices are probably not all in that group —
check the dashboard's grouped view.

---

## A device shows DOWN but it is fine

**Look at the reason on the row.** `REFUSED` means the machine answered and
nothing is listening on that port — the host is up, the service is not.
`TIMEOUT` means silence: the host, the network, or a firewall.

`REFUSED` on a device you believe is healthy is nearly always the wrong port.
Use **Check now** to probe immediately, and **Edit** to fix it.

A `TIMEOUT` on a device that pings fine is usually a firewall that drops
unsolicited connections to that port, or a device that only accepts connections
from certain addresses.

## A device is stuck at UNKNOWN

It has not been probed yet. Either it was just added — the first probe is
spread randomly across the interval so that adding forty devices does not make
them all dial at the same instant, for ever — or it is not being probed at all.
The Service page's **Monitoring** tile shows how many of the configured devices
have a live probe loop; a device that is paused or disabled is deliberately not
among them.

## The dashboard is not updating live

The header shows `live` when the event stream is connected. If it says
`connecting` or `closed`, the stream dropped and is reconnecting with backoff —
normal across a service restart, and it recovers on its own.

`/api/health` reports `events_dropped`. A number that climbs means this window
cannot keep up with the event rate and is losing updates; the figures are still
correct on refresh, since the stream only decides *when* to refetch.

---

## The database is growing

Check the Service page, or `/api/health`:

- **`maintenance` is null** after the service has been up for more than an hour
  — the janitor has not completed a pass. Look in `monitor.log` for `janitor
  pass` lines and for errors around them.
- **`heartbeats` keeps climbing** past roughly `devices × 86400 ÷ interval ×
  retention.raw_days` — pruning is not keeping up, or `retention.raw_days` is
  higher than you think.

Retention is in Settings. Lowering `raw_days` prunes on the next hourly pass;
the daily summaries are unaffected, so the 90-day view keeps working.

Space is only returned to the disk by a `VACUUM`, which the janitor runs at
most daily and only when a quarter of the file is free — it blocks everything
while it runs. A large retention change will therefore shrink the file on the
next pass, not immediately.

## Uptime figures look wrong after a clock change

Days are local calendar days, and the transitions are handled: a
spring-forward day is 23 hours and an autumn-back day is 25, and downtime is
clipped to those bounds. If a day either side of a transition looks wrong,
that is worth reporting — with the day, the timezone, and the device.

## History disappeared after an upgrade

It should not: upgrades replace the executables and never touch
`C:\ProgramData\LocalMonitor`. Check the folder still exists and that
`monitor.db` is not zero bytes.

If the *uninstaller* ran with "delete everything" chosen, the history is gone —
that is the one path that removes it, and it is not the default.

---

## Reporting something

Useful to include:

- what `/api/health` returns
- the last hundred lines of `C:\ProgramData\LocalMonitor\logs\monitor.log`
- any `LocalMonitorSvc` entries from Event Viewer
- `monitor-service.exe version`

The log contains device names, addresses and SMTP server names, but never the
SMTP password or the API token.
