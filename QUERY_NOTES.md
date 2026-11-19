# Analisis Query dan Index

## Cara mengukur

Semua angka top products di bawah berasal dari satu kali jalan:

```bash
go run ./benchmark/topproducts
```

Tool ini mengukur tiga variant. Masing-masing jalan di salinan baru database `pos`, jadi `pos` sendiri tidak berubah:

1. query legacy, tanpa index dari `004_indexes2.sql`
2. query baru, tanpa index tersebut
3. query baru, dengan `004_indexes2.sql`

Tiap variant menjalankan satu `EXPLAIN (ANALYZE, BUFFERS)` saat cache masih dingin (`cold`), beberapa run terukur saat cache sudah hangat (`warm`), lalu satu `EXPLAIN (ANALYZE, BUFFERS)` lagi saat cache hangat agar angka buffer sesuai dengan kondisi pengukuran. Hasil lengkap (ringkasan + ketiga plan): `benchmark/bench_top_products_20261120-035021.txt`.

`Buffers` = jumlah page (8 KB) yang disentuh. `hit` = dari cache Postgres, `read` = dari OS/disk.

## Legacy Query

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

Berdasarkan plan (variant 1) kita dapat melihat berapa row yang diakses:

```
Seq Scan on products p  (actual rows=5000 loops=1)
  SubPlan 1  (qty_sold)
    ->  Nested Loop  (actual rows=5 loops=5000)
          ->  Seq Scan on transaction_items ti  (actual rows=210 loops=5000)
                Filter: (product_id = p.id)
                Rows Removed by Filter: 1048068
                Buffers: shared hit=10400000 read=33320000
          ->  Index Scan using transactions_pkey on transactions t  (loops=1048278)
                Filter: ((outlet_id = 1) AND (status = 'PAID') AND (date(created_at) >= ...) ...)
SubPlan 2  (revenue)
  ->  Nested Loop  (actual rows=111 loops=10)
Buffers: shared hit=14797051 read=33387127
Execution Time: 158267 ms
```

Subquery `qty_sold` berjalan 5.000x (sekali per produk). Tiap kali, seluruh `transaction_items` (~1 juta row) di-scan untuk mencari satu produk, sehingga ~5 milyar row diperiksa. Seq scan tersebut menyentuh ~43,7 juta page (8.744 page × 5.000 kali); seluruh query ~48,2 juta page.

Subquery `revenue` hanya berjalan 10x (`loops=10`) karena Postgres menghitungnya setelah `ORDER BY ... LIMIT 10`. Yang mahal adalah `qty_sold`, karena dipakai untuk sorting dan harus dihitung untuk semua 5.000 produk.

> 159.4s (cold read: 33.387.127 page, warm total: 48.184.178 page)

Untuk memperbaikinya:
1. Filter dahulu sebelum join. Ambil dulu transaksi outlet 1, `PAID`, di rentang tanggal (~7.195 row), baru join ke `transaction_items`.
2. Join sekali. `qty_sold` dan `revenue` dihitung dari join yang sama dengan `SUM` dalam satu `GROUP BY`.
3. Hilangkan ketergantungan pada outer loop variable (`ti.product_id = p.id`) agar tidak ada correlated subquery per produk.
4. Jangan membungkus `created_at` dengan fungsi (`DATE()`), gunakan rentang setengah terbuka `>=` dan `<` agar index bisa dipakai.
5. Ambil nama produk hanya untuk 10 hasil teratas, bukan 5.000 produk.

## Optimized Query

```sql
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

`BETWEEN '2026-01-01' AND '2026-03-31'` dengan `DATE()` diubah menjadi range setengah terbuka `>= '2026-01-01' AND < '2026-04-01'` pada kolom aslinya.

Plan tanpa index baru (variant 2, cold EXPLAIN):

```
Parallel Hash Join  (actual rows=8371 loops=3)
  ->  Parallel Seq Scan on transaction_items ti  (actual rows=349426 loops=3)
  ->  Parallel Seq Scan on transactions t  (actual rows=2398 loops=3)
        Rows Removed by Filter: 97602
