import { createContext, useContext } from 'react'

import type { RowHistory } from '../components/DeviceTable'
import type { StreamState } from '../hooks/useEventStream'

export interface StreamContextValue {
  state: StreamState
  rows: Map<number, RowHistory>
  recentlyChanged: Set<number>
}

/**
 * Lives in its own module so StreamProvider.tsx exports nothing but a
 * component — which is what lets Vite's fast refresh work on it.
 */
export const StreamContext = createContext<StreamContextValue>({
  state: 'connecting',
  rows: new Map(),
  recentlyChanged: new Set(),
})

export function useStream(): StreamContextValue {
  return useContext(StreamContext)
}
