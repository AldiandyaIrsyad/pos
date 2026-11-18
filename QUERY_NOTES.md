A/N: Biasanya pakai bahasa inggris untuk analisis dan lain lain. Namun, dikarenakan `SOAL.md` menggunakan bahasa Indonesia. Saya akan menggunakan Bahasa Indonesia.

```sql
  SELECT p.id, p.name,
    (SELECT SUM(ti.qty) FROM transaction_items ti
      JOIN transactions t ON t.id = ti.transaction_id
      WHERE ti.product_id = p.id
        AND t.outlet_id = 1
        AND DATE(t.created_at) BETWEEN '2026-01-01' AND '2026-03-31'
        AND t.status = 'PAID') AS qty_sold,
    (SELECT SUM(ti.subtotal) FROM transaction_items ti
      JOIN transactions t ON t.id = ti.transaction_id
      WHERE ti.product_id = p.id
        AND t.outlet_id = 1
        AND DATE(t.created_at) BETWEEN '2026-01-01' AND '2026-03-31'
        AND t.status = 'PAID') AS revenue
  FROM products p
  ORDER BY qty_sold DESC NULLS LAST
  LIMIT 10;
```

Yang pertama dilihat adalah beberapa `SELECT`. Namun, `SELECT` dalam menggunakan `WHERE ti.product_id = p.id` . Hal ini mengikat inner select dengan outer select. Yang akan membuat loop.

Hal ini dapat diliat menggunakan query

`benchmark\explain\main.go`

```sql
EXPLAIN (ANALYZE) + ...
```

Hasilnya ada di `benchmark\explain_legacy_261118132437.txt`

```
->  Nested Loop  (cost=0.42..17650.92 rows=1 width=4) (actual time=8.199..33.444 rows=5 loops=5000)
      ->  Seq Scan on transaction_items ti  (cost=0.00..16593.30 rows=127 width=12) (actual time=0.148..32.667 rows=210 loops=5000)
            Filter: (product_id = p.id)
            Rows Removed by Filter: 1048068
      ->  Index Scan using transactions_pkey on transactions t  (cost=0.42..8.33 rows=1 width=8) (actual time=0.003..0.003 rows=0 loops=1048278)
            Index Cond: (id = ti.transaction_id)
            Filter: ((outlet_id = 1) AND (status = 'PAID'::text) AND (date(created_at) >= '2026-01-01'::date) AND (date(created_at) <= '2026-03-31'::date))
            Rows Removed by Filter: 1
```

Disini dapat dilihat terdapat nested loop. Loop pertama berjalan 5000x dan loop kedua berjalan `1048278` (seq scan, karena jumlah transaction_items ada `1048278`) dalam kata lain iterasi kurang lebih sekitar 5000\*1048278 atau ~5.2 billions

Menjalankan benchmark

`benchmark\bench_legacy_261118205347.txt`

> AVG: 2m21.68202655s

```SQL
SELECT p.id, p.name, s.qty_sold, s.revenue
FROM (
  SELECT ti.product_id,
         SUM(ti.qty)      AS qty_sold,
         SUM(ti.subtotal) AS revenue
  FROM transactions t
  JOIN transaction_items ti ON ti.transaction_id = t.id
  WHERE t.outlet_id = 1
    AND t.status = 'PAID'
    AND t.created_at >= '2026-01-01'
    AND t.created_at <  '2026-04-01'
  GROUP BY ti.product_id
  ORDER BY qty_sold DESC, ti.product_id
  LIMIT 10
) s
JOIN products p ON p.id = s.product_id
ORDER BY s.qty_sold DESC, s.product_id;
```

berikut adalah proposed query.

pertama query SELECT inner (yang didalam) tidak menggunakan yang dari outer SELECT.
Kedua kita mengubah pengecheckan `created_at` tanpa menggunakan `DATE()` dikerenakan created_at adalah sebuah index, wrapping dengan fungsi (`LOWER()`, `DATE()`, etc) membuatnya tidak behave seperti index

`benchmark\bench_proposed_261118202719.txt`

> avg: 53.29678ms

Namun masih ada optimisasi yaitu pembuatan index..

Hal ini tidak menggunakan perubahan query namun menggunakan `migrations\004_indexes2.sql` untuk pembuatan index

`benchmark\bench_proposed_261118210002.txt`

> avg: 17.23519ms

Namun beberapa caveat:

<!-- TODO: rule of thumb Filter first, join later, etc -->
