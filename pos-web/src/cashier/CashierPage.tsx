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

  function updateCart(action: CartAction) {
    // The cart being sent must not change until the server answers.
    if (sending) return
    dispatch(action)
    // A new cart after a sale: the old receipt no longer applies.
    if (checkout.state.status === 'success') checkout.reset()
  }

  function changeOutlet(id: number) {
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
          <OutletSelect value={outletId} onChange={changeOutlet} />
          <label className="flex flex-col gap-1 text-sm">
            <span className="font-medium text-slate-600">Cashier</span>
            <input
              className="rounded border border-slate-300 bg-white px-2 py-1.5"
              placeholder="Name"
              value={cashierName}
              onChange={(e) => setCashierName(e.target.value)}
            />
          </label>
        </div>
        <ProductSearch outletId={outletId} onAdd={(product: Product) => updateCart({ type: 'add', product })} />
      </div>

      <section className="flex h-fit flex-col gap-4 rounded-lg bg-white p-4 shadow-sm">
        <h2 className="font-semibold">Cart</h2>
        <Cart
          cart={cart}
          shortItems={checkout.state.status === 'short-stock' ? checkout.state.items : []}
          dispatch={updateCart}
        />
        <CheckoutStatus state={checkout.state} />
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

function CheckoutStatus({ state }: { state: CheckoutState }) {
  switch (state.status) {
    case 'idle':
    case 'sending':
      return null
    case 'success':
      return (
        <p className="rounded border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm text-emerald-800">
          Transaction #{state.transaction.id} paid · {formatRupiah(state.transaction.total)}
        </p>
      )
    case 'short-stock':
      return (
        <p className="rounded border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
          Not enough stock for {state.items.length} item{state.items.length > 1 ? 's' : ''}. Nothing was charged.
          Adjust the marked rows and try again.
        </p>
      )
    case 'error':
      return (
        <p className="rounded border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">{state.message}</p>
      )
  }
}
