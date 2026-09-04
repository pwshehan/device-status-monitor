import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { ApiError, ServiceDownError } from './api/client'
import { App } from './app/App'
import './index.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // The event stream is what makes data stale, so there is no polling
      // interval here and no refetch on focus: a transition arrives in
      // milliseconds, and a timer would only add load between transitions.
      staleTime: 30_000,
      refetchOnWindowFocus: false,
      retry: (failureCount, error) => {
        // A stale token or a validation error will not fix itself; a service
        // that is restarting will.
        if (error instanceof ApiError) return false
        if (error instanceof ServiceDownError) return failureCount < 2
        return failureCount < 1
      },
    },
    mutations: { retry: false },
  },
})

const root = document.getElementById('root')
if (root === null) throw new Error('index.html is missing #root')

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
)
