import { useState } from 'react'

import { ApiError } from '../api/client'
import type { Group, GroupWrite, Settings } from '../api/types'
import { useCreateGroup, useUpdateGroup } from '../hooks/queries'
import { Button, ErrorNote, Field, Modal, inputClass } from './ui'

interface Props {
  group: Group | null
  defaults: Settings['defaults'] | undefined
  globalRecipients: string
  onClose: () => void
}

const swatches = ['#0d6a73', '#a83227', '#3d5a80', '#6a4c93', '#2f6b3f', '#8a6d1f']

const overrideFields = [
  { key: 'check_interval_sec', label: 'Check interval (s)', from: 'check_interval_sec' },
  { key: 'timeout_sec', label: 'Timeout (s)', from: 'timeout_sec' },
  { key: 'failure_threshold', label: 'Failures before DOWN', from: 'failure_threshold' },
  { key: 'recovery_threshold', label: 'Successes before UP', from: 'recovery_threshold' },
] as const

type OverrideKey = (typeof overrideFields)[number]['key']

/**
 * Add/edit group.
 *
 * A group's four probe settings inherit from the global defaults exactly as a
 * device's inherit from the group, so the same blank-means-inherit rule
 * applies one tier up — and the placeholder names the global default.
 */
export function GroupFormModal({ group, defaults, globalRecipients, onClose }: Props) {
  const editing = group !== null
  const create = useCreateGroup()
  const update = useUpdateGroup()

  const [name, setName] = useState(group?.name ?? '')
  const [description, setDescription] = useState(group?.description ?? '')
  const [color, setColor] = useState(group?.color ?? swatches[0] ?? '#0d6a73')
  const [notify, setNotify] = useState(group?.notify ?? true)
  const [recipients, setRecipients] = useState(group?.recipients ?? '')
  const [overrides, setOverrides] = useState<Record<OverrideKey, string>>({
    check_interval_sec: group?.check_interval_sec == null ? '' : String(group.check_interval_sec),
    timeout_sec: group?.timeout_sec == null ? '' : String(group.timeout_sec),
    failure_threshold: group?.failure_threshold == null ? '' : String(group.failure_threshold),
    recovery_threshold:
      group?.recovery_threshold == null ? '' : String(group.recovery_threshold),
  })

  const pending = create.isPending || update.isPending
  const error: unknown = create.error ?? update.error
  const fieldError = error instanceof ApiError ? error.field : undefined
  const errorFor = (field: string): string | undefined =>
    fieldError === field && error instanceof ApiError ? error.message : undefined

  const submit = () => {
    const body: GroupWrite = {
      name: name.trim(),
      description: description.trim(),
      color,
      notify,
      // Empty means "use the global list", not "send to nobody" — silencing a
      // group is what notify is for.
      recipients: recipients.trim() === '' ? null : recipients.trim(),
    }
    for (const f of overrideFields) {
      const raw = overrides[f.key].trim()
      Object.assign(body, { [f.key]: raw === '' ? null : Number(raw) })
    }

    const done = { onSuccess: onClose }
    if (editing) update.mutate({ id: group.id as number, body }, done)
    else create.mutate(body, done)
  }

  return (
    <Modal
      title={editing ? `Edit ${group.name}` : 'New group'}
      onClose={onClose}
      wide
      footer={
        <>
          <Button onClick={onClose} disabled={pending}>
            Cancel
          </Button>
          <Button tone="primary" onClick={submit} disabled={pending}>
            {pending ? 'Saving…' : editing ? 'Save' : 'Create group'}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {error !== null && fieldError === undefined && <ErrorNote error={error} />}

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Name" error={errorFor('name')}>
            {(id) => (
              <input
                id={id}
                className={inputClass}
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Warehouse"
                autoFocus
              />
            )}
          </Field>

          <Field label="Colour" error={errorFor('color')}>
            {(id) => (
              <div id={id} className="flex items-center gap-2">
                {swatches.map((s) => (
                  <button
                    key={s}
                    type="button"
                    aria-label={`Use ${s}`}
                    aria-pressed={color === s}
                    onClick={() => setColor(s)}
                    style={{ backgroundColor: s }}
                    className={`size-6 rounded-full ring-2 ${
                      color === s ? 'ring-slate-900 dark:ring-white' : 'ring-transparent'
                    }`}
                  />
                ))}
                <input
                  className={`${inputClass} ml-2 w-28 font-mono text-xs`}
                  value={color}
                  onChange={(e) => setColor(e.target.value)}
                  aria-label="Hex colour"
                />
              </div>
            )}
          </Field>
        </div>

        <Field label="Description">
          {(id) => (
            <input
              id={id}
              className={inputClass}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Site B, ground floor"
            />
          )}
        </Field>

        <fieldset className="rounded-md border border-slate-200 p-3 dark:border-slate-700">
          <legend className="px-1 text-xs font-medium uppercase tracking-wide text-slate-500 dark:text-slate-400">
            Probe defaults for members — blank means inherit
          </legend>
          <div className="grid gap-4 sm:grid-cols-2">
            {overrideFields.map((f) => {
              const globalValue = defaults?.[f.from]
              const value = overrides[f.key]
              return (
                <Field
                  key={f.key}
                  label={f.label}
                  error={errorFor(f.key)}
                  hint={
                    value === '' ? (
                      `Inheriting ${globalValue ?? '—'} (global default)`
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
                      placeholder={
                        globalValue === undefined
                          ? ''
                          : `${globalValue} (global default)`
                      }
                      onChange={(e) => setOverrides({ ...overrides, [f.key]: e.target.value })}
                    />
                  )}
                </Field>
              )
            })}
          </div>
        </fieldset>

        <Field
          label="Alert recipients"
          error={errorFor('recipients')}
          hint={
            recipients.trim() === ''
              ? `Using the global list: ${globalRecipients === '' ? 'none set' : globalRecipients}`
              : 'These addresses replace the global list for every member of this group.'
          }
        >
          {(id) => (
            <input
              id={id}
              className={inputClass}
              value={recipients}
              onChange={(e) => setRecipients(e.target.value)}
              placeholder="warehouse@example.com, ops@example.com"
            />
          )}
        </Field>

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={notify}
            onChange={(e) => setNotify(e.target.checked)}
            className="size-4 rounded border-slate-300 accent-accent-600"
          />
          Send email alerts for this group
          <span className="text-xs text-slate-500 dark:text-slate-400">
            (unticking silences every member, whatever their own setting)
          </span>
        </label>
      </div>
    </Modal>
  )
}
