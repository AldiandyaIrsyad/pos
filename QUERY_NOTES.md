A/N: Biasanya pakai bahasa inggris untuk analisis dan lain lain. Namun, dikarenakan `SOAL.md` menggunakan bahasa Indonesia. Saya akan menggunakan Bahasa Indonesia.

## Cara mengukur

Semua angka top products di bawah berasal dari satu kali jalan:

```
go run ./benchmark/topproducts
```

Tool ini mengukur tiga variant. Masing-masing jalan di salinan baru database `pos`, jadi `pos` sendiri tidak berubah:

1. query legacy, tanpa index dari `004_indexes2.sql`
2. query baru, tanpa index tersebut
3. query baru, dengan `004_indexes2.sql`

Tiap variant: satu `EXPLAIN (ANALYZE, BUFFERS)` (sekaligus memanaskan cache), lalu beberapa run yang diukur waktunya. Hasil lengkap (ringkasan + ketiga plan): `benchmark/bench_top_products_20261119-135307.txt`

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

Berdasarkan plan (variant 1) kita dapat liat berapa rows yang diakses
```
Seq Scan on products p  (actual rows=5000 loops=1)
  SubPlan 1  (qty_sold)
    ->  Nested Loop  (actual rows=5 loops=5000)
          ->  Seq Scan on transaction_items ti  (actual rows=210 loops=5000)
                Filter: (product_id = p.id)
                Rows Removed by Filter: 1048070
                Buffers: shared hit=10400000 read=33280000
          ->  Index Scan using transactions_pkey on transactions t  (loops=1048280)
                Filter: ((outlet_id = 1) AND (status = 'PAID') AND (date(created_at) >= ...) ...)
SubPlan 2  (revenue)
  ->  Nested Loop  (actual rows=111 loops=10)
Buffers: shared hit=14794114 read=33349996
Execution Time: 142730 ms
```

Dari sini kita dapat melihat subquery `qty_sold` jalan 5000x (sekali per produk). Tiap kali, seluruh `transaction_items` (~1 juta row) di-scan untuk mencari satu produk. Jadi sekitar 5000 * 1 juta = ~5 milyar row yang dicek. Biayanya juga terlihat di `Buffers`: seq scan itu saja menyentuh ~43,7 juta page (8.736 page × 5000 kali). Seluruh query ~48,1 juta page.

Subquery `revenue` hanya jalan 10x (`loops=10`). Postgres menghitungnya setelah `ORDER BY ... LIMIT 10`, jadi hanya untuk 10 row yang tersisa. Yang mahal adalah `qty_sold`, karena dia dipakai untuk sorting dan harus dihitung untuk semua 5000 produk.

> avg: 144.2s (3 run)

Untuk memperbaikinya, kita dapat
1. Filter dahulu sebelum join. Ambil dulu transaksi outlet 1, PAID, di rentang tanggal (~7.000 row), baru join ke `transaction_items`. Bukan scan 1 juta item untuk setiap produk.
2. Join sekali. `qty_sold` dan `revenue` dihitung dari join yang sama dengan `SUM` di satu `GROUP BY`.
3. Tidak bergantungan dengan outer loop variable (`ti.product_id = p.id`). Tanpa itu tidak ada nested loop per produk, query dalam cukup jalan sekali.
4. Jangan menggunakan fungsi untuik `created_at`  namun `>=` dan `<`. Hal ini dikarenakan database tidak dapat menggunakan index bila kolom index tertutup sebuah fungsi (seperti `UPPER()`, `DATE()`).
5. Ambil nama produk hanya untuk 10 hasil teratas, bukan untuk 5000 produk.

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

Semua poin di atas dipakai. `BETWEEN '2026-01-01' AND '2026-03-31'` dengan `DATE()` menjadi range setengah terbuka `>= '2026-01-01' AND < '2026-04-01'` pada kolom aslinya. 

Plan tanpa index baru (variant 2)
```
Parallel Hash Join  (actual rows=8371 loops=3)
  ->  Parallel Seq Scan on transaction_items ti  (actual rows=349427 loops=3)
  ->  Parallel Seq Scan on transactions t  (actual rows=2398 loops=3)
        Rows Removed by Filter: 97602
Index Scan using products_pkey on products p  (loops=10)
Buffers: shared hit=3802 read=9994
Execution Time: 56.833 ms
```

Sekarang setiap tabel hanya di-scan sekali, lalu di-hash join. Masih ada seq scan karena belum ada index untuk `outlet_id` + `created_at`, jadi Postgres harus membaca semua 300 ribu transaksi dan 1 juta item. Tapi cukup sekali, bukan 5000x.

> avg: 39.00ms (10 run), 13.796 page. Dari 144 detik ke 39 ms hanya dengan mengubah query, tanpa biaya tambahan di write.

## Index (`migrations/004_indexes2.sql`)

