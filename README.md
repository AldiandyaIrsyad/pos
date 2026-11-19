# POS: multi-outlet checkout, stock and reports

Backend: Go 1.27 (`net/http`, [pgx](https://github.com/jackc/pgx)). Frontend: React 19 + TypeScript (Vite,
TanStack Query, Tailwind). Database: PostgreSQL 16.

We use Go because checkout correctness lives in the database transaction, and Go with pgx keeps the SQL and the
transaction boundaries explicit, with no ORM. The standard library covers HTTP, routing and JSON.

Query analysis and EXPLAIN plans: [QUERY_NOTES.md](QUERY_NOTES.md).

## 1. Running

Requirements: Docker. Go 1.27 and Node 24 only for running tests and benchmarks locally.

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
| `migrations/` | `001` seed, `002` stock/status constraints and idempotency keys, `003` search indexes, `004` report indexes |
| `benchmark/` | Tools that produce the numbers in this README and in QUERY_NOTES |

## 2. API

Every error is `{"error": "..."}`. A 409 also has `short_items`. Logs are JSON with a request ID, also returned
in `X-Request-ID`.

### `POST /checkout`

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

| Guarantee | How | Tested by |
|---|---|---|
| Price from the database | Prices are read from `products`. The body has no price field and unknown fields are rejected. | `TestCheckoutTakesPriceFromDatabase` |
| Stock at that outlet decreases | Batched `UPDATE stocks` inside one writable CTE after locking the outlet's stock rows. | `TestCheckoutRetryReturnsSameTransaction` |
| Stock never negative under concurrency | All requested stock rows are locked in `product_id` order with one `SELECT ... FOR UPDATE`, checked against committed quantities, then decremented with `WHERE qty >= n` and a `CHECK (qty >= 0)` backstop. | `TestConcurrentCheckoutNeverOversells`, `TestConcurrentMultiItemOppositeOrderNeverDeadlocks` |
| All or nothing, short products listed | One transaction; shortages roll back before writing `transactions` or `transaction_items`, reporting exact locked availability for every short item. | `TestCheckoutIsAllOrNothing`, `TestCheckoutReportsAllShortItemsAndAllowsRetry` |
| No duplicate on retry or double-click | The key is claimed with `INSERT ... ON CONFLICT DO NOTHING` in the same transaction. Concurrent requests with the same key wait, then replay the original sale. The web app blocks clicks while a request is in flight and reuses the key across network/5xx retries. | `TestCheckoutRetryReturnsSameTransaction`, `TestConcurrentSameKeyCreatesOneTransaction` |
| Clear errors, strict boundary validation | Strict single-object JSON decoding, 32-bit ID bounds, NUL-byte rejection, post-merge qty cap, and 503 on lock timeout. | `TestCheckoutValidation`, `TestCheckoutReturns503WhenStockRowStaysLocked`, `TestDecodeJSON` |

### `POST /transactions/{id}/void`

`UPDATE transactions SET status = 'VOID' WHERE id = $1 AND status = 'PAID' RETURNING ...`: only the request that
flips the status restores stock (locked in `product_id` order and updated in one statement). Voiding again returns
200 and changes nothing. Unknown id: 404. `transactions_status_valid` enforces `status IN ('PAID', 'VOID')`.
Tested by `TestVoidRestoresStockOnce` and `TestConcurrentCheckoutAndVoidPreserveStock`.

### `GET /products?outlet_id=&q=&category=&cursor=&limit=`

`q` matches anywhere in the name or SKU, case-insensitive (trigram indexes). Returns the stock at the outlet;
products without a stock row there are left out. Pages by `cursor` (last id) with `next_cursor`, wired to a
"Load more" control in the cashier UI.
Tested by `TestSearchByNameAndSKUWithOutletStock`, `TestSearchFiltersPagesAndEscapes`, `TestSearchValidation`.
p95 through the API: 4.0 ms (1 client), 29.4 ms (10 clients). Target 50 ms.

### `GET /reports/daily-sales?outlet_id=&from=&to=`

Per day: `transactions`, `revenue`, `average`. PAID only. Days are WIB (`Asia/Jakarta`), `to` is inclusive, and
ranges are capped at 366 days.
Tested by `TestDailySales`, `TestReportValidation`.

### `GET /reports/top-products?outlet_id=&from=&to=&limit=10&sort=qty|revenue`

`qty_sold` and `revenue` per product, PAID only, ranked by `qty` (default) or `revenue` (exposed via a toggle in
the reports UI).
Tested by `TestTopProducts`, `TestReportValidation`.

### `GET /outlets`

All outlets, for the outlet selector.

## 3. Tests

Backend tests run against the real database, because the guarantees come from Postgres locks and constraints. Each
test creates its own outlet, products and sales and deletes them afterwards.

```bash
docker compose up -d db
cd pos-api
DATABASE_URL=postgres://postgres:postgres@localhost:5432/pos go test ./...
```

Frontend unit tests run with Node 24's built-in test runner (no extra test framework dependencies):

```bash
cd pos-web
npm test
npm run lint
npm run build
```

| Suite | Coverage |
|---|---|
| `pos-api/internal/sales` | 50 concurrent checkouts on 10 in stock; opposite-order multi-item deadlock test; all-or-nothing; missing stock rows and retry after 409; price from DB; duplicate-product merge; retry/replay and 20 concurrent same-key requests; 503 on held lock; 10 concurrent voids; concurrent checkout + void conservation; boundary validation |
| `pos-api/internal/catalog` | Search by name and SKU with outlet stock; category, pagination, `%` escaping; overflow/NUL/range validation |
| `pos-api/internal/reports` | Daily sales per WIB day without VOID; top products by qty and revenue; range/overflow validation |
| `pos-api/internal/web` | Strict JSON decoding (trailing tokens, braces, oversize); lock/statement timeout 503; client-cancel 499; `X-Request-ID` middleware |
| `pos-web` (`npm test`) | `parseQty` and `cartReducer`; UUIDv4 fallback and idempotency key lifecycle; report URL param parsing and 366-day validation |

### Benchmarks

Each writes one report into `benchmark/`. Tools marked * copy `pos`, so stop the API first
(`docker compose stop api`).

| Command | Measures | Result |
|---|---|---|
| `go run ./benchmark/topproducts` * | Top products: legacy, rewrite, rewrite + `004` | 159.4 s → 49.6 ms → 14.2 ms (p95 15.6 ms) |
| `go run ./benchmark/beforeafter -sql <file> -migration <file>` * | One query without and with a migration | Daily sales p50 13.3 ms → 4.9 ms (39 pages, 0 heap fetches); no-match search 3.6 ms → 0.8 ms |
| `go run ./benchmark/checkout` * | Checkout write cost of the `004` indexes (5 rounds, 3-item CTE checkout) | 814.1 → 807.1 TPS (-0.9%, within noise) |
| `go run ./benchmark/hotproduct` | 1000 checkouts from 50 clients on one product | 192 req/s, p95 627 ms, 800×201 / 200×409, stock never negative |
| `go run ./benchmark/search` | Search latency while typing + category/cursor queries | p95 4.0 ms (1 client), 29.4 ms (10 clients) |

## 4. Assumptions

- Report dates are `Asia/Jakarta` (WIB) calendar days, capped at 366 days per request.
- A repeated void is a success (200), not an error.
- A price sent by the client is rejected (400), not ignored.
- There is no login; the cashier name is free text.
- The client generates the idempotency key (`crypto.randomUUID()`, with a `crypto.getRandomValues` UUIDv4 fallback for non-HTTPS origins).

## 5. What I would improve

- Delete idempotency keys older than 72 hours, hourly (with an index on `idempotency_keys(created_at)`). Use `pg_cron` on managed Postgres, otherwise a ticker in `pos-api`.
- A daily summary table for reports if sales per day grow about 10x (see [QUERY_NOTES.md](QUERY_NOTES.md)).
- Void and receipt view in the web app, plus browser E2E tests.
- Login and a real cashier identity.
