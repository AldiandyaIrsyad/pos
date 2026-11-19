import { keepPreviousData, useInfiniteQuery } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { searchProducts, type Product } from '../api'
import { formatNumber, formatRupiah } from '../format'
import AsyncView from '../ui/AsyncView'

type Props = { outletId: number; onAdd: (product: Product) => void; disabled?: boolean }

export default function ProductSearch({ outletId, onAdd, disabled = false }: Props) {
  const [text, setText] = useState('')
  const q = useDebounced(text.trim(), 250)

  // Each (outlet, q) pair is its own cache entry, so a slow response for an
  // older q can never show up as the result for the current one. When the key
  // changes, TanStack aborts the previous request through `signal`.
  const products = useInfiniteQuery({
    queryKey: ['products', outletId, q],
    queryFn: ({ pageParam, signal }) => searchProducts(outletId, q, pageParam, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
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
          isEmpty={(data) => data.pages.every((page) => page.items.length === 0)}
          emptyText={`No products match “${q}”.`}
        >
          {(data) => {
            const items = data.pages.flatMap((page) => page.items)
            return (
              <div className="flex flex-col gap-3">
                <ul className={`divide-y divide-slate-100 ${products.isPlaceholderData ? 'opacity-60' : ''}`}>
                  {items.map((p) => (
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
                        disabled={disabled || p.stock === 0}
                      >
                        {p.stock === 0 ? 'Out' : 'Add'}
                      </button>
                    </li>
                  ))}
                </ul>
                {products.isFetchNextPageError && (
                  <p className="text-center text-xs text-red-700">Failed to load more products.</p>
                )}
                {products.hasNextPage && !products.isPlaceholderData && (
                  <button
                    className="rounded border border-slate-300 py-1.5 text-sm font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-50"
                    onClick={() => products.fetchNextPage()}
                    disabled={products.isFetchingNextPage}
                  >
                    {products.isFetchingNextPage ? 'Loading more…' : 'Load more'}
                  </button>
                )}
              </div>
            )
          }}
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
