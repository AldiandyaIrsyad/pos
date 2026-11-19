import { useQueryClient } from '@tanstack/react-query'
import { useRef, useState } from 'react'
import { ApiError, checkout, type CheckoutRequest, type ShortItem, type Transaction } from '../api'
import type { CartLine } from './cartReducer'

export type CheckoutState =
  | { status: 'idle' }
  | { status: 'sending' }
  | { status: 'success'; transaction: Transaction }
  | { status: 'short-stock'; items: ShortItem[] }
  | { status: 'error'; message: string }

export type CheckoutAttempt = { body: string; key: string }

export function generateUUID(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  // Fallback for non-secure HTTP origins (e.g. LAN IPs) where randomUUID is unavailable.
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

// Reuses the existing attempt key only when retrying the exact same payload;
// otherwise mints a new UUID so one key is never sent with two bodies.
export function resolveAttempt(
  current: CheckoutAttempt | null,
  body: string,
  newKey: () => string = generateUUID,
): CheckoutAttempt {
  if (current?.body === body) return current
  return { body, key: newKey() }
}

// Keep the idempotency key after ambiguous failures (network errors, timeouts, 5xx)
// so pressing Checkout again safely replays or retries. Discard it on definitive
// 4xx rejections so the next attempt starts fresh.
export function shouldKeepAttempt(err: unknown): boolean {
  return !(err instanceof ApiError) || err.status >= 500
}

// Two guards against duplicate transactions:
// 1. `inFlight` ignores clicks while a request is out. It is a ref, not state,
//    so a second click before React re-renders is still blocked.
// 2. The Idempotency-Key covers what the lock can't: a retry after a network
//    error, timeout, or 503 reuses the key, so the server returns the first
//    transaction instead of creating another.
export function useCheckout() {
  const queryClient = useQueryClient()
  const [state, setState] = useState<CheckoutState>({ status: 'idle' })
  const inFlight = useRef(false)
  const attempt = useRef<CheckoutAttempt | null>(null)

  // Resolves to the final state, or null when ignored because a request is in flight.
  async function submit(cart: CartLine[], outletId: number, cashierName: string): Promise<CheckoutState | null> {
    if (inFlight.current) return null
    inFlight.current = true

    let result: CheckoutState
    try {
      const req: CheckoutRequest = {
        outlet_id: outletId,
        cashier_name: cashierName,
        items: cart.map((l) => ({ product_id: l.product.id, qty: l.qty })),
      }
      const current = resolveAttempt(attempt.current, JSON.stringify(req))
      attempt.current = current

      setState({ status: 'sending' })
      const transaction = await checkout(req, current.key)
      attempt.current = null
      result = { status: 'success', transaction }
      // Refresh stock shown in search results and any cached report queries.
      queryClient.invalidateQueries({ queryKey: ['products'] })
      queryClient.invalidateQueries({ queryKey: ['daily-sales'] })
      queryClient.invalidateQueries({ queryKey: ['top-products'] })
    } catch (err) {
      if (!shouldKeepAttempt(err)) {
        attempt.current = null
      }
      result = toFailure(err)
      if (result.status === 'short-stock') {
        // Stock changed behind our back; refresh visible stock counts immediately.
        queryClient.invalidateQueries({ queryKey: ['products'] })
      }
    } finally {
      inFlight.current = false
    }
    setState(result)
    return result
  }

  function clearAttempt() {
    attempt.current = null
  }

  function reset() {
    attempt.current = null
    setState({ status: 'idle' })
  }

  return { state, submit, clearAttempt, reset }
}

export function toFailure(err: unknown): CheckoutState {
  if (err instanceof ApiError) {
    if (err.status === 409 && hasShortItems(err.body) && err.body.short_items.length > 0) {
      return { status: 'short-stock', items: err.body.short_items }
    }
    return { status: 'error', message: err.message }
  }
  // Network failure or timeout: the server may or may not have saved the sale.
  // The key is kept, so pressing Checkout again cannot create a second one.
  return { status: 'error', message: 'No response from the server. Press Checkout again to retry safely.' }
}

function hasShortItems(body: unknown): body is { short_items: ShortItem[] } {
  return typeof body === 'object' && body !== null && 'short_items' in body && Array.isArray(body.short_items)
}
