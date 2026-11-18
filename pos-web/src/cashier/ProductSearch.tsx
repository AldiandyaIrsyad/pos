import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { searchProducts, type Product } from '../api'
import { formatNumber, formatRupiah } from '../format'
import AsyncView from '../ui/AsyncView'

type Props = { outletId: number; onAdd: (product: Product) => void }

export default function ProductSearch({ outletId, onAdd }: Props) {
  const [text, setText] = useState('')
  const q = useDebounced(text.trim(), 250)

  // Each (outlet, q) pair is its own cache entry, so a slow response for an
  // older q can never show up as the result for the current one. When the key
  // changes, TanStack aborts the previous request through `signal`.
  const products = useQuery({
    queryKey: ['products', outletId, q],
    queryFn: ({ signal }) => searchProducts(outletId, q, signal),
    enabled: q !== '',
    placeholderData: keepPreviousData, // keep old rows on screen while typing
  })

  return (
    <section className="flex flex-col gap-3 rounded-lg bg-white p-4 shadow-sm">
      <input
        className="rounded border border-slate-300 px-3 py-2"
        placeholder="Search by name or SKU…"
        value={text}
        onChange={(e) => setText(e.target.value)}
        autoFocus
      />
      {q === '' ? (
        <p className="py-6 text-center text-sm text-slate-500">Type to search products.</p>
      ) : (
        <AsyncView
          query={products}
          isEmpty={(page) => page.items.length === 0}
          emptyText={`No products match “${q}”.`}
        >
          {(page) => (
            <ul className={`divide-y divide-slate-100 ${products.isPlaceholderData ? 'opacity-60' : ''}`}>
              {page.items.map((p) => (
                <li key={p.id} className="flex items-center gap-3 py-2">
                  <div className="min-w-0 flex-1">
                    <p className="truncate font-medium">{p.name}</p>
                    <p className="text-xs text-slate-500">
                      {p.sku} · stock {formatNumber(p.stock)}
                    </p>
                  </div>
                  <span className="text-sm tabular-nums">{formatRupiah(p.price)}</span>
                  <button
                    className="rounded bg-slate-900 px-3 py-1 text-sm text-white disabled:bg-slate-300"
                    onClick={() => onAdd(p)}
                    disabled={p.stock === 0}
                  >
                    {p.stock === 0 ? 'Out' : 'Add'}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </AsyncView>
      )}
    </section>
  )
}

function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const id = setTimeout(() => setDebounced(value), ms)
    return () => clearTimeout(id)
  }, [value, ms])
  return debounced
}
