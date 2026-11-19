# POS: multi-outlet checkout, stock and reports

Backend: Go 1.27 (`net/http`, [pgx](https://github.com/jackc/pgx)). Frontend: React 19 + TypeScript (Vite,
TanStack Query, Tailwind). Database: PostgreSQL 16.

We use Go because checkout correctness lives in the database transaction, and Go with pgx keeps the SQL and the
transaction boundaries explicit, with no ORM. The standard library covers HTTP, routing and JSON.

Query analysis and EXPLAIN plans: [QUERY_NOTES.md](QUERY_NOTES.md).

## 1. Running

Requirements: Docker. Go 1.27 only for tests and benchmarks.

```bash
cp .env.example .env
docker compose up --build
```

The first start seeds the database (`migrations/001_seed.sql`, unchanged) and applies `002`–`004`.

| | |
|---|---|
| Web | http://localhost:8000 (`/cashier`, `/reports`) |
| API | http://localhost:8080, also at http://localhost:8000/api |
| Postgres | `localhost:5432`, database `pos`, user and password `postgres` |

Reset to a fresh seed:

```bash
docker compose down -v
docker compose up --build
```

Without the API and web containers:

```bash
docker compose up -d db
go run ./pos-api/cmd/api                  # from the repo root, reads .env
cd pos-web && npm install && npm run dev  # http://localhost:5173, proxies /api to :8080
```

| Folder | |
|---|---|
| `pos-api/` | `internal/sales` (checkout, void), `internal/catalog`, `internal/reports`, `internal/web` (JSON, errors, request ID) |
| `pos-web/` | `src/cashier`, `src/reports` |
| `migrations/` | `001` seed, `002` stock constraint and idempotency keys, `003` search indexes, `004` report indexes |
| `benchmark/` | Tools that produce the numbers in this README and in QUERY_NOTES |

## 2. API

Every error is `{"error": "..."}`. A 409 also has `short_items`. Logs are JSON with a request ID, also returned
in `X-Request-ID`.

### `POST /checkout` (SOAL 3.1)

Header `Idempotency-Key: <uuid>`. Body `{"outlet_id", "cashier_name", "items": [{"product_id", "qty"}]}`.

| Status | When |
|---|---|
| 201 | Sale created |
| 200 | Same key and same body again: the original sale |
| 400 | Invalid input (the message names the field) |
| 404 | Unknown outlet or product |
| 409 | Not enough stock: `short_items` lists every short product with `requested` and `available` |
| 422 | Same key, different body |
| 503 | The stock row stayed locked longer than 2 s; retry with the same key |

| SOAL rule | How | Tested by |
|---|---|---|
| 1. Price from the database | Prices are read from `products`. The body has no price field and unknown fields are rejected. | `TestCheckoutTakesPriceFromDatabase` |
| 2. Stock at that outlet decreases | `UPDATE stocks SET qty = qty - n WHERE outlet_id = ... AND product_id = ...` | `TestCheckoutRetryReturnsSameTransaction` |
| 3. Stock never negative, 50 concurrent requests | Check and decrement in one statement: `UPDATE ... WHERE qty >= n`. Rows locked in `product_id` order. `CHECK (qty >= 0)` as a backstop. | `TestConcurrentCheckoutNeverOversells` |
| 3. All or nothing, short products listed | One transaction; any short item rolls everything back. | `TestCheckoutIsAllOrNothing` |
| 4. No duplicate on retry or double-click | The key is claimed with `INSERT ... ON CONFLICT DO NOTHING` in the same transaction. A concurrent request with the same key waits, then gets the original sale. The web app also blocks clicks while a request is in flight. | `TestCheckoutRetryReturnsSameTransaction`, `TestConcurrentSameKeyCreatesOneTransaction` |
| 5. Clear errors, correct status codes | See the table above. | `TestCheckoutValidation`, `TestCheckoutReturns503WhenStockRowStaysLocked` |

The checkout inserts the sale before taking stock, so the stock row is locked only until the commit. A 409
therefore uses up a transaction id (ids can have gaps; they are not receipt numbers).

### `POST /transactions/{id}/void` (SOAL 3.1)

`UPDATE transactions SET status = 'VOID' WHERE id = $1 AND status = 'PAID'`: only the request that changes the
status gives the stock back. Voiding again returns 200 and changes nothing. Unknown id: 404.
Tested by `TestVoidRestoresStockOnce` (10 voids at once, stock restored once).

### `GET /products?outlet_id=&q=&category=&cursor=&limit=` (SOAL 3.2)

`q` matches anywhere in the name or SKU, case-insensitive (trigram indexes). Returns the stock at the outlet;
products without a stock row there are left out. Pages by `cursor` (last id) with `next_cursor`.
Tested by `TestSearchByNameAndSKUWithOutletStock`, `TestSearchFiltersPagesAndEscapes`, `TestSearchValidation`.
p95 through the API: 4 ms (1 client), 29 ms (10 clients). Target 50 ms.

### `GET /reports/daily-sales?outlet_id=&from=&to=` (SOAL 3.2)

Per day: `transactions`, `revenue`, `average`. PAID only. Days are WIB (`Asia/Jakarta`), `to` is inclusive.
Tested by `TestDailySales`, `TestReportValidation`.

### `GET /reports/top-products?outlet_id=&from=&to=&limit=10&sort=qty|revenue` (SOAL 3.2)

`qty_sold` and `revenue` per product, PAID only, ranked by qty (default) or revenue.
Tested by `TestTopProducts`, `TestReportValidation`.

### `GET /outlets`

All outlets, for the outlet selector.

## 3. Tests

The tests run against the real database, because the guarantees come from Postgres locks and constraints. Each
test creates its own outlet, products and sales and deletes them afterwards.

```bash
docker compose up -d db
cd pos-api
DATABASE_URL=postgres://postgres:postgres@localhost:5432/pos go test ./...
```

| Package | Tests |
|---|---|
| `internal/sales` | 50 concurrent checkouts on 10 in stock; all or nothing; price from the database; retry and replay; 20 concurrent requests with one key; 503 on a held lock; 10 concurrent voids; validation |
| `internal/catalog` | Search by name and SKU with outlet stock; category, pagination, `%` escaping; validation |
| `internal/reports` | Daily sales per WIB day without VOID; top products by qty and revenue; validation |

The web app was also tested in a browser: search, cart, checkout, double-click, insufficient stock, and the
report loading, empty and error states.

### Benchmarks

Each writes one report into `benchmark/`. Tools marked * copy `pos`, so stop the API first
(`docker compose stop api`).

| Command | Measures | Result |
|---|---|---|
| `go run ./benchmark/topproducts` * | Top products: legacy, rewrite, rewrite + `004` | 144.2 s → 39 ms → 15 ms |
| `go run ./benchmark/beforeafter -sql <file> -migration <file>` * | One query without and with a migration | Daily sales 13 ms → 4 ms; search with no match 3.3 ms → 0.4 ms |
| `go run ./benchmark/checkout` * | Checkout cost of the `004` indexes | About 3%, within noise |
| `go run ./benchmark/hotproduct` | 1000 checkouts from 50 clients on one product | 267/s, p95 398 ms, stock never negative |
| `go run ./benchmark/search` | Search latency while typing | p95 4 ms (1 client), 29 ms (10 clients) |

## 4. Assumptions

- Report dates are WIB days.
- A repeated void is a success (200), not an error.
- A price sent by the client is rejected (400), not ignored.
- There is no login; the cashier name is free text.
- The client generates the idempotency key (`crypto.randomUUID()` in the web app).

## 5. What I would improve

- Delete idempotency keys older than 72 hours, hourly. With `pg_cron` on managed Postgres, otherwise a ticker in
  pos-api.
- A daily summary table for reports if sales per day grow about 10x (see QUERY_NOTES).
- Batch the stock updates into one statement, with an explicit `SELECT ... ORDER BY product_id FOR UPDATE` to keep
  the lock order.
- Void and receipt view in the web app; frontend tests.
- Login and a real cashier identity.
