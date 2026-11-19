import type { ReportFilter } from '../api'

// The seed covers 2025-07-01 to 2026-06-30, so a range relative to today can
// be empty. Default to a fixed quarter inside the seed.
export const DEFAULT_FROM = '2026-01-01'
export const DEFAULT_TO = '2026-03-31'
export const MAX_REPORT_DAYS = 366

const ISO_DATE = /^(\d{4})-(\d{2})-(\d{2})$/
const MAX_INT32 = 2_147_483_647

// Parses a calendar YYYY-MM-DD date to UTC midnight ms, rejecting impossible
// dates like 2026-02-31.
export function parseIsoDate(raw: string): number | null {
  const m = ISO_DATE.exec(raw)
  if (!m) return null
  const y = Number(m[1])
  const mo = Number(m[2])
  const d = Number(m[3])
  const ms = Date.UTC(y, mo - 1, d)
  const dt = new Date(ms)
  if (dt.getUTCFullYear() !== y || dt.getUTCMonth() !== mo - 1 || dt.getUTCDate() !== d) {
    return null
  }
  return ms
}

export function parseOutletId(raw: string | null): number {
  if (raw === null || !/^\d+$/.test(raw)) return 1
  const n = Number(raw)
  return Number.isSafeInteger(n) && n >= 1 && n <= MAX_INT32 ? n : 1
}

function parseDateParam(raw: string | null, fallback: string): string {
  if (raw === '') return ''
  if (raw !== null && parseIsoDate(raw) !== null) return raw
  return fallback
}

// Reads report filters from URL query params, replacing missing or malformed
// values with defaults so broken links never send NaN or invalid dates to the API.
export function parseReportFilter(params: Pick<URLSearchParams, 'get'>): ReportFilter {
  return {
    outletId: parseOutletId(params.get('outlet')),
    from: parseDateParam(params.get('from'), DEFAULT_FROM),
    to: parseDateParam(params.get('to'), DEFAULT_TO),
  }
}

export function validateReportRange(from: string, to: string): string | null {
  const start = parseIsoDate(from)
  const end = parseIsoDate(to)
  if (start === null || end === null) return 'Enter valid start and end dates.'
  if (end < start) return '“From” must be on or before “To”.'
  const days = Math.round((end - start) / 86_400_000) + 1
  if (days > MAX_REPORT_DAYS) return `Date range must be at most ${MAX_REPORT_DAYS} days.`
  return null
}
