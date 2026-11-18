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

// Two guards against duplicate transactions:
// 1. `inFlight` ignores clicks while a request is out. It is a ref, not state,
//    so a second click before React re-renders is still blocked.
// 2. The Idempotency-Key covers what the lock can't: a retry after a network
//    error or timeout reuses the key, so the server returns the first
//    transaction instead of creating another.
//
// Key lifecycle: one key per request body. Pressing checkout again with the
// same body (a retry) reuses the key. Any change to the body (cart, outlet or
// cashier name) or a successful sale starts a new key, so the server never sees
// one key with two different bodies.
export function useCheckout() {
  const queryClient = useQueryClient()
  const [state, setState] = useState<CheckoutState>({ status: 'idle' })
  const inFlight = useRef(false)
  const attempt = useRef<{ body: string; key: string } | null>(null)

  // Resolves to the final state, or null when ignored because a request is in flight.
  async function submit(cart: CartLine[], outletId: number, cashierName: string): Promise<CheckoutState | null> {
    if (inFlight.current) return null
    inFlight.current = true

    const req: CheckoutRequest = {
      outlet_id: outletId,
      cashier_name: cashierName,
      items: cart.map((l) => ({ product_id: l.product.id, qty: l.qty })),
    }
    const body = JSON.stringify(req)
    if (attempt.current?.body !== body) {
      attempt.current = { body, key: crypto.randomUUID() }
    }

    setState({ status: 'sending' })
    let result: CheckoutState
    try {
      const transaction = await checkout(req, attempt.current.key)
      attempt.current = null
      result = { status: 'success', transaction }
      // Stock shown in search results is now out of date.
      queryClient.invalidateQueries({ queryKey: ['products'] })
    } catch (err) {
      result = toFailure(err)
    } finally {
      inFlight.current = false
    }
    setState(result)
    return result
  }

  return { state, submit, reset: () => setState({ status: 'idle' }) }
}

function toFailure(err: unknown): CheckoutState {
  if (err instanceof ApiError) {
    if (err.status === 409 && hasShortItems(err.body)) {
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
