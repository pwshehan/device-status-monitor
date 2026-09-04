import { useState } from 'react'

import { ApiError, api } from '../api/client'
import type { CheckResult, Device, DeviceWrite, Group, Settings } from '../api/types'
import { useCreateDevice, useUpdateDevice } from '../hooks/queries'
import type { SourceKey } from '../lib/format'
import { Button, ErrorNote, Field, Modal, inputClass } from './ui'

interface Props {
  device: Device | null
  groups: Group[]
  /** The global tier, for working out what a field would inherit. */
  defaults: Settings['defaults'] | undefined
  onClose: () => void
}

/** One inheritable field's state: empty string means "inherit". */
type Overrides = Record<SourceKey, string>

/** The four inheritable settings, and where each one lives on a group. */
const overrideFields: {
  key: SourceKey
  label: string
  body: 'check_interval_sec' | 'timeout_sec' | 'failure_threshold' | 'recovery_threshold'
}[] = [
  { key: 'interval', label: 'Check interval (s)', body: 'check_interval_sec' },
  { key: 'timeout', label: 'Timeout (s)', body: 'timeout_sec' },
  { key: 'failure_threshold', label: 'Failures before DOWN', body: 'failure_threshold' },
  { key: 'recovery_threshold', label: 'Successes before UP', body: 'recovery_threshold' },
]

/**
 * Add/edit device.
 *
 * The four inheritable fields are blank by default with the resolved value as
 * placeholder text — "30 (from Warehouse)". Leaving a field alone means
 * *inherit*; typing in it means *override*; clearing it resets to inherited.
 * That is the whole reason the form sends `null` rather than omitting a key.
 */
