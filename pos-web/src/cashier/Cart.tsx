import { useState } from 'react'
import type { ShortItem } from '../api'
import { formatRupiah } from '../format'
import { cartTotal, MAX_QTY_PER_ITEM, parseQty, type CartAction, type CartLine } from './cartReducer'

type Props = {
  cart: CartLine[]
  shortItems: ShortItem[]
  dispatch: (action: CartAction) => void
  disabled?: boolean
}

export default function Cart({ cart, shortItems, dispatch, disabled = false }: Props) {
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
              <QtyInput
                qty={line.qty}
                productName={line.product.name}
                isShort={isShort}
                disabled={disabled}
                onChange={(qty) => dispatch({ type: 'setQty', productId: line.product.id, qty })}
              />
              <span className="w-28 text-right text-sm tabular-nums">
                {formatRupiah(line.product.price * line.qty)}
              </span>
              <button
                className="px-1 text-slate-400 hover:text-red-600 disabled:pointer-events-none disabled:opacity-40"
                onClick={() => dispatch({ type: 'remove', productId: line.product.id })}
                disabled={disabled}
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

function QtyInput({
  qty,
  productName,
  isShort,
  disabled,
  onChange,
}: {
  qty: number
  productName: string
  isShort: boolean
  disabled: boolean
  onChange: (qty: number) => void
}) {
  // Keep an in-progress string while typing so clearing a 1-digit value with
  // Backspace doesn't snap back; blur restores the last valid quantity.
  const [draft, setDraft] = useState<string | null>(null)
  const [prevQty, setPrevQty] = useState(qty)
  if (qty !== prevQty) {
    setPrevQty(qty)
    if (draft !== null && Number(draft) !== qty) {
      setDraft(null)
    }
  }

  return (
    <input
      type="number"
      inputMode="numeric"
      min={1}
      max={MAX_QTY_PER_ITEM}
      step={1}
      disabled={disabled}
      className={`w-16 rounded border px-2 py-1 text-right disabled:bg-slate-100 disabled:text-slate-500 ${
        isShort ? 'border-red-400' : 'border-slate-300'
      }`}
      value={draft ?? String(qty)}
      onChange={(e) => {
        const raw = e.target.value
        const parsed = parseQty(raw)
        if (parsed !== null) {
          setDraft(Number(raw) === parsed ? raw : String(parsed))
          onChange(parsed)
        } else {
          setDraft(raw)
        }
      }}
      onBlur={() => setDraft(null)}
      aria-label={`Quantity of ${productName}`}
    />
  )
}