Index Scan using products_pkey on products p  (loops=10)
Buffers: shared hit=4220 read=9584
Execution Time: 54.916 ms
```

Sekarang setiap tabel hanya di-scan sekali lalu di-hash join. Masih ada seq scan karena belum ada index untuk `(outlet_id, created_at)`, sehingga Postgres membaca semua 300 ribu transaksi dan 1 juta item—tapi cukup sekali, bukan 5.000x.

> avg: 49.58ms (10 run), cold read 9.584 page, warm 13.804 page (6.300 hit + 7.504 read via ring buffer). Dari 159 detik ke ~50 ms hanya dari rewrite query, tanpa biaya tambahan di write.

## Index (`migrations/004_indexes2.sql`)

```sql
CREATE INDEX transactions_paid_outlet_created_idx
  ON transactions (outlet_id, created_at) INCLUDE (total)
  WHERE status = 'PAID';

CREATE INDEX transaction_items_transaction_idx
  ON transaction_items (transaction_id) INCLUDE (product_id, qty, subtotal);
```

Alasan `transactions_paid_outlet_created_idx` (11 MB):
1. `(outlet_id, created_at)`: kolom equality (`=`) di depan, kolom range di belakang. Index langsung melompat ke outlet 1 lalu membaca rentang tanggalnya berurutan. Jika dibalik `(created_at, outlet_id)`, index harus memindai semua outlet pada rentang tersebut lalu membuang outlet lain.
2. `WHERE status = 'PAID'` (partial index): semua laporan hanya membaca `PAID`, sehingga transaksi `VOID` tidak masuk index.
3. `INCLUDE (total)` saja (bukan `INCLUDE (id, total)`): untuk `daily-sales`, `total` sudah ada di index sehingga terjadi `Index Only Scan` (`Heap Fetches: 0`). Pada `top-products`, planner memilih `Parallel Bitmap Heap Scan`—yang hanya membaca TID dari bitmap index dan tetap membuka heap—jadi menyertakan `id` di `INCLUDE` sebelumnya menambah 3 MB (14 MB → 11 MB) dan 8 page tambahan (46 → 38 page) tanpa mengurangi heap fetch.

Alasan `transaction_items_transaction_idx` (41 MB):
1. Join `ti.transaction_id = t.id` langsung mencari item dari transaksi yang sudah tersaring, bukan scan 1 juta item.
2. `INCLUDE (product_id, qty, subtotal)`: semua kolom yang dibutuhkan `top-products` ada di index, menghasilkan `Index Only Scan` (`Heap Fetches: 6`). Dibanding index biasa `(transaction_id)` tanpa `INCLUDE` (14 MB), index covering ini lebih besar 27 MB tetapi memangkas cold disk read hampir setengahnya (5.472 → 2.747 page saat diukur terisolasi) dan mengurangi total sentuhan buffer hangat ~23% (31.360 → 24.110 page) karena menghindari random heap lookup.
3. Index ini juga dipakai saat void dan replay idempotency (mengambil item untuk satu transaksi).

Plan dengan index (variant 3, cold EXPLAIN):

```
Nested Loop  (actual rows=8371 loops=3)
  ->  Parallel Bitmap Heap Scan on transactions t  (actual rows=2398 loops=3)
        Heap Blocks: exact=879
        ->  Bitmap Index Scan on transactions_paid_outlet_created_idx  (actual rows=7195 loops=1)
              Buffers: shared read=38
  ->  Index Only Scan using transaction_items_transaction_idx on transaction_items ti  (actual rows=3 loops=7195)
        Heap Fetches: 6