export function DeviceFormModal({ device, groups, defaults, onClose }: Props) {
  const editing = device !== null
  const create = useCreateDevice()
  const update = useUpdateDevice()

  const [name, setName] = useState(device?.name ?? '')
  const [host, setHost] = useState(device?.ip_address ?? '')
  const [port, setPort] = useState(device === null ? '' : String(device.port))
  const [groupId, setGroupId] = useState<string>(
    // The prop is Device | null, so the null check has to be against null:
    // `device === undefined` let the Add case fall through to device.group_id.
    device === null || device.group_id === null ? '' : String(device.group_id),
  )
  const [tags, setTags] = useState((device?.tags ?? []).join(', '))
  const [enabled, setEnabled] = useState(device?.enabled ?? true)
  const [notify, setNotify] = useState(device?.notify ?? true)
  const [overrides, setOverrides] = useState<Overrides>({
    interval: device?.check_interval_sec == null ? '' : String(device.check_interval_sec),
    timeout: device?.timeout_sec == null ? '' : String(device.timeout_sec),
    failure_threshold:
      device?.failure_threshold == null ? '' : String(device.failure_threshold),
    recovery_threshold:
      device?.recovery_threshold == null ? '' : String(device.recovery_threshold),
  })

  const [test, setTest] = useState<CheckResult | null>(null)
  const [testError, setTestError] = useState<unknown>(null)
  const [testing, setTesting] = useState(false)

  const pending = create.isPending || update.isPending
  const error: unknown = create.error ?? update.error
  const fieldError = error instanceof ApiError ? error.field : undefined
  const errorFor = (field: string): string | undefined =>
    fieldError === field && error instanceof ApiError ? error.message : undefined

  const selectedGroup =
    groupId === '' ? null : (groups.find((g) => String(g.id) === groupId) ?? null)

  /**
   * What this field would inherit, given the group currently chosen in the
   * form.
   *
   * Resolved from the picker rather than from device.effective, because the
   * group can be changed in this very dialog: showing "30 (global default)"
   * while Warehouse is selected and overrides it to 15 would be a lie about
   * what saving is going to do.
   */
  const placeholder = (key: SourceKey, field: (typeof overrideFields)[number]['body']): string => {
    const fromGroup = selectedGroup?.[field] ?? null
    if (fromGroup !== null && selectedGroup !== null) {
      return `${fromGroup} (from ${selectedGroup.name})`
    }
    const global = defaults?.[field]
    return global === undefined ? '' : `${global} (global default)`
  }

  const submit = () => {
    const body: DeviceWrite = {
      name: name.trim(),
      ip_address: host.trim(),
      port: Number(port),
      group_id: groupId === '' ? null : Number(groupId),
      enabled,
      notify,
      tags: tags
        .split(',')
        .map((t) => t.trim())
        .filter((t) => t !== ''),
    }
    // An empty box is an explicit null: reset to inherited. A filled one is an
    // override. There is no third state, which is why this cannot be a
    // "changed fields only" diff.
    for (const f of overrideFields) {
      const raw = overrides[f.key].trim()
      Object.assign(body, { [f.body]: raw === '' ? null : Number(raw) })
    }

    const done = { onSuccess: onClose }
    if (editing) update.mutate({ id: device.id, body }, done)
    else create.mutate(body, done)
  }

  /**
   * "Test connection now" before saving.
   *
   * For an existing device this asks the engine to probe it; for a new one
   * there is nothing to probe yet, so the button is only offered when editing.
   * Adding a device with the wrong port and finding out from the dashboard is
   * the case this avoids.
   */
  const runTest = async () => {
    if (device === null) return
    setTesting(true)
    setTest(null)
    setTestError(null)
    try {
      setTest(await api.checkDevice(device.id))
    } catch (err) {
      setTestError(err)
    } finally {
      setTesting(false)
    }
  }

  return (
    <Modal
      title={editing ? `Edit ${device.name}` : 'Add device'}
      onClose={onClose}
      wide
      footer={
        <>
          {editing && (
            <Button onClick={() => void runTest()} disabled={testing} className="mr-auto">
              {testing ? 'Testing…' : 'Test connection now'}
            </Button>
          )}
          <Button onClick={onClose} disabled={pending}>
            Cancel
          </Button>
          <Button tone="primary" onClick={submit} disabled={pending}>
            {pending ? 'Saving…' : editing ? 'Save' : 'Add device'}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {error !== null && fieldError === undefined && <ErrorNote error={error} />}

        {test !== null && (
          <p
            className={`rounded-md px-3 py-2 text-sm ${
              test.ok
                ? 'bg-up-100 text-up-600 dark:bg-up-600/15 dark:text-up-100'
                : 'bg-down-100 text-down-600 dark:bg-down-600/15 dark:text-down-100'
            }`}
          >
            {test.ok
              ? `Answered in ${test.latency_ms} ms.`
              : `No answer — ${test.error === '' ? test.class : test.error}`}
          </p>
        )}
        {testError !== null && <ErrorNote error={testError} />}

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Name" error={errorFor('name')}>
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Core switch"
                autoFocus
              />
            )}
          </Field>

          <Field label="Group" hint="Ungrouped devices inherit the global defaults.">
            {(id) => (
              <select
                id={id}
                className={inputClass}
                value={groupId}
                onChange={(e) => setGroupId(e.target.value)}
              >
                <option value="">Ungrouped</option>
                {groups
                  .filter((g) => g.id !== null)
                  .map((g) => (
                    <option key={g.id} value={String(g.id)}>
                      {g.name}
                    </option>
                  ))}
              </select>
            )}
          </Field>

          <Field label="Host or IP" error={errorFor('ip_address')}>
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={host}
                onChange={(e) => setHost(e.target.value)}
                placeholder="10.0.0.1"
              />
            )}
          </Field>

          <Field label="Port" error={errorFor('port')}>
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={port}
                onChange={(e) => setPort(e.target.value)}
                inputMode="numeric"
                placeholder="22"
              />
            )}
          </Field>
        </div>

        <fieldset className="rounded-md border border-slate-200 p-3 dark:border-slate-700">
          <legend className="px-1 text-xs font-medium uppercase tracking-wide text-slate-500 dark:text-slate-400">
            Probe settings — blank means inherit
          </legend>
          <div className="grid gap-4 sm:grid-cols-2">
            {overrideFields.map((f) => {
              const value = overrides[f.key]
              return (
                <Field
                  key={f.key}
                  label={f.label}
                  error={errorFor(f.body)}
                  hint={
                    value === '' ? (
                      `Inheriting ${placeholder(f.key, f.body)}`
                    ) : (
                      <button
                        type="button"
                        className="text-accent-600 hover:underline dark:text-accent-500"
                        onClick={() => setOverrides({ ...overrides, [f.key]: '' })}
                      >
                        Reset to inherited
                      </button>
                    )
                  }
                >
                  {(id) => (
                    <input
                      id={id}
                      className={inputClass}
                      value={value}
                      inputMode="numeric"
                      placeholder={placeholder(f.key, f.body)}
                      onChange={(e) => setOverrides({ ...overrides, [f.key]: e.target.value })}
                    />
                  )}
                </Field>
              )
            })}
          </div>
        </fieldset>

        <Field label="Tags" hint="Comma separated, for cross-cutting filters.">
          {(id) => (
            <input
              id={id}
              className={inputClass}
              value={tags}
              onChange={(e) => setTags(e.target.value)}
              placeholder="critical, customer-facing"
            />
          )}
        </Field>

        <div className="flex flex-wrap gap-6">
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
              className="size-4 rounded border-slate-300 accent-accent-600"
            />
            Monitor this device
          </label>
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={notify}
              onChange={(e) => setNotify(e.target.checked)}
              className="size-4 rounded border-slate-300 accent-accent-600"
            />
            Send email alerts
            {device !== null && !device.effective.notify && notify && (
              <span className="text-xs text-warn-600 dark:text-warn-500">
                (silenced by its group)
              </span>
            )}
          </label>
        </div>
      </div>
    </Modal>
  )
}
