import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  DEFAULT_FROM,
  DEFAULT_TO,
  parseIsoDate,
  parseOutletId,
  parseReportFilter,
  validateReportRange,
} from './reportFilter.ts'

describe('parseIsoDate', () => {
  it('parses valid calendar dates including leap days', () => {
    assert.equal(parseIsoDate('2026-01-01'), Date.UTC(2026, 0, 1))
    assert.equal(parseIsoDate('2024-02-29'), Date.UTC(2024, 1, 29))
  })

  it('rejects malformed and impossible calendar dates', () => {
    for (const raw of ['', 'bad', '2026-1-1', '2026-02-29', '2026-04-31', '2026-13-01']) {
      assert.equal(parseIsoDate(raw), null, `expected null for ${JSON.stringify(raw)}`)
    }
  })
})

describe('parseOutletId', () => {
  it('accepts positive 32-bit integers and falls back to 1 otherwise', () => {
    assert.equal(parseOutletId('7'), 7)
    assert.equal(parseOutletId('2147483647'), 2147483647)
    for (const raw of [null, '', 'abc', '0', '-3', '1.5', '2147483648']) {
      assert.equal(parseOutletId(raw), 1, `expected 1 for ${JSON.stringify(raw)}`)
    }
  })
})

describe('parseReportFilter', () => {
  it('falls back to defaults on missing or invalid query params', () => {
    assert.deepEqual(parseReportFilter(new URLSearchParams()), {
      outletId: 1,
      from: DEFAULT_FROM,
      to: DEFAULT_TO,
    })
    assert.deepEqual(parseReportFilter(new URLSearchParams('outlet=abc&from=bad&to=2026-02-31')), {
      outletId: 1,
      from: DEFAULT_FROM,
      to: DEFAULT_TO,
    })
  })

  it('preserves valid params and intentionally cleared empty date strings', () => {
    assert.deepEqual(parseReportFilter(new URLSearchParams('outlet=4&from=2026-02-01&to=2026-02-28')), {
      outletId: 4,
      from: '2026-02-01',
      to: '2026-02-28',
    })
    assert.deepEqual(parseReportFilter(new URLSearchParams('outlet=4&from=&to=2026-02-28')), {
      outletId: 4,
      from: '',
      to: '2026-02-28',
    })
  })
})

describe('validateReportRange', () => {
  it('allows ranges up to 366 inclusive days and rejects inverted, empty, or oversized ranges', () => {
    assert.equal(validateReportRange('2026-01-01', '2026-01-01'), null)
    assert.equal(validateReportRange('2026-01-01', '2026-12-31'), null)
    assert.equal(validateReportRange('2024-01-01', '2024-12-31'), null) // 366 days (leap year)

    assert.ok(validateReportRange('', '2026-01-31'))
    assert.ok(validateReportRange('2026-02-01', '2026-01-31'))
    assert.ok(validateReportRange('2024-01-01', '2025-01-01')) // 367 days
  })
})
