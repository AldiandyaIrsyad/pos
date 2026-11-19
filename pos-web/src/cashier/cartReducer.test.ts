import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { Product } from '../api.ts'
import { cartReducer, cartTotal, MAX_QTY_PER_ITEM, parseQty } from './cartReducer.ts'

const kopi: Product = { id: 1, sku: 'SKU-000001', name: 'Kopi Original 1', category: 'Minuman', price: 5000, stock: 10 }
const roti: Product = { id: 2, sku: 'SKU-000002', name: 'Roti Gold 2', category: 'Makanan', price: 12000, stock: 5 }

describe('parseQty', () => {
  it('accepts positive whole numbers and clamps to MAX_QTY_PER_ITEM', () => {
    assert.equal(parseQty('1'), 1)
    assert.equal(parseQty(' 05 '), 5)
    assert.equal(parseQty(String(MAX_QTY_PER_ITEM)), MAX_QTY_PER_ITEM)
    assert.equal(parseQty(String(MAX_QTY_PER_ITEM + 500)), MAX_QTY_PER_ITEM)
  })

  it('rejects empty, zero, negative, fractional, and non-numeric strings', () => {
    for (const raw of ['', '   ', '0', '-1', '1.5', '1e3', 'abc', '10px']) {
      assert.equal(parseQty(raw), null, `expected null for ${JSON.stringify(raw)}`)
    }
  })
})

describe('cartReducer', () => {
  it('adds products, increments existing lines, and caps at MAX_QTY_PER_ITEM', () => {
    let cart = cartReducer([], { type: 'add', product: kopi })
    cart = cartReducer(cart, { type: 'add', product: roti })
    cart = cartReducer(cart, { type: 'add', product: kopi })
    assert.deepEqual(
      cart.map((l) => [l.product.id, l.qty]),
      [
        [1, 2],
        [2, 1],
      ],
    )
    assert.equal(cartTotal(cart), 22000)

    const maxed = cartReducer([{ product: kopi, qty: MAX_QTY_PER_ITEM }], { type: 'add', product: kopi })
    assert.equal(maxed[0].qty, MAX_QTY_PER_ITEM)
  })

  it('handles setQty clamping, removal on <= 0, and ignores non-finite values', () => {
    const start = [{ product: kopi, qty: 2 }]

    assert.equal(cartReducer(start, { type: 'setQty', productId: 1, qty: Number.NaN }), start)
    assert.equal(cartReducer(start, { type: 'setQty', productId: 1, qty: Number.POSITIVE_INFINITY }), start)

    const clamped = cartReducer(start, { type: 'setQty', productId: 1, qty: MAX_QTY_PER_ITEM + 1 })
    assert.equal(clamped[0].qty, MAX_QTY_PER_ITEM)

    const floored = cartReducer(start, { type: 'setQty', productId: 1, qty: 3.9 })
    assert.equal(floored[0].qty, 3)

    assert.deepEqual(cartReducer(start, { type: 'setQty', productId: 1, qty: 0 }), [])
    assert.deepEqual(cartReducer(start, { type: 'setQty', productId: 1, qty: -5 }), [])
  })

  it('removes a line and clears the cart', () => {
    const start = [
      { product: kopi, qty: 2 },
      { product: roti, qty: 1 },
    ]
    const afterRemove = cartReducer(start, { type: 'remove', productId: 1 })
    assert.deepEqual(
      afterRemove.map((l) => l.product.id),
      [2],
    )
    assert.deepEqual(cartReducer(start, { type: 'clear' }), [])
  })
})
