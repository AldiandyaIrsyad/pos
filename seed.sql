-- POS technical test seed. Deterministic (setseed), PostgreSQL 14+.
-- Intentionally minimal indexes: only primary keys. Candidates add the rest.

DROP DATABASE IF EXISTS pos;
CREATE DATABASE pos;
\c pos

SELECT setseed(0.42);

CREATE TABLE outlets (
  id   SERIAL PRIMARY KEY,
  code TEXT NOT NULL,
  name TEXT NOT NULL
);

CREATE TABLE products (
  id       SERIAL PRIMARY KEY,
  sku      TEXT NOT NULL,
  name     TEXT NOT NULL,
  category TEXT NOT NULL,
  price    BIGINT NOT NULL
);

CREATE TABLE stocks (
  outlet_id  INT NOT NULL REFERENCES outlets(id),
  product_id INT NOT NULL REFERENCES products(id),
  qty        INT NOT NULL,
  PRIMARY KEY (outlet_id, product_id)
);

CREATE TABLE transactions (
  id           BIGSERIAL PRIMARY KEY,
  outlet_id    INT NOT NULL REFERENCES outlets(id),
  cashier_name TEXT NOT NULL,
  status       TEXT NOT NULL,
  total        BIGINT NOT NULL DEFAULT 0,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE transaction_items (
  id             BIGSERIAL PRIMARY KEY,
  transaction_id BIGINT NOT NULL REFERENCES transactions(id),
  product_id     INT NOT NULL REFERENCES products(id),
  qty            INT NOT NULL,
  price          BIGINT NOT NULL,
  subtotal       BIGINT NOT NULL
);

-- 10 outlets
INSERT INTO outlets (code, name)
SELECT 'OUT-' || lpad(i::text, 3, '0'), 'Outlet ' || i
FROM generate_series(1, 10) i;

-- 5,000 products
INSERT INTO products (sku, name, category, price)
SELECT
  'SKU-' || lpad(i::text, 6, '0'),
  (ARRAY['Indomie','Aqua','Teh Botol','Kopi','Roti','Susu','Sabun','Shampo','Gula','Beras',
         'Minyak','Kecap','Saus','Biskuit','Coklat','Keripik','Rokok','Tisu','Pasta Gigi','Detergen'])[1 + (i % 20)]
    || ' ' || (ARRAY['Original','Pedas','Jumbo','Mini','Premium','Hemat','Gold','Lite'])[1 + (i % 8)]
    || ' ' || i,
  (ARRAY['Makanan','Minuman','Kebersihan','Sembako','Snack','Lainnya'])[1 + (i % 6)],
  (500 + floor(random() * 200) * 500)::bigint
FROM generate_series(1, 5000) i;

-- stock for every outlet x product (50,000 rows); a few hot items start low
INSERT INTO stocks (outlet_id, product_id, qty)
SELECT o.id, p.id,
       CASE WHEN p.id <= 20 THEN 5 ELSE 20 + floor(random() * 480)::int END
FROM outlets o CROSS JOIN products p;

-- 300,000 transactions over 2025-07-01 .. 2026-06-30; ~3% VOID
INSERT INTO transactions (outlet_id, cashier_name, status, created_at)
SELECT
  1 + floor(random() * 10)::int,
  (ARRAY['Budi','Siti','Andi','Dewi','Rina','Agus','Lina','Joko'])[1 + floor(random() * 8)::int],
  CASE WHEN random() < 0.03 THEN 'VOID' ELSE 'PAID' END,
  timestamptz '2025-07-01 07:00+07'
    + (floor(random() * 365) || ' days')::interval
    + (floor(random() * 15 * 3600) || ' seconds')::interval
FROM generate_series(1, 300000);

-- ~1,050,000 items (1..6 per transaction), skewed toward popular products
INSERT INTO transaction_items (transaction_id, product_id, qty, price, subtotal)
SELECT t.id, x.product_id, x.qty, p.price, p.price * x.qty
FROM transactions t
CROSS JOIN LATERAL (
  SELECT 1 + floor(power(random(), 2) * 5000)::int AS product_id,
         1 + floor(random() * 4)::int              AS qty
  -- "+ t.id * 0" correlates the bound so random() is re-evaluated per transaction
  FROM generate_series(1, 1 + floor(random() * 6 + t.id * 0)::int)
) x
JOIN products p ON p.id = x.product_id;

UPDATE transactions t
SET total = s.total
FROM (SELECT transaction_id, SUM(subtotal) AS total
      FROM transaction_items GROUP BY transaction_id) s
WHERE s.transaction_id = t.id;

ANALYZE;

SELECT 'outlets' AS tbl, count(*) FROM outlets
UNION ALL SELECT 'products', count(*) FROM products
UNION ALL SELECT 'stocks', count(*) FROM stocks
UNION ALL SELECT 'transactions', count(*) FROM transactions
UNION ALL SELECT 'transaction_items', count(*) FROM transaction_items;
