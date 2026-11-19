import { useQuery } from '@tanstack/react-query'
import { getOutlets } from '../api'

type Props = { value: number; onChange: (outletId: number) => void; disabled?: boolean }

export default function OutletSelect({ value, onChange, disabled = false }: Props) {
  const outlets = useQuery({
    queryKey: ['outlets'],
    queryFn: ({ signal }) => getOutlets(signal),
    staleTime: Infinity, // outlets only change through the seed
  })

  return (
    <label className="flex flex-col gap-1 text-sm">
      <span className="font-medium text-slate-600">Outlet</span>
      <select
        className="rounded border border-slate-300 bg-white px-2 py-1.5 disabled:bg-slate-100 disabled:text-slate-500"
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        disabled={disabled || !outlets.isSuccess}
      >
        {outlets.isSuccess ? (
          outlets.data.map((o) => (
            <option key={o.id} value={o.id}>
              {o.code} · {o.name}
            </option>
          ))
        ) : (
          <option value={value}>{outlets.isError ? 'Failed to load outlets' : 'Loading…'}</option>
        )}
      </select>
    </label>
  )
}
