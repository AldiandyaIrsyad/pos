-- Checkout safety: stock backstop + idempotency keys.
-- Read `QUERY_NOTES.md`

-- Apply to an existing DB with:
-- docker exec -i pos-db psql -U postgres -v ON_ERROR_STOP=1 < migrations/002_checkout.sql
\c pos

-- Database-level check that stock never goes below 0
-- This makes sure that even another code (maybe in the future), even done incorrectly still would not make negative value.
-- Code still have the `UPDATE ... WHERE qty >= n.` to make sure waiting request would see the updated one before comitting.

-- Drop and re-add
ALTER TABLE stocks DROP CONSTRAINT IF EXISTS stocks_qty_non_negative;
ALTER TABLE stocks ADD CONSTRAINT stocks_qty_non_negative CHECK (qty >= 0);

ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_status_valid;
ALTER TABLE transactions ADD CONSTRAINT transactions_status_valid CHECK (status IN ('PAID', 'VOID'));

-- Idempotency
CREATE TABLE IF NOT EXISTS idempotency_keys (
  key            UUID        PRIMARY KEY,
  request_hash   TEXT        NOT NULL,
  transaction_id BIGINT      REFERENCES transactions(id),
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
