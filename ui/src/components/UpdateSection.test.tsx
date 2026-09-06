import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'

import type { UpdateStatus } from '../api/types'
import { defaultUpdate, server } from '../test/server'
import { renderWithProviders } from '../test/render'
import { UpdateSection } from './UpdateSection'

function withStatus(overrides: Partial<UpdateStatus>) {
  server.use(
    http.get('/api/update', () => HttpResponse.json({ ...defaultUpdate, ...overrides })),
  )
}

describe('UpdateSection', () => {
  it('says nothing at all on a build that could not apply an update', async () => {
    withStatus({ supported: false, state: 'available', latest: '1.1.0' })
    renderWithProviders(<UpdateSection />)

    // A dev run or a non-Windows build. Explaining why the button is missing
    // would be worse than the section not being there.
    await waitFor(() => {
      expect(screen.queryByRole('heading', { name: 'Updates' })).not.toBeInTheDocument()
    })
    expect(screen.queryByRole('button', { name: 'Install now' })).not.toBeInTheDocument()
  })

  it('confirms the running version is current when there is nothing newer', async () => {
    renderWithProviders(<UpdateSection />)

    expect(await screen.findByText(/Version 1\.0\.0 is the latest release/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Install now' })).not.toBeInTheDocument()
  })

  it('offers to install once an update is downloaded and checked', async () => {
    withStatus({ state: 'ready', latest: '1.1.0', notes_url: 'https://example.test/v1.1.0' })

    let installed = 0
    server.use(
      http.post('/api/update/install', () => {
        installed++
        return HttpResponse.json(
          { ...defaultUpdate, state: 'installing', latest: '1.1.0' },
          { status: 202 },
        )
      }),
    )
    renderWithProviders(<UpdateSection />)

    expect(await screen.findByText(/Version 1\.1\.0 is downloaded and checked/)).toBeInTheDocument()
    // The gap in monitoring has to be said out loud before the button is pressed.
    expect(screen.getByText(/stops the service for a few seconds/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Release notes' })).toHaveAttribute(
      'href',
      'https://example.test/v1.1.0',
    )

    await userEvent.click(screen.getByRole('button', { name: 'Install now' }))

    await waitFor(() => expect(installed).toBe(1))
    expect(await screen.findByText(/Installing version 1\.1\.0/)).toBeInTheDocument()
  })

  it('offers a download instead when automatic downloads are off', async () => {
    withStatus({ state: 'available', latest: '1.1.0', auto_download: false })
    renderWithProviders(<UpdateSection />)

    expect(await screen.findByText(/Version 1\.1\.0 is available/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Download' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Install now' })).not.toBeInTheDocument()
  })

  it('shows how far a download has got', async () => {
    withStatus({ state: 'downloading', latest: '1.1.0', size_bytes: 1000, downloaded_bytes: 250 })
    renderWithProviders(<UpdateSection />)

    // A percentage rather than a spinner: on a slow link it is the only way to
    // tell a download in progress from one that has stalled.
    const bar = await screen.findByRole('progressbar')
    expect(bar).toHaveAttribute('aria-valuenow', '25')
  })

  it('reports a failure without claiming anything was installed', async () => {
    withStatus({ state: 'error', latest: '1.1.0', error: 'checksum mismatch' })
    renderWithProviders(<UpdateSection />)

    expect(await screen.findByText(/checksum mismatch/)).toBeInTheDocument()
    expect(screen.getByText(/Nothing has been installed/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Check now' })).toBeEnabled()
  })

  it('explains itself and stops offering to check when checking is turned off', async () => {
    withStatus({ enabled: false })
    renderWithProviders(<UpdateSection />)

    expect(await screen.findByText(/Checking for new releases is turned off/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Check now' })).toBeDisabled()
  })

  it('still says what is waiting when checking has been turned off', async () => {
    withStatus({ enabled: false, state: 'ready', latest: '1.1.0' })
    renderWithProviders(<UpdateSection />)

    // Turning checking off does not throw away an update already downloaded.
    // "Checking is off" over a bare Install button says nothing about what the
    // button would install.
    expect(await screen.findByText(/Version 1\.1\.0 is downloaded and checked/)).toBeInTheDocument()
    expect(screen.getByText(/Checking for new releases is turned off/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Install now' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Check now' })).toBeDisabled()
  })

  it('still reports a failure when checking has been turned off', async () => {
    withStatus({ enabled: false, state: 'error', error: 'the installer would not start' })
    renderWithProviders(<UpdateSection />)

    // A failure is the one thing that must not be swallowed by the notice.
    expect(await screen.findByText(/the installer would not start/)).toBeInTheDocument()
    expect(screen.getByText(/Nothing has been installed/)).toBeInTheDocument()
  })

  it('asks the service to check when told to', async () => {
    let checks = 0
    server.use(
      http.post('/api/update/check', () => {
        checks++
        return HttpResponse.json({ ...defaultUpdate, state: 'available', latest: '2.0.0' })
      }),
    )
    renderWithProviders(<UpdateSection />)

    await userEvent.click(await screen.findByRole('button', { name: 'Check now' }))

    await waitFor(() => expect(checks).toBe(1))
    expect(await screen.findByText(/Version 2\.0\.0 is available/)).toBeInTheDocument()
  })
})
