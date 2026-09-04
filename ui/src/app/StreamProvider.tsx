import { useMemo, type ReactNode } from 'react'

import { useEventStream } from '../hooks/useEventStream'
import { useHistory } from '../hooks/useHistory'
import { StreamContext } from './StreamContext'

/**
 * Owns the single SSE connection for the whole app.
 *
 * One connection, not one per screen: the stream is also what invalidates the
 * query cache, and several connections would multiply every invalidation by
 * the number of mounted screens. Live latency history rides along for the
 * same reason.
 */
export function StreamProvider({ children }: { children: ReactNode }) {
  const history = useHistory()
  const state = useEventStream({ onEvent: history.onEvent })

  const value = useMemo(
    () => ({ state, rows: history.rows, recentlyChanged: history.recentlyChanged }),
    [state, history.rows, history.recentlyChanged],
  )

  return <StreamContext.Provider value={value}>{children}</StreamContext.Provider>
}
