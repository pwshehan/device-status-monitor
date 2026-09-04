import { useEffect, useId, useRef, type ReactNode } from 'react'

import { ApiError, ServiceDownError } from '../api/client'

// Small shared primitives. Deliberately not a component library: the whole UI
// is seven screens, and a dependency would cost more than these forty lines.

type ButtonTone = 'primary' | 'default' | 'danger' | 'ghost'

const buttonTones: Record<ButtonTone, string> = {
  primary: 'bg-accent-600 text-white hover:bg-accent-700 disabled:bg-accent-600/50',
  default:
    'bg-white text-slate-700 ring-1 ring-slate-300 hover:bg-slate-50 disabled:text-slate-400 dark:bg-slate-800 dark:text-slate-200 dark:ring-slate-600 dark:hover:bg-slate-700',
  danger: 'bg-down-600 text-white hover:bg-down-500 disabled:bg-down-600/50',
  ghost:
    'text-slate-600 hover:bg-slate-100 hover:text-slate-900 dark:text-slate-300 dark:hover:bg-slate-800 dark:hover:text-white',
}

interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  tone?: ButtonTone
  size?: 'sm' | 'md'
}

export function Button({ tone = 'default', size = 'md', className = '', ...rest }: ButtonProps) {
  return (
    <button
      type="button"
      className={[
        'inline-flex items-center justify-center gap-1.5 rounded-md font-medium transition-colors disabled:cursor-not-allowed',
        size === 'sm' ? 'px-2 py-1 text-xs' : 'px-3 py-1.5 text-sm',
        buttonTones[tone],
        className,
      ].join(' ')}
      {...rest}
    />
  )
}

interface FieldProps {
  label: string
  /** Shown under the control; the place inheritance is explained. */
  hint?: ReactNode
  error?: string | undefined
  children: (id: string) => ReactNode
}

export function Field({ label, hint, error, children }: FieldProps) {
  const id = useId()
  return (
    <div className="space-y-1">
      <label htmlFor={id} className="block text-sm font-medium text-slate-700 dark:text-slate-200">
        {label}
      </label>
      {children(id)}
      {error !== undefined && error !== '' ? (
        <p className="text-xs text-down-600 dark:text-down-500">{error}</p>
      ) : hint !== undefined ? (
        <p className="text-xs text-slate-500 dark:text-slate-400">{hint}</p>
      ) : null}
    </div>
  )
}

export const inputClass =
  'w-full rounded-md border-0 bg-white px-2.5 py-1.5 text-sm text-slate-900 ring-1 ring-slate-300 placeholder:text-slate-400 focus:ring-2 focus:ring-accent-500 dark:bg-slate-800 dark:text-slate-100 dark:ring-slate-600'

interface ModalProps {
  title: string
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
  wide?: boolean
}

/**
 * A dialog with a focus trap and Escape to close.
 *
 * Uses <dialog> rather than a hand-rolled overlay so the browser handles the
 * modal semantics, the top layer and the backdrop.
 */
export function Modal({ title, onClose, children, footer, wide = false }: ModalProps) {
  const ref = useRef<HTMLDialogElement>(null)

  useEffect(() => {
    const dialog = ref.current
    if (dialog === null) return
    if (!dialog.open) dialog.showModal()

    const onCancel = (event: Event) => {
      event.preventDefault()
      onClose()
    }
    dialog.addEventListener('cancel', onCancel)
    return () => dialog.removeEventListener('cancel', onCancel)
  }, [onClose])

  return (
    <dialog
      ref={ref}
      aria-label={title}
      className={[
        'm-auto w-[calc(100vw-2rem)] rounded-lg bg-white p-0 text-slate-900 shadow-xl backdrop:bg-slate-900/40 dark:bg-slate-900 dark:text-slate-100',
        wide ? 'max-w-3xl' : 'max-w-lg',
      ].join(' ')}
      onClick={(event) => {
        // Clicking the backdrop closes; clicking the card does not.
        if (event.target === ref.current) onClose()
      }}
    >
      <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-slate-700">
        <h2 className="text-base font-semibold">{title}</h2>
        <Button tone="ghost" size="sm" onClick={onClose} aria-label="Close">
          ✕
        </Button>
      </div>
      <div className="max-h-[70vh] overflow-y-auto px-4 py-4">{children}</div>
      {footer !== undefined && (
        <div className="flex justify-end gap-2 border-t border-slate-200 px-4 py-3 dark:border-slate-700">
          {footer}
        </div>
      )}
    </dialog>
  )
}

interface ConfirmProps {
  title: string
  message: ReactNode
  confirmLabel?: string
  tone?: ButtonTone
  busy?: boolean
  onConfirm: () => void
  onCancel: () => void
}

export function Confirm({
  title,
  message,
  confirmLabel = 'Confirm',
  tone = 'danger',
  busy = false,
  onConfirm,
  onCancel,
}: ConfirmProps) {
  return (
    <Modal
      title={title}
      onClose={onCancel}
      footer={
        <>
          <Button onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
          <Button tone={tone} onClick={onConfirm} disabled={busy}>
            {busy ? 'Working…' : confirmLabel}
          </Button>
        </>
      }
    >
      <div className="text-sm text-slate-600 dark:text-slate-300">{message}</div>
    </Modal>
  )
}

/**
 * Renders whatever went wrong.
 *
 * Field-level validation errors are shown on the control instead, so anything
 * reaching here is either a conflict, a service problem or something
 * unexpected — and in every one of those cases the service's own words are
 * more useful than a generic apology.
 */
export function ErrorNote({ error }: { error: unknown }) {
  if (error === null || error === undefined) return null

  // Anything that is not an Error is stringified defensively: String() on a
  // plain object yields "[object Object]", which tells the user nothing.
  let message: string
  if (error instanceof ServiceDownError || error instanceof ApiError) message = error.message
  else if (error instanceof Error) message = error.message
  else if (typeof error === 'string') message = error
  else message = 'Something went wrong. The log has the detail.'

  return (
    <p
      role="alert"
      className="rounded-md bg-down-100 px-3 py-2 text-sm text-down-600 dark:bg-down-600/15 dark:text-down-100"
    >
      {message}
    </p>
  )
}

export function Tile({
  label,
  value,
  tone = 'default',
  hint,
}: {
  label: string
  value: ReactNode
  tone?: 'default' | 'up' | 'down' | 'warn'
  hint?: string
}) {
  const tones = {
    default: 'text-slate-900 dark:text-slate-100',
    up: 'text-up-600 dark:text-up-500',
    down: 'text-down-600 dark:text-down-500',
    warn: 'text-warn-600 dark:text-warn-500',
  }
  return (
    <div
      title={hint}
      className="rounded-lg bg-white px-4 py-3 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700"
    >
      <div className="text-xs font-medium uppercase tracking-wide text-slate-500 dark:text-slate-400">
        {label}
      </div>
      <div className={`mt-1 text-2xl font-semibold tabular-nums ${tones[tone]}`}>{value}</div>
    </div>
  )
}

export function Spinner({ label = 'Loading' }: { label?: string }) {
  return (
    <div className="flex items-center gap-2 py-8 text-sm text-slate-500 dark:text-slate-400">
      <span className="size-4 animate-spin rounded-full border-2 border-slate-300 border-t-accent-500" />
      {label}…
    </div>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-slate-300 px-4 py-10 text-center text-sm text-slate-500 dark:border-slate-700 dark:text-slate-400">
      {children}
    </div>
  )
}
