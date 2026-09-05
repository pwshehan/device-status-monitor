import { describe, expect, it } from 'vitest'

import { mergeChecks, type RowHistory } from './useHistory'
import { makeDevice } from '../test/server'

describe('mergeChecks', () => {
  const at = (secondsAgo: number) => new Date(Date.now() - secondsAgo * 1000).toISOString()

  it('uses the server baseline so a row is not blank on load', () => {
    const device = makeDevice({ recent_checks: 'UUDU', last_check_at: at(10) })
    expect(mergeChecks(device, undefined)).toEqual(['UP', 'UP', 'DOWN', 'UP'])
  })

  it('appends live checks that arrived after the fetch', () => {
    const device = makeDevice({ recent_checks: 'UU', last_check_at: at(60) })
    const live: RowHistory = [
      { t: Math.floor(Date.now() / 1000) - 30, status: 'DOWN' },
      { t: Math.floor(Date.now() / 1000) - 5, status: 'UP' },
    ]
    expect(mergeChecks(device, live)).toEqual(['UP', 'UP', 'DOWN', 'UP'])
  })

  it('does not draw a live check twice once the refetch includes it', () => {
    // The baseline already covers this instant, so appending it again would
    // show one check as two.
    const now = Math.floor(Date.now() / 1000)
    const device = makeDevice({ recent_checks: 'UUD', last_check_at: new Date(now * 1000).toISOString() })
    const live: RowHistory = [{ t: now, status: 'DOWN' }]
    expect(mergeChecks(device, live)).toEqual(['UP', 'UP', 'DOWN'])
  })

  it('copes with a device that has never been checked', () => {
    const device = makeDevice({ recent_checks: '', last_check_at: null })
    expect(mergeChecks(device, undefined)).toEqual([])

    const live: RowHistory = [{ t: Math.floor(Date.now() / 1000), status: 'UP' }]
    expect(mergeChecks(device, live)).toEqual(['UP'])
  })
})
