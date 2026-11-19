import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { ApiError } from '../api.ts'
import { generateUUID, resolveAttempt, shouldKeepAttempt, toFailure } from './useCheckout.ts'

const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

describe('generateUUID', () => {
  it('returns valid distinct v4 UUIDs', () => {
    const a = generateUUID()
    const b = generateUUID()
    assert.match(a, UUID_V4)
    assert.match(b, UUID_V4)
    assert.notEqual(a, b)
  })

  it('falls back to crypto.getRandomValues when crypto.randomUUID is unavailable', () => {
    const desc = Object.getOwnPropertyDescriptor(crypto, 'randomUUID')
    const protoDesc = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(crypto), 'randomUUID')
    Object.defineProperty(crypto, 'randomUUID', { value: undefined, configurable: true, writable: true })
    try {
      const id = generateUUID()
      assert.match(id, UUID_V4)
    } finally {
      if (desc) {
        Object.defineProperty(crypto, 'randomUUID', desc)
      } else if (protoDesc) {
        delete (crypto as unknown as Record<string, unknown>).randomUUID
      }
    }
  })
})

describe('idempotency attempt lifecycle', () => {
  it('reuses the key only when retrying the exact same payload', () => {
    let seq = 0
    const nextKey = () => `key-${++seq}`

    const first = resolveAttempt(null, '{"outlet_id":1,"items":[{"product_id":1,"qty":1}]}', nextKey)
    assert.equal(first.key, 'key-1')

    const retry = resolveAttempt(first, first.body, nextKey)
    assert.equal(retry.key, 'key-1')

    const edited = resolveAttempt(first, '{"outlet_id":1,"items":[{"product_id":1,"qty":2}]}', nextKey)
    assert.equal(edited.key, 'key-2')

    // After reset/edit clears the saved attempt, an identical cart gets a fresh key.
    const nextCustomer = resolveAttempt(null, first.body, nextKey)
    assert.equal(nextCustomer.key, 'key-3')
  })

  it('keeps attempt keys on network/5xx errors and discards them on definitive 4xx errors', () => {
    assert.equal(shouldKeepAttempt(new TypeError('Failed to fetch')), true)
    assert.equal(shouldKeepAttempt(new DOMException('Timeout', 'TimeoutError')), true)
    assert.equal(shouldKeepAttempt(new ApiError(503, 'busy', { error: 'busy' })), true)
    assert.equal(shouldKeepAttempt(new ApiError(500, 'internal', { error: 'internal' })), true)

    assert.equal(shouldKeepAttempt(new ApiError(400, 'bad request', { error: 'bad request' })), false)
    assert.equal(shouldKeepAttempt(new ApiError(404, 'not found', { error: 'not found' })), false)
    assert.equal(shouldKeepAttempt(new ApiError(409, 'short', { error: 'short', short_items: [] })), false)
    assert.equal(shouldKeepAttempt(new ApiError(422, 'key reused', { error: 'key reused' })), false)
  })
})

describe('toFailure', () => {
  it('maps 409 with short_items to short-stock, and falls back to error if empty', () => {
    const short = toFailure(
      new ApiError(409, 'insufficient stock', {
        error: 'insufficient stock',
        short_items: [{ product_id: 1, requested: 3, available: 1 }],
      }),
    )
    assert.deepEqual(short, {
      status: 'short-stock',
      items: [{ product_id: 1, requested: 3, available: 1 }],
    })

    const emptyShort = toFailure(new ApiError(409, 'insufficient stock', { error: 'insufficient stock', short_items: [] }))
    assert.deepEqual(emptyShort, { status: 'error', message: 'insufficient stock' })
  })

  it('surfaces API error messages and safe-retry guidance for network failures', () => {
    assert.deepEqual(toFailure(new ApiError(503, 'database busy', { error: 'database busy' })), {
      status: 'error',
      message: 'database busy',
    })
    const net = toFailure(new TypeError('fetch failed'))
    assert.equal(net.status, 'error')
    assert.match(net.message, /retry safely/i)
  })
})