Buffers: shared hit=19805 read=4305
Execution Time: 20.353 ms
```

Sekarang hanya 7.195 transaksi yang relevan dibaca, lalu ~3 item per transaksi dicari lewat index.

> avg: 14.19ms (10 run, p95 15.59ms), cold read 4.305 page, warm 24.110 page (24.110 hit, 0 read).

Catatan: dengan index, jumlah sentuhan page justru naik (13.804 → 24.110), tetapi waktu turun jauh. `Buffers` menghitung berapa kali page disentuh, bukan berapa page unik yang dibaca.

| | tanpa index | dengan index |
|---|---|---|
| total sentuhan page (warm) | 13.804 | 24.110 |
| `read` saat cold EXPLAIN | 9.584 | 4.305 |
| `read` saat warm EXPLAIN | 7.504 | 0 |
| row yang diperiksa | ~1,35 juta | ~32 ribu |

1. Tanpa index, Postgres membaca semua page kedua tabel secara sekuensial (memakai ring buffer sehingga tetap ada `read` saat warm) dan memeriksa ~1,35 juta row.
2. Dengan index, tiap dari 7.195 lookup melewati ~3 page B-tree (root → internal → leaf). Root dan page internal yang sama disentuh berulang kali dari `shared_buffers`, sehingga seluruh 24.110 sentuhan saat warm adalah `hit` (0 `read`).
3. Bagian yang mahal turun drastis: cold disk read turun >55%, dan jumlah row yang diperiksa turun ~40x.

## Ringkasan top products

| | avg | p95 | cold read | warm pages | vs legacy |
|---|---|---|---|---|---|
| 1. legacy, tanpa index | 159.4s | 159.4s | 33.387.127 | 48.184.178 | 1x |
| 2. query baru, tanpa index | 49.58ms | 65.34ms | 9.584 | 13.804 | 3.215x |
| 3. query baru + `004_indexes2.sql` | 14.19ms | 15.59ms | 4.305 | 24.110 | 11.236x |

## Daily sales

```bash
go run ./benchmark/beforeafter -sql benchmark/daily_sales.sql -migration migrations/004_indexes2.sql
```

Query sama dengan API (`pos-api/internal/reports/reports.go`), outlet 1, Q1 2026 WIB. Hasil: `benchmark/bench_daily_sales_004_indexes2_20261120-033719.txt`.

Sebelum 004 (cold EXPLAIN):

```
Parallel Seq Scan on transactions  (actual rows=2398 loops=3)
  Rows Removed by Filter: 97602
Buffers: shared hit=2154 read=2920
Execution Time: 13.673 ms
```

Sesudah 004 (cold EXPLAIN):

```
Index Only Scan using transactions_paid_outlet_created_idx on transactions  (actual rows=7195 loops=1)
  Heap Fetches: 0
