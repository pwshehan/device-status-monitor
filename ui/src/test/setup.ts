import '@testing-library/jest-dom/vitest'

import { cleanup } from '@testing-library/react'
import { afterAll, afterEach, beforeAll } from 'vitest'

import { server } from './server'

// MSW rather than a hand-stubbed fetch: the tests then exercise the real
// client, including its error-envelope handling, and a request the UI is not
// supposed to make shows up as an unhandled-request error instead of silently
// resolving to undefined.
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => {
  server.resetHandlers()
  cleanup()
})
afterAll(() => server.close())

// jsdom implements <dialog> but not the top layer, so showModal and close are
// simply absent. The component under test is right to use the native element —
// it gets focus trapping, Escape and the backdrop for free in a real browser —
// so the gap is patched here rather than designing around it.
beforeAll(() => {
  if (typeof HTMLDialogElement === 'undefined') return
  HTMLDialogElement.prototype.showModal = function showModal(this: HTMLDialogElement) {
    this.open = true
  }
  HTMLDialogElement.prototype.close = function close(this: HTMLDialogElement) {
    this.open = false
    this.dispatchEvent(new Event('close'))
  }
})
