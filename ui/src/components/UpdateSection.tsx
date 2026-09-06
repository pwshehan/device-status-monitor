import type { UpdateStatus } from '../api/types'
import { useCheckUpdate, useDownloadUpdate, useInstallUpdate, useUpdate } from '../hooks/queries'
import { bytes, since } from '../lib/format'
import { Button, ErrorNote } from './ui'

/**
 * What this machine knows about the newest release.
 *
 * The service does the checking and the downloading; this offers the one thing
 * it will not do on its own, which is run the installer. That separation is
 * deliberate: applying an update stops the service for a few seconds, and a
 * monitor deciding on its own to stop monitoring is not a decision it gets to
 * make.
 */
export function UpdateSection() {
  const update = useUpdate()
  const check = useCheckUpdate()
  const download = useDownloadUpdate()
  const install = useInstallUpdate()

  const u = update.data
  // Hidden entirely rather than shown as unavailable: on a build that cannot
  // apply an update — a dev run, anything but Windows — a section explaining
  // why is worse than no section at all.
  if (u === undefined || !u.supported) return null

  const busy = check.isPending || download.isPending || install.isPending

  return (
    <section className="space-y-3 rounded-lg bg-white p-4 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <h2 className="text-sm font-semibold">Updates</h2>
        <span className="text-xs text-slate-500 dark:text-slate-400">
          {u.last_checked_at === null ? 'not checked yet' : `checked ${since(u.last_checked_at)}`}
        </span>
      </div>

      <ErrorNote error={check.error ?? download.error ?? install.error} />

      <Body status={u} />

      <div className="flex flex-wrap items-center gap-2">
        {u.state === 'ready' && (
          <Button tone="primary" disabled={busy} onClick={() => install.mutate()}>
            Install now
          </Button>
        )}
        {u.state === 'available' && (
          <Button tone="primary" disabled={busy} onClick={() => download.mutate()}>
            Download
          </Button>
        )}
        {u.state !== 'installing' && (
          <Button disabled={busy || !u.enabled} onClick={() => check.mutate()}>
            Check now
          </Button>
        )}
        {u.notes_url !== undefined && u.notes_url !== '' && (
          <a
            className="text-sm text-accent-600 hover:underline dark:text-accent-500"
            href={u.notes_url}
            target="_blank"
            rel="noreferrer noopener"
          >
            Release notes
          </a>
        )}
      </div>
    </section>
  )
}

function Body({ status: u }: { status: UpdateStatus }) {
  const muted = 'text-sm text-slate-500 dark:text-slate-400'

  if (!u.enabled) {
    return (
      <p className={muted}>
        Checking for updates is turned off. Turn it back on under Settings, or upgrade by hand from
        the releases page.
      </p>
    )
  }

  switch (u.state) {
    case 'idle':
      return <p className={muted}>Version {u.current} is the latest release.</p>

    case 'available':
      return (
        <p className="text-sm">
          <strong>Version {u.latest} is available.</strong>{' '}
          <span className={muted}>You are on {u.current}.</span>
        </p>
      )

    case 'downloading': {
      // The byte count rather than a bare spinner: on a slow link this is the
      // only way to tell a download in progress from one that has stalled.
      const pct = u.size_bytes > 0 ? Math.round((u.downloaded_bytes / u.size_bytes) * 100) : 0
      return (
        <div className="space-y-1">
          <p className="text-sm">Downloading version {u.latest}…</p>
          <div className="h-1.5 overflow-hidden rounded-full bg-slate-200 dark:bg-slate-700">
            <div
              className="h-full bg-accent-600 transition-[width]"
              style={{ width: `${pct}%` }}
              role="progressbar"
              aria-valuenow={pct}
              aria-valuemin={0}
              aria-valuemax={100}
            />
          </div>
          <p className={`${muted} tabular-nums`}>
            {bytes(u.downloaded_bytes)}
            {u.size_bytes > 0 && ` of ${bytes(u.size_bytes)}`}
          </p>
        </div>
      )
    }

    case 'ready':
      return (
        <div className="space-y-1">
          <p className="text-sm">
            <strong>Version {u.latest} is downloaded and checked.</strong>{' '}
            <span className={muted}>You are on {u.current}.</span>
          </p>
          <p className={muted}>
            Installing stops the service for a few seconds and restarts it. Nothing is monitored
            during that gap, and the recorded history is not touched.
          </p>
        </div>
      )

    case 'installing':
      return (
        <p className="text-sm">
          Installing version {u.latest}…{' '}
          <span className={muted}>
            This window will lose contact with the service for a moment, then reconnect.
          </span>
        </p>
      )

    case 'error':
      return (
        <div className="space-y-1">
          <p className="text-sm text-down-600 dark:text-down-500">
            The last attempt failed: {u.error}
          </p>
          <p className={muted}>
            Nothing has been installed. You can try again, or upgrade by hand from the releases
            page.
          </p>
        </div>
      )
  }
}