```sql
CREATE INDEX transactions_paid_outlet_created_idx
  ON transactions (outlet_id, created_at) INCLUDE (id, total)
  WHERE status = 'PAID';

CREATE INDEX transaction_items_transaction_idx
  ON transaction_items (transaction_id) INCLUDE (product_id, qty, subtotal);
```

Alasan `transactions_paid_outlet_created_idx`:
1. `(outlet_id, created_at)`: kolom yang dicari dengan `=` di depan, kolom range di belakang. Dengan urutan ini index langsung lompat ke outlet 1, lalu baca rentang tanggalnya berurutan. Kalau dibalik `(created_at, outlet_id)`, index harus membaca semua outlet di rentang tanggal itu lalu membuang yang bukan outlet 1.
2. `WHERE status = 'PAID'` (partial index): semua laporan hanya pakai PAID, jadi transaksi VOID tidak perlu masuk index.
3. `INCLUDE (id, total)`: `id` untuk join ke items dan `total` untuk daily sales sudah ada di index. Untuk daily sales Postgres tidak perlu membuka tabelnya (index-only scan).

Alasan `transaction_items_transaction_idx`:
1. Join `ti.transaction_id = t.id` langsung cari item dari transaksi yang sudah difilter, bukan scan 1 juta item.
2. `INCLUDE (product_id, qty, subtotal)`: semua kolom yang dibutuhkan top products ada di index, jadi index-only scan (`Heap Fetches: 6`).
3. Index ini juga dipakai saat void dan replay idempotency (ambil item dari satu transaksi).

Plan dengan index (variant 3)
```
Nested Loop  (actual rows=8371 loops=3)
  ->  Parallel Bitmap Heap Scan on transactions t  (actual rows=2398 loops=3)
        ->  Bitmap Index Scan on transactions_paid_outlet_created_idx  (actual rows=7195 loops=1)
  ->  Index Only Scan using transaction_items_transaction_idx on transaction_items ti  (actual rows=3 loops=7195)
        Heap Fetches: 6
Buffers: shared hit=19808 read=4310
Execution Time: 22.955 ms
```

Sekarang hanya 7.195 transaksi yang relevan yang dibaca, lalu untuk masing-masing ~3 item dicari lewat index.

> avg: 15.24ms (10 run), 24.118 page.

Catatan: dengan index, jumlah page justru naik (13.796 → 24.118), tapi waktu turun. `Buffers` menghitung berapa kali page disentuh, bukan berapa page yang berbeda.

| | tanpa index | dengan index |
|---|---|---|
| page disentuh | 13.796 | 24.118 |
| dari disk/OS (`read`) | 9.994 | 4.310 |
| dari cache (`hit`) | 3.802 | 19.808 |
| row yang diperiksa | ~1,35 juta | ~32 ribu |

1. Tanpa index, Postgres membaca semua page kedua tabel masing-masing sekali, tapi harus memeriksa semua 1 juta item dan 300 ribu transaksi.
2. Dengan index, setiap dari 7.195 lookup melewati ~3 page index (root → tengah → leaf). Root dan page tengahnya sama untuk setiap lookup, jadi page yang sama disentuh berulang kali. Page itu sudah ada di cache, makanya kebanyakan `hit` dan murah.
3. Yang benar-benar mahal turun: page dari disk turun lebih dari setengah, dan row yang diperiksa turun ~40x.

## Ringkasan top products

| | avg | p95 | page (hit+read) | vs legacy |
|---|---|---|---|---|
| 1. legacy, tanpa index | 144.2s | 144.9s | 48.144.110 | 1x |
| 2. query baru, tanpa index | 39.00ms | 41.85ms | 13.796 | 3.697x |
| 3. query baru + `004_indexes2.sql` | 15.24ms | 17.11ms | 24.118 | 9.462x |

## Daily sales

```
go run ./benchmark/beforeafter -sql benchmark/daily_sales.sql -migration migrations/004_indexes2.sql
```

Query sama dengan API (`pos-api/internal/reports/reports.go`), outlet 1, Q1 2026 WIB. Hasil: `benchmark/bench_daily_sales_004_indexes2_20261119-164114.txt`

Sebelum 004
```
Parallel Seq Scan on transactions  (actual rows=2398 loops=3)
  Rows Removed by Filter: 97602
Buffers: shared hit=2154 read=2920
Execution Time: 14.055 ms
```

Sesudah 004
```
Index Only Scan using transactions_paid_outlet_created_idx on transactions  (actual rows=7195 loops=1)
  Heap Fetches: 0
Buffers: shared hit=1 read=46
Execution Time: 4.496 ms
```

Sebelumnya semua 300 ribu transaksi dibaca lalu disaring. Sekarang index langsung ke outlet 1 dan rentang tanggalnya. `total` ada di index (`INCLUDE`), jadi tabelnya tidak dibuka sama sekali (`Heap Fetches: 0`).

> avg: 13.22ms → 4.48ms (20 run), 5.074 → 47 page.

## Product search

`migrations/003_indexes.sql`:

```sql
CREATE INDEX products_name_trgm_idx ON products USING gin (name gin_trgm_ops);
CREATE INDEX products_sku_trgm_idx  ON products USING gin (sku gin_trgm_ops);
```

Pencarian memakai `name ILIKE '%q%' OR sku ILIKE '%q%'`. B-tree tidak bisa dipakai untuk `%` di depan, karena B-tree diurutkan dari huruf pertama. Trigram index memecah teks jadi potongan 3 huruf (`kop`, `opi`, ...), jadi bisa mencari di tengah kata.

```
go run ./benchmark/beforeafter -sql benchmark/search_no_match.sql -migration migrations/003_indexes.sql
go run ./benchmark/beforeafter -sql benchmark/search_kopi.sql -migration migrations/003_indexes.sql
```

Query sama dengan API (`pos-api/internal/catalog/catalog.go`), limit 21 (20 + 1 untuk cek halaman berikutnya).

Kata yang tidak ada (`zzzz`). Hasil: `benchmark/bench_search_no_match_003_indexes_20261119-164130.txt`
```
Sebelum: Seq Scan on products p   Rows Removed by Filter: 5000   Execution Time: 2.962 ms
Sesudah: Bitmap Index Scan on products_name_trgm_idx / products_sku_trgm_idx   Execution Time: 0.128 ms
```

Kata yang umum (`kopi`). Hasil: `benchmark/bench_search_kopi_003_indexes_20261119-164123.txt`
```
Sebelum dan sesudah: Index Scan using products_pkey   Rows Removed by Filter: 382   Execution Time: ~0.4 ms
```

Untuk kata yang umum, Postgres tidak memakai trigram. Dia jalan urut `id` dan berhenti setelah 21 yang cocok, dan itu sudah cepat. Trigram berguna untuk kata yang jarang atau tidak ada, karena tanpa index semua 5.000 produk harus dicek.

Diukur lewat API (`go run ./benchmark/search`, setiap prefix dari 9 kata seperti orang mengetik, limit 20 seperti web): p95 4 ms dari 1 client, 29 ms dari 10 client sekaligus. Target p95 < 50 ms. Hasil: `benchmark/bench_search_20261119-160812.txt`

## Trade-off

1. Setiap checkout juga menulis ke index 004. Diukur dengan `go run ./benchmark/checkout`: throughput -3,1%, masih di dalam noise antar ronde (5-10%). Hasil: `benchmark/bench_checkout_write_cost_004_indexes2_20261119-131641.txt`
2. Index 004 memakan tempat: `transaction_items_transaction_idx` 41 MB (tabel 68 MB), `transactions_paid_outlet_created_idx` 14 MB (tabel 39 MB). Trigram index 296 KB + 200 KB.
3. Transaksi VOID tidak masuk `transactions_paid_outlet_created_idx`. Laporan yang butuh VOID tidak bisa memakai index ini.

## Data 10x

Kalau penjualan per hari naik 10x, query dengan index membaca 10x lebih banyak transaksi untuk rentang tanggal yang sama. Perkiraan top products 3 bulan dari 15 ms ke ~150 ms (belum diukur). Kalau datanya bertambah karena history lebih panjang, laporan 3 bulan tidak berubah, karena index langsung ke rentang tanggalnya.

Yang akan saya lakukan:
1. Tabel ringkasan harian per outlet dan produk (`outlet_id, day, product_id, qty, revenue`).
2. Hari yang sudah lewat diisi job malam (`pg_cron`). Job yang sama juga bisa menghapus idempotency key lama.
3. Laporan membaca hari lalu dari ringkasan dan hari ini langsung dari `transactions`.
4. Void untuk hari lalu menghitung ulang hari itu.

Ringkasan tidak di-update di dalam checkout, karena row ringkasan produk yang laris akan jadi row yang diperebutkan seperti row stok.

Belum dibuat sekarang karena belum diperlukan: laporan saat ini 15 ms. `pg_cron` juga butuh image Postgres sendiri, karena tidak ada di image resmi `postgres:16`.

## Catatan

1. API memakai timezone `Asia/Jakarta` (WIB). Query benchmark memakai UTC, sama seperti query legacy, jadi perbandingannya adil.
2. Legacy selalu mengembalikan 10 produk. Kalau yang terjual kurang dari 10, sisanya `qty_sold` NULL. Query baru hanya mengembalikan produk yang terjual.
3. Query baru deterministik. Legacy hanya `ORDER BY qty_sold`, jadi kalau peringkat 10 seri, produk yang muncul bisa berbeda tergantung urutan data di database. Query baru menambahkan `product_id` sebagai pemisah seri.
4. Rule of thumb dari kasus ini:
   1. Filter dulu, baru join.
   2. Hindari subquery yang bergantung pada row luar (correlated subquery) di `SELECT` kalau dipakai untuk sorting.
   3. Kolom index tidak berfungsi bila menggunakan sebuah fungsi.
   4. Minimalisir join.