Buffers: shared hit=1 read=38
Execution Time: 4.081 ms
```

Sebelumnya semua 300 ribu transaksi dibaca lalu disaring. Sekarang index langsung ke outlet 1 dan rentang tanggalnya. Karena `total` ada di `INCLUDE`, tabelnya tidak dibuka sama sekali (`Heap Fetches: 0`).

> p50: 13.34ms → 4.86ms (avg 13.50ms → 6.08ms, 20 run), 5.074 → 39 page (warm: 39 hit, 0 read).

## Product search

`migrations/003_indexes.sql`:

```sql
CREATE INDEX products_name_trgm_idx ON products USING gin (name gin_trgm_ops);
CREATE INDEX products_sku_trgm_idx  ON products USING gin (sku gin_trgm_ops);
```

Pencarian memakai `name ILIKE '%q%' OR sku ILIKE '%q%'`. B-tree tidak bisa dipakai untuk wildcard `%` di depan karena B-tree diurutkan dari karakter pertama. Trigram GIN index memecah teks menjadi potongan 3 huruf (`kop`, `opi`, ...), sehingga cocok untuk pencarian substring (`q >= 3` karakter).

```bash
go run ./benchmark/beforeafter -sql benchmark/search_no_match.sql -migration migrations/003_indexes.sql
go run ./benchmark/beforeafter -sql benchmark/search_kopi.sql -migration migrations/003_indexes.sql
```

Query sama dengan API (`pos-api/internal/catalog/catalog.go`), limit 21 (20 + 1 untuk cek halaman berikutnya).

Kata yang tidak ada (`zzzz`). Hasil: `benchmark/bench_search_no_match_003_indexes_20261120-033728.txt`:

```
Sebelum: Seq Scan on products p   Rows Removed by Filter: 5000   Buffers: 50   Execution Time: 2.943 ms
Sesudah: Bitmap Index Scan on products_name_trgm_idx / products_sku_trgm_idx   Buffers: 6   Execution Time: 0.146 ms
```

Kata yang umum (`kopi`). Hasil: `benchmark/bench_search_kopi_003_indexes_20261120-033736.txt`:

```
Sebelum dan sesudah: Index Scan using products_pkey   Rows Removed by Filter: 382   Buffers: 32   Execution Time: ~0.4 ms
```

Untuk kata umum, Postgres memilih tidak memakai trigram: ia memindai urut `id` dan berhenti begitu mendapat 21 kecocokan. Trigram berguna untuk kata yang jarang atau tidak ada, karena tanpa index seluruh 5.000 produk harus diperiksa.

Diukur lewat API (`go run ./benchmark/search`, setiap prefix dari 9 kata plus query `category` dan `cursor`, limit 20 seperti web): p95 **4.00 ms** (1 client) dan **29.44 ms** (10 client). Target p95 < 50 ms. Hasil: `benchmark/bench_search_20261120-035135.txt`.

## Trade-off

1. **Biaya write checkout untuk index 004:** Diukur dengan `go run ./benchmark/checkout` (5 ronde, 3 item/keranjang, memakai SQL checkout yang sama dengan `pos-api`): median throughput **814,1 → 807,1 TPS (-0,9%)**, p95 **+1,7%**, p99 **+8,4%** (masih dalam noise antar ronde). Hasil: `benchmark/bench_checkout_write_cost_004_indexes2_20261120-034204.txt`.
2. **Ukuran index:** `transaction_items_transaction_idx` **41 MB** (tabel 68 MB), `transactions_paid_outlet_created_idx` **11 MB** (tabel 39 MB). Trigram index ~296 KB + ~200 KB.
3. **Partial index `PAID`:** Transaksi `VOID` tidak masuk `transactions_paid_outlet_created_idx`. Query laporan yang menyertakan `VOID` tidak dapat memakai index ini.
4. **Locking vs write bloat di checkout:** Mengunci stok lebih dulu dengan satu `SELECT ... ORDER BY product_id FOR UPDATE` lalu menulis semuanya dalam satu CTE membuat 409 tidak meninggalkan dead tuple di `transactions`/`transaction_items` dan menjaga jumlah round trip tetap konstan untuk keranjang multi-item (807 TPS pada 3 item vs 729 TPS sebelumnya), dengan konsekuensi 1 round trip tambahan di bawah lock pada skenario ekstrem 1 produk panas (50 client serentak: 192 req/s, p95 627 ms, `benchmark/bench_hot_product_20261120-035130.txt`).

## Data 10x

Jika transaksi per hari naik 10x, query ber-index akan membaca ~10x lebih banyak transaksi untuk rentang tanggal yang sama (perkiraan top products 3 bulan: ~14 ms → ~140 ms, belum diukur). Jika data bertambah karena histori makin panjang, laporan 3 bulan tetap konstan karena index langsung melompat ke rentang tanggalnya.

Langkah berikutnya jika volume harian naik 10x:
1. Tabel ringkasan harian per outlet dan produk (`outlet_id, day, product_id, qty, revenue`).
2. Hari yang sudah lewat diagregasi oleh job malam (`pg_cron`). Job yang sama dapat menghapus `idempotency_keys` lama (dengan index pada `created_at`).
3. Laporan membaca hari lalu dari tabel ringkasan dan hari ini langsung dari `transactions`.
4. Void untuk transaksi hari lalu menghitung ulang ringkasan hari tersebut.

Ringkasan tidak di-update di dalam transaksi checkout, karena baris ringkasan produk laris akan menjadi titik perebutan lock baru seperti baris stok.

Belum dibuat sekarang karena laporan saat ini sudah ~14 ms dan `pg_cron` memerlukan image Postgres kustom (tidak tersedia di `postgres:16` resmi).

## Catatan

1. API memakai timezone `Asia/Jakarta` (WIB) di Go maupun SQL (`time/tzdata`). Query benchmark `top-products` memakai UTC, sama seperti query legacy, agar perbandingannya adil.
2. Legacy selalu mengembalikan 10 produk (sisanya `qty_sold` NULL jika yang terjual < 10). Query baru hanya mengembalikan produk yang terjual.
3. Query baru deterministik. Legacy hanya `ORDER BY qty_sold`, sedangkan query baru menambahkan tiebreaker (`product_id`, dan di API juga `revenue DESC` / `qty_sold DESC`).
4. Prinsip utama dari kasus ini:
   1. Filter dulu, baru join.
   2. Hindari correlated subquery di `SELECT` bila hasilnya dipakai untuk sorting.
   3. Jangan membungkus kolom index dengan fungsi pada klausa `WHERE`.
   4. Minimalkan jumlah join dan round trip di dalam transaksi.
