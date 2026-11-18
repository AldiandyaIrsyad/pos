-- Indexes for product search.
-- read `QUERY_NOTES.md`.

-- Apply to an existing DB with:
-- docker exec -i pos-db psql -U postgres -v ON_ERROR_STOP=1 < migrations/003_indexes.sql
\c pos

-- Product search: ILIKE '%q%' on name or SKU. A B-tree can't serve a leading wildcard;
-- a trigram GIN index can (for q of 3+ characters).
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS products_name_trgm_idx ON products USING gin (name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS products_sku_trgm_idx  ON products USING gin (sku gin_trgm_ops);


VACUUM ANALYZE products;
