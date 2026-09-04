import { useState } from 'react'

import { ApiError } from '../api/client'
import type { Settings as ApiSettings } from '../api/types'
import { useSaveSettings, useSettings, useTestEmail } from '../hooks/queries'
import { Button, ErrorNote, Field, Spinner, inputClass } from '../components/ui'

interface Form {
  host: string
  port: string
  security: 'starttls' | 'tls' | 'none'
  username: string
  password: string
  from: string
  recipients: string
  reminder: string
  collapse: string
  maxPerHour: string
  interval: string
  timeout: string
  failure: string
  recovery: string
  rawDays: string
  rollupDays: string
}

/** Seeds the editable form from what the service currently holds. */
function formOf(s: ApiSettings): Form {
  return {
    host: s.smtp.host,
    port: String(s.smtp.port),
    security: s.smtp.security,
    username: s.smtp.username,
    // Never the real password: the service does not return it. Blank means
    // "leave whatever is stored alone".
    password: '',
    from: s.smtp.from,
    recipients: s.alerts.recipients,
    reminder: String(s.alerts.reminder_sec),
    collapse: String(s.alerts.collapse_sec),
    maxPerHour: String(s.alerts.max_per_hour),
    interval: String(s.defaults.check_interval_sec),
    timeout: String(s.defaults.timeout_sec),
    failure: String(s.defaults.failure_threshold),
    recovery: String(s.defaults.recovery_threshold),
    rawDays: String(s.retention.raw_days),
    rollupDays: String(s.retention.rollup_days),
  }
}

export function Settings() {
  const settings = useSettings()

  if (settings.isPending) return <Spinner label="Loading settings" />
  if (settings.error !== null) return <ErrorNote error={settings.error} />
  if (settings.data === undefined) return null

  // Keyed on the fetch timestamp so the form re-initialises when the service
  // reports new values — a settings write from another window means what is on
  // screen is out of date. Resetting state with a key beats syncing it in an
  // effect: no cascading render, and no half-updated form if a field is left
  // out of the sync.
  return (
    <SettingsForm
      key={settings.dataUpdatedAt}
      settings={settings.data}
      onDiscard={() => void settings.refetch()}
    />
  )
}

interface FormProps {
  settings: ApiSettings
  onDiscard: () => void
}

