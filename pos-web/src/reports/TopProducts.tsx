import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { getTopProducts, type ReportFilter, type TopProductsSort } from '../api'
import { formatNumber, formatRupiah } from '../format'
import AsyncView from '../ui/AsyncView'

export default function TopProducts({ filter }: { filter: ReportFilter }) {
  const [sort, setSort] = useState<TopProductsSort>('qty')
  const report = useQuery({
    queryKey: ['top-products', filter, sort],
    queryFn: ({ signal }) => getTopProducts(filter, sort, signal),
  })

  return (
    <section className="flex flex-col gap-3 rounded-lg bg-white p-4 shadow-sm">
      <div className="flex items-center justify-between gap-2">
        <h2 className="font-semibold">Top 10 products</h2>
        <div className="inline-flex rounded border border-slate-200 p-0.5 text-xs" role="group" aria-label="Sort top products">
          {(['qty', 'revenue'] as const).map((option) => (
            <button
              key={option}
              type="button"
              className={`rounded px-2 py-1 font-medium ${
                sort === option ? 'bg-slate-900 text-white' : 'text-slate-600 hover:bg-slate-100'
              }`}
              onClick={() => setSort(option)}
            >
              {option === 'qty' ? 'Quantity' : 'Revenue'}
            </button>
          ))}
        </div>
      </div>
      <AsyncView query={report} isEmpty={(rows) => rows.length === 0} emptyText="No products sold in this range.">
        {(rows) => {
          const metric = (r: (typeof rows)[number]) => (sort === 'qty' ? r.qty_sold : r.revenue)
          const maxValue = Math.max(...rows.map(metric), 1)
          return (
            <ol className="flex flex-col gap-2 text-sm">
              {rows.map((r, i) => (
                <li key={r.product_id} className="flex flex-col gap-1">
                  <div className="flex justify-between gap-3">
                    <span className="truncate">
                      <span className="mr-2 text-slate-400 tabular-nums">{i + 1}.</span>
                      {r.name}
                    </span>
                    <span className="shrink-0 tabular-nums text-slate-600">
                      {formatNumber(r.qty_sold)} pcs · {formatRupiah(r.revenue)}
                    </span>
                  </div>
                  <div className="h-2 rounded bg-slate-100">
                    <div className="h-2 rounded bg-sky-500" style={{ width: `${(metric(r) / maxValue) * 100}%` }} />
                  </div>
                </li>
              ))}
            </ol>
          )
        }}
      </AsyncView>
    </section>
  )
}
