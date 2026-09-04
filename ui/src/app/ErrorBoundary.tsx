import { Component, type ErrorInfo, type ReactNode } from 'react'

interface Props {
  children: ReactNode
}

interface State {
  error: Error | null
}

/**
 * Catches a render fault and keeps the rest of the window usable.
 *
 * Without one, a single bad component unmounts the whole tree and leaves a
 * blank page — which on a monitoring dashboard is indistinguishable from
 * "everything is fine". The engine keeps probing and alerting either way; this
 * only decides how the failure is reported.
 */
export class ErrorBoundary extends Component<Props, State> {
  override state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  override componentDidCatch(error: Error, info: ErrorInfo): void {
    console.error('UI error', error, info.componentStack)
  }

  override render(): ReactNode {
    const { error } = this.state
    if (error === null) return this.props.children

    return (
      <div className="mx-auto max-w-2xl space-y-3 rounded-lg bg-white p-6 shadow-sm ring-1 ring-down-500/30 dark:bg-slate-900">
        <h1 className="text-lg font-semibold text-down-600 dark:text-down-500">
          This screen hit an error
        </h1>
        <p className="text-sm text-slate-600 dark:text-slate-300">
          Monitoring is unaffected — the service keeps probing and alerting whatever this window
          does. Reloading usually clears it.
        </p>
        <pre className="overflow-x-auto rounded bg-slate-100 p-3 text-xs text-slate-700 dark:bg-slate-800 dark:text-slate-200">
          {error.message}
        </pre>
        <button
          type="button"
          onClick={() => this.setState({ error: null })}
          className="rounded-md bg-accent-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-700"
        >
          Try again
        </button>
      </div>
    )
  }
}
