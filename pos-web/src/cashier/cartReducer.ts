// The cart's rules as a pure reducer: no React, no fetch.
import type { Product } from '../api.ts'

export const MAX_QTY_PER_ITEM = 10_000

export type CartLine = { product: Product; qty: number }

export type CartAction =
  | { type: 'add'; product: Product }
  | { type: 'setQty'; productId: number; qty: number }
  | { type: 'remove'; productId: number }
  | { type: 'clear' }

// Parses a positive whole-number quantity string, clamping to the API limit.
// Returns null for empty, zero, negative, fractional, or non-numeric input.
export function parseQty(raw: string): number | null {
  const trimmed = raw.trim()
  if (!/^\d+$/.test(trimmed)) return null
  const n = Number(trimmed)
  if (!Number.isSafeInteger(n) || n < 1) return null
  return Math.min(n, MAX_QTY_PER_ITEM)
}

export function cartReducer(cart: CartLine[], action: CartAction): CartLine[] {
  switch (action.type) {
    case 'add': {
      const existing = cart.find((l) => l.product.id === action.product.id)
      if (existing) {
        return cart.map((l) =>
          l === existing ? { ...l, qty: Math.min(l.qty + 1, MAX_QTY_PER_ITEM) } : l,
        )
      }
      return [...cart, { product: action.product, qty: 1 }]
    }
    case 'setQty': {
      if (!Number.isFinite(action.qty)) return cart
      const qty = Math.floor(action.qty)
      if (qty <= 0) {
        return cart.filter((l) => l.product.id !== action.productId)
      }
      const clamped = Math.min(qty, MAX_QTY_PER_ITEM)
      return cart.map((l) => (l.product.id === action.productId ? { ...l, qty: clamped } : l))
    }
    case 'remove':
      return cart.filter((l) => l.product.id !== action.productId)
    case 'clear':
      return []
    default: {
      const unhandled: never = action
      throw new Error(`Unhandled cart action: ${JSON.stringify(unhandled)}`)
    }
  }
}

export const cartTotal = (cart: CartLine[]) =>
  cart.reduce((sum, l) => sum + l.product.price * l.qty, 0)
