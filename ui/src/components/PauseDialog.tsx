import { useState } from 'react'

import type { PauseRequest } from '../api/types'
import { Button, ErrorNote, Field, Modal, inputClass } from './ui'

interface Props {
  what: string
  /** Set when the target is already paused, so the dialog offers a resume. */
  pausedUntil: string | null
  busy: boolean
  error: unknown
  onSubmit: (body: PauseRequest) => void
  onClose: () => void
}

const presets = [15, 30, 60, 120, 480]

/**
 * The maintenance-window dialog, shared by devices and groups.
 *
 * A pause is always bounded. An open-ended one is how a device ends up
 * silently unmonitored for a year, so the API caps it at a month and the UI
 * offers presets plus an explicit end time — never "until I say so".
 */
export function PauseDialog({ what, pausedUntil, busy, error, onSubmit, onClose }: Props) {
  const [minutes, setMinutes] = useState('60')
  const [until, setUntil] = useState('')

  const paused = pausedUntil !== null

  return (
    <Modal
      title={paused ? `Resume ${what}` : `Pause ${what}`}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          {paused && (
            <Button tone="primary" onClick={() => onSubmit({ minutes: null })} disabled={busy}>
              Resume now
            </Button>
          )}
          {!paused && (
            <Button
              tone="primary"
              disabled={busy}
              onClick={() =>
                onSubmit(
                  until === ''
                    ? { minutes: Number(minutes) }
                    : { until: new Date(until).toISOString() },
                )
              }
            >
              {busy ? 'Working…' : 'Pause'}
            </Button>
          )}
        </>
      }
    >
      <div className="space-y-4">
        <ErrorNote error={error} />

        {paused ? (
          <p className="text-sm text-slate-600 dark:text-slate-300">
            Paused until{' '}
            <strong>{new Date(pausedUntil).toLocaleString()}</strong>. Nothing is being probed and
            no alerts will be sent until then.
          </p>
        ) : (
          <>
            <p className="text-sm text-slate-600 dark:text-slate-300">
              While paused, {what} is not probed at all — no checks, no history, no alerts.
            </p>

            <Field label="For how long" hint="Choose a preset, or set an exact end time below.">
              {(id) => (
                <div id={id} className="flex flex-wrap gap-2">
                  {presets.map((m) => (
                    <Button
                      key={m}
                      size="sm"
                      tone={until === '' && minutes === String(m) ? 'primary' : 'default'}
                      onClick={() => {
                        setMinutes(String(m))
                        setUntil('')
                      }}
                    >
                      {m < 60 ? `${m} min` : `${m / 60} h`}
                    </Button>
                  ))}
                </div>
              )}
            </Field>

            <Field label="Or until" hint="Local time.">
              {(id) => (
                <input
                  id={id}
                  type="datetime-local"
                  className={inputClass}
                  value={until}
                  onChange={(e) => setUntil(e.target.value)}
                />
              )}
            </Field>
          </>
        )}
      </div>
    </Modal>
  )
}
