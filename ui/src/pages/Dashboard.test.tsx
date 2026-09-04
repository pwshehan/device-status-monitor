import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'

import { StreamProvider } from '../app/StreamProvider'
import { Dashboard } from './Dashboard'
import { defaultSummary, makeDevice, makeGroup, server } from '../test/server'
import { renderWithProviders } from '../test/render'

function renderDashboard() {
  return renderWithProviders(
    <StreamProvider>
      <Dashboard />
    </StreamProvider>,
  )
}

describe('Dashboard', () => {
  it('shows a device with the group its settings come from', async () => {
    server.use(
      http.get('/api/devices', () =>
        HttpResponse.json({
          devices: [
            makeDevice({
              id: 7,
              name: 'Floor 2 switch',
              group_id: 2,
              group_name: 'Warehouse',
              effective: {
                ...makeDevice().effective,
                check_interval_sec: 15,
                source: { ...makeDevice().effective.source, interval: 'group:Warehouse' },
              },
            }),
          ],
        }),
      ),
      http.get('/api/groups', () =>
        HttpResponse.json({
          groups: [makeGroup({ id: 2, name: 'Warehouse', stats: { ...makeGroup().stats!, members: 1 } })],
        }),
      ),
    )

    renderDashboard()

    expect(await screen.findByText('Floor 2 switch')).toBeInTheDocument()
    expect(screen.getByText('Warehouse')).toBeInTheDocument()
    expect(screen.getByText('10.0.0.1:22')).toBeInTheDocument()
  })

  it('sorts a group with a member down to the top and flags the section', async () => {
    server.use(
      http.get('/api/groups', () =>
        HttpResponse.json({
          groups: [
            makeGroup({ id: 1, name: 'Quiet site', sort_order: 0 }),
            makeGroup({ id: 2, name: 'Broken site', sort_order: 1 }),
          ],
        }),
      ),
      http.get('/api/devices', () =>
        HttpResponse.json({
          devices: [
            makeDevice({ id: 1, name: 'fine', group_id: 1, status: 'UP' }),
            makeDevice({
              id: 2,
              name: 'broken',
              group_id: 2,
              status: 'DOWN',
              last_error: 'REFUSED: connection refused',
            }),
          ],
        }),
      ),
    )

    renderDashboard()

    await screen.findByText('broken')
    const headings = screen.getAllByRole('link', { name: /site$/ })
    // When something is wrong the answer belongs at the top of the page, not
    // wherever sort_order happens to put it.
    expect(headings[0]).toHaveTextContent('Broken site')
  })

  it('offers bulk actions once rows are selected, and moves in one call', async () => {
    const user = userEvent.setup()
    const bulk = vi.fn()

    server.use(
      http.get('/api/devices', () =>
        HttpResponse.json({
          devices: [
            makeDevice({ id: 1, name: 'one' }),
            makeDevice({ id: 2, name: 'two', ip_address: '10.0.0.2' }),
          ],
        }),
      ),
      http.get('/api/groups', () =>
        HttpResponse.json({ groups: [makeGroup({ id: 5, name: 'Warehouse' })] }),
      ),
      http.post('/api/devices/bulk', async ({ request }) => {
        bulk(await request.json())
        return HttpResponse.json({ op: 'move', affected: 2, ids: [1, 2] })
      }),
    )

    renderDashboard()

    await screen.findByText('one')
    await user.click(screen.getByLabelText('Select one'))
    await user.click(screen.getByLabelText('Select two'))

    expect(screen.getByText('2 selected')).toBeInTheDocument()

    await user.selectOptions(screen.getByLabelText('Move to group'), '5')

    // One request for the whole batch: that is what makes grouping usable
    // after adding forty devices.
    await waitFor(() => expect(bulk).toHaveBeenCalledTimes(1))
    expect(bulk).toHaveBeenCalledWith({ ids: [1, 2], op: 'move', group_id: 5 })
  })

  it('switches between grouped and flat, and remembers the choice', async () => {
    const user = userEvent.setup()
    renderDashboard()

    await screen.findByText('Core switch')
    await user.click(screen.getByRole('button', { name: 'flat' }))

    expect(window.localStorage.getItem('monitor.dashboard-mode')).toBe('flat')
    // Flat mode drops the group sections: when everything is on fire,
    // grouping is in the way.
    expect(screen.queryByRole('button', { name: 'Pause group' })).not.toBeInTheDocument()
  })

  it('lists open incidents with how long they have been down', async () => {
    server.use(
      http.get('/api/summary', () =>
        HttpResponse.json({
          ...defaultSummary,
          counts: { ...defaultSummary.counts, down: 1, up: 0 },
          open_incidents: [
            {
              id: 3,
              device_id: 1,
              device_name: 'Core switch',
              started_at: new Date(Date.now() - 600_000).toISOString(),
              detected_at: new Date(Date.now() - 570_000).toISOString(),
              resolved_at: null,
              duration_sec: 600,
              ongoing: true,
              cause: 'TIMEOUT: i/o timeout',
              alert_sent: true,
              recovery_sent: false,
            },
          ],
        }),
      ),
    )

    renderDashboard()

    const panel = await screen.findByRole('heading', { name: 'Open incidents' })
    const section = panel.closest('section')
    expect(section).not.toBeNull()
    expect(within(section as HTMLElement).getByText(/down 10 min/)).toBeInTheDocument()
  })

  it('warns before deleting a device, and says the history goes with it', async () => {
    const user = userEvent.setup()
    renderDashboard()

    await screen.findByText('Core switch')
    await user.click(screen.getByRole('button', { name: 'Delete' }))

    const dialog = await screen.findByRole('dialog', { name: /Delete Core switch/ })
    expect(within(dialog).getByText(/entire history/)).toBeInTheDocument()
  })
})
