import type { ShortItem } from '../api'
import { formatRupiah } from '../format'
import { cartTotal, type CartAction, type CartLine } from './cartReducer'

type Props = {
  cart: CartLine[]
  shortItems: ShortItem[]
  dispatch: (action: CartAction) => void
}

export default function Cart({ cart, shortItems, dispatch }: Props) {
  if (cart.length === 0) {
    return <p className="py-6 text-center text-sm text-slate-500">Cart is empty.</p>
  }

  const shortById = new Map(shortItems.map((s) => [s.product_id, s]))

  return (
    <div className="flex flex-col gap-3">
      <ul className="divide-y divide-slate-100">
        {cart.map((line) => {
          // A row stays marked only while its qty is still above what's available,
          // so lowering the qty clears the mark without extra state.
          const short = shortById.get(line.product.id)
          const isShort = short !== undefined && line.qty > short.available
          return (
            <li
              key={line.product.id}
              className={`flex items-center gap-2 py-2 ${isShort ? '-mx-2 rounded bg-red-50 px-2' : ''}`}
            >
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium">{line.product.name}</p>
                <p className="text-xs text-slate-500">{formatRupiah(line.product.price)}</p>
                {isShort && (
                  <p className="text-xs font-medium text-red-700">
                    Not enough stock: only {short.available} left
                  </p>
                )}
              </div>
              <input
                type="number"
                min={0}
                className={`w-16 rounded border px-2 py-1 text-right ${isShort ? 'border-red-400' : 'border-slate-300'}`}
                value={line.qty}
                onChange={(e) => {
                  // An emptied field is a cashier about to type a new number, not a removal;
                  // removing is the ✕ button's job.
                  if (e.target.value === '') return
                  dispatch({ type: 'setQty', productId: line.product.id, qty: Math.floor(Number(e.target.value)) })
                }}
                aria-label={`Quantity of ${line.product.name}`}
              />
              <span className="w-28 text-right text-sm tabular-nums">
                {formatRupiah(line.product.price * line.qty)}
              </span>
              <button
                className="px-1 text-slate-400 hover:text-red-600"
                onClick={() => dispatch({ type: 'remove', productId: line.product.id })}
                aria-label={`Remove ${line.product.name}`}
              >
                ✕
              </button>
            </li>
          )
        })}
      </ul>
      <div className="flex justify-between border-t border-slate-200 pt-3 font-semibold">
        <span>Total</span>
        <span className="tabular-nums">{formatRupiah(cartTotal(cart))}</span>
      </div>
    </div>
  )
}
