import { useQuery } from '@tanstack/react-query'
import { getDailySales, type ReportFilter } from '../api'
import { formatDay, formatNumber, formatRupiah } from '../format'
import AsyncView from '../ui/AsyncView'

export default function DailySales({ filter }: { filter: ReportFilter }) {
  const report = useQuery({
    queryKey: ['daily-sales', filter],
    queryFn: ({ signal }) => getDailySales(filter, signal),
  })

  return (
    <section className="flex flex-col gap-3 rounded-lg bg-white p-4 shadow-sm">
      <h2 className="font-semibold">Daily sales</h2>
      <AsyncView query={report} isEmpty={(rows) => rows.length === 0} emptyText="No paid transactions in this range.">
        {(rows) => {
          const totalRevenue = rows.reduce((s, r) => s + r.revenue, 0)
          const totalTx = rows.reduce((s, r) => s + r.transactions, 0)
          return (
            <div className="max-h-[32rem] overflow-auto">
              <table className="w-full text-sm">
                <thead className="sticky top-0 bg-white text-left text-slate-500">
                  <tr>
                    <th className="py-2 font-medium">Date</th>
                    <th className="py-2 text-right font-medium">Transactions</th>
                    <th className="py-2 text-right font-medium">Revenue</th>
                    <th className="py-2 text-right font-medium">Average</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 tabular-nums">
                  {rows.map((r) => (
                    <tr key={r.date}>
                      <td className="py-1.5">{formatDay(r.date)}</td>
                      <td className="py-1.5 text-right">{formatNumber(r.transactions)}</td>
                      <td className="py-1.5 text-right">{formatRupiah(r.revenue)}</td>
                      <td className="py-1.5 text-right">{formatRupiah(r.average)}</td>
                    </tr>
                  ))}
                </tbody>
                <tfoot className="sticky bottom-0 border-t border-slate-200 bg-white font-semibold tabular-nums">
                  <tr>
                    <td className="py-2">Total</td>
                    <td className="py-2 text-right">{formatNumber(totalTx)}</td>
                    <td className="py-2 text-right">{formatRupiah(totalRevenue)}</td>
                    <td className="py-2 text-right">{formatRupiah(Math.round(totalRevenue / totalTx))}</td>
                  </tr>
                </tfoot>
              </table>
            </div>
          )
        }}
      </AsyncView>
    </section>
  )
}
