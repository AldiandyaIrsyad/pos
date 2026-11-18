-- Indexes for the reports. Reasons and EXPLAIN output: QUERY_NOTES.md.
-- Apply to an existing DB with:
-- docker exec -i pos-db psql -U postgres -v ON_ERROR_STOP=1 < migrations/004_indexes2.sql
\c pos

CREATE INDEX IF NOT EXISTS transactions_paid_outlet_created_idx
  ON transactions (outlet_id, created_at) INCLUDE (id, total)
  WHERE status = 'PAID';

CREATE INDEX IF NOT EXISTS transaction_items_transaction_idx
  ON transaction_items (transaction_id) INCLUDE (product_id, qty, subtotal);


VACUUM ANALYZE transactions;
VACUUM ANALYZE transaction_items;
