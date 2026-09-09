import { useEffect } from 'react'
import { HashRouter, NavLink, Route, Routes } from 'react-router-dom'

import { ServiceDownBanner } from '../components/ServiceDownBanner'
import { useHealth } from '../hooks/queries'
import { setTrayStatus } from '../lib/shell'
import { Dashboard } from '../pages/Dashboard'
import { DeviceDetail } from '../pages/DeviceDetail'
import { GroupDetail } from '../pages/GroupDetail'
import { Groups } from '../pages/Groups'
import { Service } from '../pages/Service'
import { Settings } from '../pages/Settings'
import { ErrorBoundary } from './ErrorBoundary'
import { useStream } from './StreamContext'
import { StreamProvider } from './StreamProvider'

const tabs = [
  { to: '/', label: 'Dashboard' },
  { to: '/groups', label: 'Groups' },
  { to: '/settings', label: 'Settings' },
  { to: '/service', label: 'Service' },
]

export function App() {
  return (
    // HashRouter, not BrowserRouter: this is served as files by the Tauri
    // shell and by `vite preview`, neither of which rewrites unknown paths to
    // index.html, so a real path would 404 on reload.
    <HashRouter>
      <StreamProvider>
        <Shell />
      </StreamProvider>
    </HashRouter>
  )
}

function Shell() {
  const health = useHealth()
  const stream = useStream()

  // Keep the tray tooltip in step with the badge counts. A no-op in a browser.
  const { up, down, paused } = health.data ?? { up: 0, down: 0, paused: 0 }
  useEffect(() => {
    void setTrayStatus(up, down, paused)
  }, [up, down, paused])

  return (
    <div className="min-h-dvh">
      <ServiceDownBanner error={health.error} health={health.data} />

      <header className="border-b border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
        <div className="mx-auto flex max-w-7xl flex-wrap items-center gap-4 px-4 py-2">
          <span className="font-semibold">Local Device Monitor</span>

          <nav className="flex gap-1">
            {tabs.map((tab) => (
              <NavLink
                key={tab.to}
                to={tab.to}
                end={tab.to === '/'}
                className={({ isActive }) =>
                  `rounded-md px-3 py-1.5 text-sm ${
                    isActive
                      ? 'bg-accent-50 font-medium text-accent-700 dark:bg-accent-700/25 dark:text-accent-100'
                      : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'
                  }`
                }
              >
                {tab.label}
              </NavLink>
            ))}
          </nav>

          <div className="ml-auto flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
            <span
              title={
                stream.state === 'open'
                  ? 'Receiving live updates'
                  : 'Not receiving live updates — reconnecting'
              }
              className={`size-2 rounded-full ${
                stream.state === 'open'
                  ? 'bg-up-500'
                  : stream.state === 'connecting'
                    ? 'animate-pulse bg-warn-500'
                    : 'bg-down-500'
              }`}
            />
            {stream.state === 'open' ? 'live' : stream.state}
            {health.data !== undefined && (
              <span className="ml-2 tabular-nums">
                {health.data.up} up · {health.data.down} down
              </span>
            )}
          </div>
        </div>
      </header>

      <main className="mx-auto max-w-7xl px-4 py-6">
        <ErrorBoundary>
          <Routes>
            <Route path="/" element={<Dashboard />} />
            <Route path="/devices/:id" element={<DeviceDetail />} />
            <Route path="/groups" element={<Groups />} />
            <Route path="/groups/:id" element={<GroupDetail />} />
            <Route path="/settings" element={<Settings />} />
            <Route path="/service" element={<Service />} />
            <Route
              path="*"
              element={<p className="text-sm text-slate-500">That page does not exist.</p>}
            />
          </Routes>
        </ErrorBoundary>
      </main>
    </div>
  )
}
