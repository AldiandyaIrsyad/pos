import { useReducer, useState } from 'react'
import type { Product } from '../api'
import { formatRupiah } from '../format'
import OutletSelect from '../ui/OutletSelect'
import Cart from './Cart'
import { cartReducer, type CartAction } from './cartReducer'
import ProductSearch from './ProductSearch'
import { useCheckout, type CheckoutState } from './useCheckout'

export default function CashierPage() {
  const [outletId, setOutletId] = useState(1)
  const [cashierName, setCashierName] = useState('')
  const [cart, dispatch] = useReducer(cartReducer, [])
  const checkout = useCheckout()

  const sending = checkout.state.status === 'sending'
  const canCheckout = cart.length > 0 && cashierName.trim() !== '' && !sending
  // Short items from the last 409 that the current cart still asks too much of.
  // Editing or removing a row drops it from here, so the warning follows the cart.
  const shortItems =
    checkout.state.status === 'short-stock'
      ? checkout.state.items.filter((s) => cart.some((l) => l.product.id === s.product_id && l.qty > s.available))
      : []

  function updateCart(action: CartAction) {
    // The cart being sent must not change until the server answers.
    if (sending) return
    dispatch(action)
    // Editing or clearing the cart ends any prior ambiguous attempt so a new
    // customer with an identical cart can never replay an older key.
    checkout.clearAttempt()
    // A new cart after a sale: the old receipt no longer applies.
    if (checkout.state.status === 'success') checkout.reset()
  }

  function changeOutlet(id: number) {
    if (sending) return
    // Prices are shared but stock is per outlet, so the cart doesn't carry over.
    setOutletId(id)
    dispatch({ type: 'clear' })
    checkout.reset()
  }

  async function handleCheckout() {
    const result = await checkout.submit(cart, outletId, cashierName.trim())
    // The hook only reports results; the page decides what they mean for the cart.
    if (result?.status === 'success') dispatch({ type: 'clear' })
  }

  return (
    <div className="grid gap-6 lg:grid-cols-[1fr_420px]">
      <div className="flex flex-col gap-4">
        <div className="flex flex-wrap gap-4">
          <OutletSelect value={outletId} onChange={changeOutlet} disabled={sending} />
          <label className="flex flex-col gap-1 text-sm">
            <span className="font-medium text-slate-600">Cashier</span>
            <input
              className="rounded border border-slate-300 bg-white px-2 py-1.5 disabled:bg-slate-100 disabled:text-slate-500"
              placeholder="Name"
              value={cashierName}
              disabled={sending}
              onChange={(e) => {
                setCashierName(e.target.value)
                checkout.clearAttempt()
              }}
            />
          </label>
        </div>
        <ProductSearch
          outletId={outletId}
          disabled={sending}
          onAdd={(product: Product) => updateCart({ type: 'add', product })}
        />
      </div>

      <section className="flex h-fit flex-col gap-4 rounded-lg bg-white p-4 shadow-sm">
        <h2 className="font-semibold">Cart</h2>
        <Cart
          cart={cart}
          shortItems={shortItems}
          dispatch={updateCart}
          disabled={sending}
        />
        <CheckoutStatus state={checkout.state} shortCount={shortItems.length} />
        <button
          className="rounded bg-emerald-600 py-2.5 font-semibold text-white disabled:bg-slate-300"
          onClick={handleCheckout}
          disabled={!canCheckout}
        >
          {sending ? 'Processing…' : 'Checkout'}
        </button>
      </section>
    </div>
  )
}

function CheckoutStatus({ state, shortCount }: { state: CheckoutState; shortCount: number }) {
  switch (state.status) {
    case 'idle':
    case 'sending':
      return null
    case 'success':
      return (
        <p className="rounded border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm text-emerald-800">
          Transaction #{state.transaction.id} {state.transaction.status.toLowerCase()} ·{' '}
          {formatRupiah(state.transaction.total)}
        </p>
      )
    case 'short-stock':
      if (shortCount === 0) return null
      return (
        <p className="rounded border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
          Not enough stock for {shortCount} item{shortCount > 1 ? 's' : ''}. Nothing was charged.
          Adjust the marked rows and try again.
        </p>
      )
    case 'error':
      return (
        <p className="rounded border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">{state.message}</p>
      )
  }
}
