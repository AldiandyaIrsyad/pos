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

Hal ini dapat dilihat dengan `EXPLAIN (ANALYZE, BUFFERS)`. Semua angka di bawah berasal dari satu kali jalan:

```
go run ./benchmark/topproducts
```

Tool ini mengukur tiga kecepatan, masing-masing di salinan baru database `pos` (pos sendiri tidak diubah):

1. query legacy, tanpa index dari `004_indexes2.sql`
2. query baru, tanpa index tersebut
3. query baru, dengan `004_indexes2.sql`

Hasil lengkap (ringkasan + ketiga plan): `benchmark\bench_top_products_20261119-135307.txt`

```
->  Nested Loop  (cost=0.42..23571.61 rows=1 width=4) (actual time=7.965..28.423 rows=5 loops=5000)
      Buffers: shared hit=14589414 read=33283706
      ->  Seq Scan on transaction_items ti  (cost=0.00..21839.50 rows=211 width=12) (actual time=0.225..27.748 rows=210 loops=5000)
            Filter: (product_id = p.id)
            Rows Removed by Filter: 1048070
            Buffers: shared hit=10400000 read=33280000
      ->  Index Scan using transactions_pkey on transactions t  (cost=0.42..8.21 rows=1 width=8) (actual time=0.003..0.003 rows=0 loops=1048280)
            Index Cond: (id = ti.transaction_id)
            Filter: ((outlet_id = 1) AND (status = 'PAID'::text) AND (date(created_at) >= '2026-01-01'::date) AND (date(created_at) <= '2026-03-31'::date))
            Rows Removed by Filter: 1
            Buffers: shared hit=4189414 read=3706
```

Disini dapat dilihat terdapat nested loop. Loop pertama berjalan 5000x dan loop kedua berjalan `1048280` (seq scan, karena jumlah transaction_items ada `1048280`) dalam kata lain iterasi kurang lebih sekitar 5000\*1048280 atau ~5.2 billions. `Buffers` menunjukkan biayanya: seq scan itu menyentuh ~43,7 juta page (8.736 page × 5000 kali), dan seluruh query ~48,1 juta page

Benchmark (3 run):

> avg: 144.2s

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

Benchmark tanpa index (10 run):

> avg: 39.00ms (13.796 page)

Namun masih ada optimisasi yaitu pembuatan index..

Hal ini tidak menggunakan perubahan query namun menggunakan `migrations\004_indexes2.sql` untuk pembuatan index

Benchmark dengan index (10 run):

> avg: 15.24ms (24.118 page)

| | avg | p95 | page (hit+read) | vs legacy |
|---|---|---|---|---|
| 1. legacy, tanpa index | 144.2s | 144.9s | 48.144.110 | 1x |
| 2. query baru, tanpa index | 39.00ms | 41.85ms | 13.796 | 3.697x |
| 3. query baru + `004_indexes2.sql` | 15.24ms | 17.11ms | 24.118 | 9.462x |

Catatan: dengan index, jumlah page justru naik (13.796 → 24.118), tapi waktu turun. Tanpa index, Postgres membaca seluruh tabel sekali (seq scan ~8.700 page `transaction_items`, ~5.000 page `transactions`) lalu hash join 1 juta baris. Dengan index, hanya 7.195 transaksi yang relevan yang dicari, lalu 7.195 lookup index-only (masing-masing ~3 page, sebagian besar page yang sama berulang, makanya `hit`). Yang hemat adalah baris yang diproses, bukan page. Page yang dibaca dari disk (`read`) juga turun: 9.994 → 4.310.

Namun beberapa caveat:

<!-- TODO: rule of thumb Filter first, join later, etc -->
