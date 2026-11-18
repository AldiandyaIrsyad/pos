import { useSearchParams } from 'react-router'
import type { ReportFilter } from '../api'
import OutletSelect from '../ui/OutletSelect'
import DailySales from './DailySales'
import TopProducts from './TopProducts'

// The seed covers 2025-07-01 to 2026-06-30, so a range relative to today can
// be empty. Default to a fixed quarter inside the seed.
const DEFAULT_FROM = '2026-01-01'
const DEFAULT_TO = '2026-03-31'

// Filters live in the URL (?outlet=1&from=...&to=...): a refresh or a shared
// link shows the same report.
export default function ReportsPage() {
  const [params, setParams] = useSearchParams()
  const filter: ReportFilter = {
    outletId: Number(params.get('outlet') ?? 1),
    from: params.get('from') ?? DEFAULT_FROM,
    to: params.get('to') ?? DEFAULT_TO,
  }
  const rangeValid = filter.from !== '' && filter.to !== '' && filter.from <= filter.to

  function update(patch: Partial<ReportFilter>) {
    const next = { ...filter, ...patch }
    setParams({ outlet: String(next.outletId), from: next.from, to: next.to }, { replace: true })
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-end gap-4">
        <OutletSelect value={filter.outletId} onChange={(outletId) => update({ outletId })} />
        <DateInput label="From" value={filter.from} onChange={(from) => update({ from })} />
        <DateInput label="To" value={filter.to} onChange={(to) => update({ to })} />
        {!rangeValid && <p className="pb-2 text-sm text-red-700">“From” must be on or before “To”.</p>}
      </div>

      {rangeValid && (
        <div className="grid gap-6 lg:grid-cols-2">
          <DailySales filter={filter} />
          <TopProducts filter={filter} />
        </div>
      )}
    </div>
  )
}

function DateInput({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  return (
    <label className="flex flex-col gap-1 text-sm">
      <span className="font-medium text-slate-600">{label}</span>
      <input
        type="date"
        className="rounded border border-slate-300 bg-white px-2 py-1.5"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
    </label>
  )
}
