// One typed function per pos-api endpoint. The types mirror the API's JSON.
// No React here: hooks and components decide when to call these.

const BASE = '/api'

export type Outlet = { id: number; code: string; name: string }

export type Product = {
  id: number
  sku: string
  name: string
  category: string
  price: number
  stock: number // at the requested outlet
}

export type ProductPage = { items: Product[]; next_cursor: string | null }

export type CheckoutRequest = {
  outlet_id: number
  cashier_name: string
  items: { product_id: number; qty: number }[]
}

export type Transaction = {
  id: number
  outlet_id: number
  cashier_name: string
  status: 'PAID' | 'VOID'
  total: number
  created_at: string
  items: { product_id: number; qty: number; price: number; subtotal: number }[]
}

export type ShortItem = { product_id: number; requested: number; available: number }

export type DailySalesRow = {
  date: string // YYYY-MM-DD
  transactions: number
  revenue: number
  average: number
}

export type TopProductRow = {
  product_id: number
  name: string
  qty_sold: number
  revenue: number
}

export type ReportFilter = { outletId: number; from: string; to: string }

export type TopProductsSort = 'qty' | 'revenue'

// Every non-2xx response. `body` keeps the parsed JSON so callers can read
// endpoint-specific fields (e.g. short_items on a 409).
export class ApiError extends Error {
  readonly status: number
  readonly body: unknown

  constructor(status: number, message: string, body: unknown) {
    super(message)
    this.status = status
    this.body = body
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(BASE + path, init)
  const body: unknown = await res.json().catch(() => null)
  if (!res.ok) {
    const message =
      body && typeof body === 'object' && 'error' in body && typeof body.error === 'string'
        ? body.error
        : `Request failed (${res.status})`
    throw new ApiError(res.status, message, body)
  }
  return body as T
}

export function getOutlets(signal?: AbortSignal) {
  return request<Outlet[]>('/outlets', { signal })
}

export function searchProducts(outletId: number, q: string, cursor?: string, signal?: AbortSignal) {
  const params = new URLSearchParams({ outlet_id: String(outletId), q, limit: '20' })
  if (cursor) params.set('cursor', cursor)
  return request<ProductPage>(`/products?${params}`, { signal })
}

// Same key + same body returns the original transaction (200) instead of a new one.
// The timeout turns a hung request into an error the cashier can retry with the same key.
export function checkout(req: CheckoutRequest, idempotencyKey: string) {
  return request<Transaction>('/checkout', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(req),
    signal: AbortSignal.timeout(15_000),
  })
}

function reportParams(f: ReportFilter) {
  return new URLSearchParams({ outlet_id: String(f.outletId), from: f.from, to: f.to })
}

export function getDailySales(f: ReportFilter, signal?: AbortSignal) {
  return request<DailySalesRow[]>(`/reports/daily-sales?${reportParams(f)}`, { signal })
}

export function getTopProducts(f: ReportFilter, sort: TopProductsSort = 'qty', signal?: AbortSignal) {
  const params = reportParams(f)
  params.set('limit', '10')
  params.set('sort', sort)
  return request<TopProductRow[]>(`/reports/top-products?${params}`, { signal })
}
