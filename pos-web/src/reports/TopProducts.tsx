import { useQuery } from '@tanstack/react-query'
import { getTopProducts, type ReportFilter } from '../api'
import { formatNumber, formatRupiah } from '../format'
import AsyncView from '../ui/AsyncView'

export default function TopProducts({ filter }: { filter: ReportFilter }) {
  const report = useQuery({
    queryKey: ['top-products', filter],
    queryFn: ({ signal }) => getTopProducts(filter, signal),
  })

  return (
    <section className="flex flex-col gap-3 rounded-lg bg-white p-4 shadow-sm">
      <h2 className="font-semibold">Top 10 products by quantity</h2>
      <AsyncView query={report} isEmpty={(rows) => rows.length === 0} emptyText="No products sold in this range.">
        {(rows) => {
          const maxQty = Math.max(...rows.map((r) => r.qty_sold))
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
                    <div className="h-2 rounded bg-sky-500" style={{ width: `${(r.qty_sold / maxQty) * 100}%` }} />
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