function SettingsForm({ settings: stored, onDiscard }: FormProps) {
  const save = useSaveSettings()
  const test = useTestEmail()

  const [form, setForm] = useState<Form>(() => formOf(stored))
  const [saved, setSaved] = useState(false)

  const set = <K extends keyof Form>(key: K, value: Form[K]) => {
    setForm({ ...form, [key]: value })
    setSaved(false)
  }

  const error: unknown = save.error
  const field = error instanceof ApiError ? error.field : undefined
  const errorFor = (name: string): string | undefined =>
    field === name && error instanceof ApiError ? error.message : undefined

  const submit = () => {
    save.mutate(
      {
        smtp: {
          host: form.host,
          port: Number(form.port),
          security: form.security,
          username: form.username,
          from: form.from,
          // Omitted entirely when blank, which is what makes "leave blank to
          // keep the stored password" work.
          ...(form.password === '' ? {} : { password: form.password }),
        },
        alerts: {
          recipients: form.recipients,
          reminder_sec: Number(form.reminder),
          collapse_sec: Number(form.collapse),
          max_per_hour: Number(form.maxPerHour),
        },
        defaults: {
          check_interval_sec: Number(form.interval),
          timeout_sec: Number(form.timeout),
          failure_threshold: Number(form.failure),
          recovery_threshold: Number(form.recovery),
        },
        retention: { raw_days: Number(form.rawDays), rollup_days: Number(form.rollupDays) },
      },
      {
        onSuccess: () => {
          setSaved(true)
          setForm({ ...form, password: '' })
        },
      },
    )
  }

  return (
    <div className="max-w-3xl space-y-6">
      <div>
        <h1 className="text-xl font-semibold">Settings</h1>
        <p className="text-sm text-slate-500 dark:text-slate-400">
          The defaults here are the bottom of the inheritance chain: a group overrides them, and a
          device overrides its group.
        </p>
      </div>

      {error !== null && field === undefined && <ErrorNote error={error} />}
      {saved && (
        <p className="rounded-md bg-up-100 px-3 py-2 text-sm text-up-600 dark:bg-up-600/15 dark:text-up-100">
          Saved. Every device that inherits these values has been rescheduled.
        </p>
      )}

      <section className="space-y-4 rounded-lg bg-white p-4 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
        <h2 className="text-sm font-semibold">Email (SMTP)</h2>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Server" error={errorFor('smtp.host')}>
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={form.host}
                onChange={(e) => set('host', e.target.value)}
                placeholder="smtp.office365.com"
              />
            )}
          </Field>

          <Field label="Port" error={errorFor('smtp.port')}>
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={form.port}
                inputMode="numeric"
                onChange={(e) => set('port', e.target.value)}
              />
            )}
          </Field>

          <Field
            label="Encryption"
            error={errorFor('smtp.security')}
            hint="587 is usually STARTTLS; 465 is implicit TLS; 25 on an internal relay is often none."
          >
            {(id) => (
              <select
                id={id}
                className={inputClass}
                value={form.security}
                onChange={(e) => set('security', e.target.value as Form['security'])}
              >
                <option value="starttls">STARTTLS (587)</option>
                <option value="tls">TLS (465)</option>
                <option value="none">None (25)</option>
              </select>
            )}
          </Field>

          <Field label="Username" error={errorFor('smtp.username')}>
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={form.username}
                autoComplete="off"
                onChange={(e) => set('username', e.target.value)}
              />
            )}
          </Field>

          <Field
            label="Password"
            error={errorFor('smtp.password')}
            hint={
              stored.smtp.has_password
                ? 'A password is stored. Leave blank to keep it.'
                : 'Providers with MFA usually need an app password rather than the account one.'
            }
          >
            {(id) => (
              <input
                id={id}
                type="password"
                className={inputClass}
                value={form.password}
                autoComplete="new-password"
                placeholder={stored.smtp.has_password ? '••••••••' : ''}
                onChange={(e) => set('password', e.target.value)}
              />
            )}
          </Field>

          <Field label="From address" error={errorFor('smtp.from')}>
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={form.from}
                onChange={(e) => set('from', e.target.value)}
                placeholder="monitor@example.com"
              />
            )}
          </Field>
        </div>

        <Field
          label="Alert recipients"
          error={errorFor('alerts.recipients')}
          hint="Comma separated. A group with its own recipients overrides this for its members."
        >
          {(id) => (
            <input
              id={id}
              className={inputClass}
              value={form.recipients}
              onChange={(e) => set('recipients', e.target.value)}
              placeholder="ops@example.com"
            />
          )}
        </Field>

        <Field
          label="Repeat while down (seconds)"
          error={errorFor('alerts.reminder_sec')}
          hint="0 means one alert per outage. 3600 re-sends hourly until it recovers."
        >
          {(id) => (
            <input
              id={id}
              className={inputClass}
              value={form.reminder}
              inputMode="numeric"
              onChange={(e) => set('reminder', e.target.value)}
            />
          )}
        </Field>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label="Group failures together for (seconds)"
            error={errorFor('alerts.collapse_sec')}
            hint={
              form.collapse === '0'
                ? 'Off: every device sends its own message, immediately.'
                : `A site failing together becomes one message. Costs ${form.collapse}s of delay on every alert.`
            }
          >
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={form.collapse}
                inputMode="numeric"
                onChange={(e) => set('collapse', e.target.value)}
              />
            )}
          </Field>

          <Field
            label="Maximum emails per hour"
            error={errorFor('alerts.max_per_hour')}
            hint={
              form.maxPerHour === '0'
                ? 'No cap.'
                : 'At the cap, one message says mail is paused. Incidents are still recorded.'
            }
          >
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={form.maxPerHour}
                inputMode="numeric"
                onChange={(e) => set('maxPerHour', e.target.value)}
              />
            )}
          </Field>
        </div>

        <div className="flex flex-wrap items-center gap-3">
          <Button onClick={() => test.mutate(undefined)} disabled={test.isPending}>
            {test.isPending ? 'Sending…' : 'Send test email'}
          </Button>
          {test.data?.sent === true && (
            <span className="text-sm text-up-600 dark:text-up-500">
              Sent to {test.data.to.join(', ')}.
            </span>
          )}
          {test.error !== null && (
            <span className="text-sm text-down-600 dark:text-down-500">
              {/* The server's own words: "535 Username and Password not accepted"
                  is what tells you an app password is needed. */}
              {test.error instanceof Error ? test.error.message : String(test.error)}
            </span>
          )}
        </div>
        <p className="text-xs text-slate-500 dark:text-slate-400">
          The test bypasses the retry queue and reports exactly what the mail server said. Save
          first — it uses the stored settings, not what is on screen.
        </p>
      </section>

      <section className="space-y-4 rounded-lg bg-white p-4 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
        <h2 className="text-sm font-semibold">Default probe settings</h2>
        <div className="grid gap-4 sm:grid-cols-4">
          <Field label="Interval (s)" error={errorFor('defaults.check_interval_sec')}>
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={form.interval}
                inputMode="numeric"
                onChange={(e) => set('interval', e.target.value)}
              />
            )}
          </Field>
          <Field label="Timeout (s)" error={errorFor('defaults.timeout_sec')}>
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={form.timeout}
                inputMode="numeric"
                onChange={(e) => set('timeout', e.target.value)}
              />
            )}
          </Field>
          <Field label="Failures → DOWN" error={errorFor('defaults.failure_threshold')}>
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={form.failure}
                inputMode="numeric"
                onChange={(e) => set('failure', e.target.value)}
              />
            )}
          </Field>
          <Field label="Successes → UP" error={errorFor('defaults.recovery_threshold')}>
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={form.recovery}
                inputMode="numeric"
                onChange={(e) => set('recovery', e.target.value)}
              />
            )}
          </Field>
        </div>
        <p className="text-xs text-slate-500 dark:text-slate-400">
          At {form.failure} failures on a {form.interval}s interval, an outage is reported after
          about {Number(form.failure) * Number(form.interval)}s.
        </p>
      </section>

      <section className="space-y-4 rounded-lg bg-white p-4 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
        <h2 className="text-sm font-semibold">Retention</h2>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label="Keep raw checks (days)"
            error={errorFor('retention.raw_days')}
            hint="Every individual probe result. This is what the latency chart reads."
          >
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={form.rawDays}
                inputMode="numeric"
                onChange={(e) => set('rawDays', e.target.value)}
              />
            )}
          </Field>
          <Field
            label="Keep daily summaries (days)"
            error={errorFor('retention.rollup_days')}
            hint="One row per device per day. This is what the 90-day strip reads."
          >
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={form.rollupDays}
                inputMode="numeric"
                onChange={(e) => set('rollupDays', e.target.value)}
              />
            )}
          </Field>
        </div>
      </section>

      <div className="flex items-center gap-3">
        <Button tone="primary" onClick={submit} disabled={save.isPending}>
          {save.isPending ? 'Saving…' : 'Save settings'}
        </Button>
        <Button onClick={onDiscard} disabled={save.isPending}>
          Discard changes
        </Button>
      </div>
    </div>
  )
}
