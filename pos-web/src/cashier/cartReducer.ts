// The cart's rules as a pure reducer: no React, no fetch.
import type { Product } from '../api'

export type CartLine = { product: Product; qty: number }

export type CartAction =
  | { type: 'add'; product: Product }
  | { type: 'setQty'; productId: number; qty: number }
  | { type: 'remove'; productId: number }
  | { type: 'clear' }

export function cartReducer(cart: CartLine[], action: CartAction): CartLine[] {
  switch (action.type) {
    case 'add': {
      const existing = cart.find((l) => l.product.id === action.product.id)
      if (existing) {
        return cart.map((l) => (l === existing ? { ...l, qty: l.qty + 1 } : l))
      }
      return [...cart, { product: action.product, qty: 1 }]
    }
    case 'setQty':
      if (action.qty <= 0) {
        return cart.filter((l) => l.product.id !== action.productId)
      }
      return cart.map((l) => (l.product.id === action.productId ? { ...l, qty: action.qty } : l))
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
