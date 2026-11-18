import type { UseQueryResult } from '@tanstack/react-query'
import type { ReactNode } from 'react'

type Props<T> = {
  query: UseQueryResult<T>
  isEmpty: (data: T) => boolean
  emptyText: string
  children: (data: T) => ReactNode
}

// Renders the loading, error, empty or data state of one query.
// `children` only runs once data exists, so callers never handle undefined.
export default function AsyncView<T>({ query, isEmpty, emptyText, children }: Props<T>) {
  if (query.isPending) {
    return <p className="py-6 text-center text-sm text-slate-500">Loading…</p>
  }
  if (query.isError) {
    return (
      <div className="flex items-center justify-between gap-4 rounded border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
        <span>{query.error.message}</span>
        <button className="font-medium underline" onClick={() => query.refetch()}>
          Retry
        </button>
      </div>
    )
  }
  if (isEmpty(query.data)) {
    return <p className="py-6 text-center text-sm text-slate-500">{emptyText}</p>
  }
  return children(query.data)
}
