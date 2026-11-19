# POS: multi-outlet checkout, stock and reports

| Part | Stack |
|---|---|
| `pos-api/` | Go 1.27, standard library `net/http`, [pgx](https://github.com/jackc/pgx) |
| `pos-web/` | React 19 + TypeScript, Vite, TanStack Query, Tailwind |
| `migrations/` | Seed (unchanged) plus my constraints and indexes |
| `benchmark/` | Go tools that produce the numbers in this README and in [QUERY_NOTES.md](QUERY_NOTES.md) |

Query analysis, EXPLAIN plans and index reasons: [QUERY_NOTES.md](QUERY_NOTES.md).

---

## 1. Running it from zero

Requirements: Docker. Go 1.27 only to run the tests and benchmarks.

```bash
cp .env.example .env          # optional; the defaults match it
docker compose up --build
```

The first start creates the `pos` database from `migrations/001_seed.sql` (about 300k transactions,
1–2 minutes), then applies `002`–`004`. The API waits for the database health check, which turns
healthy only after the seed and migrations finish.

| What | Where |
|---|---|
| Web (cashier, reports) | http://localhost:8000 |
| API | http://localhost:8080 (also via the web at `/api/...`) |
| Postgres | `localhost:5432`, database `pos`, user/password `postgres` |

**Reset** to a freshly seeded database:

```bash
docker compose down -v
docker compose up --build
```

**Tests** (need the compose database running; each test creates its own outlet and products and
deletes them, so the seed data is untouched):

```bash
cd pos-api
DATABASE_URL=postgres://postgres:postgres@localhost:5432/pos go test ./...
```

**Local development** without the API/web containers:

```bash
docker compose up -d db
go run ./pos-api/cmd/api                 # from the repo root: reads DATABASE_URL from .env, serves :8080
cd pos-web && npm install && npm run dev  # :5173, proxies /api to :8080
```

### Why Go

Checkout correctness lives in the database transaction, so I wanted a language where the SQL and the
transaction boundaries are explicit and nothing is hidden by an ORM. Go's standard library covers the
HTTP server, routing (`POST /transactions/{id}/void` patterns), JSON and graceful shutdown; pgx gives direct
control over transactions and Postgres error codes. The API is a single static binary with two
dependencies.

---

## 2. Design decisions

### Stock never goes negative

The check and the decrement are one statement:

```sql
UPDATE stocks SET qty = qty - $3
WHERE outlet_id = $1 AND product_id = $2 AND qty >= $3
```

- `RowsAffected() = 1` means the stock was taken; `0` means it was short.
- The `UPDATE` locks the row. A concurrent checkout of the same product waits, and when the first one
  commits, Postgres (READ COMMITTED) re-evaluates `qty >= $3` against the new row. There is no window
  between reading and writing, so no number of concurrent requests can oversell.
- Items are merged and sorted by `product_id` before locking, and void restores in the same order, so
  two carts sharing products queue instead of deadlocking.
- **All or nothing:** if any item is short, the whole transaction rolls back and the 409 lists every
  short product with `requested` and `available`.
- `CHECK (qty >= 0)` on `stocks` (`migrations/002_checkout.sql`) is a backstop for any future code path.

Proof: `TestConcurrentCheckoutNeverOversells` sends 50 simultaneous checkouts for a product with 10 in
stock and asserts exactly 10 × 201, 40 × 409, final stock 0 and 10 transactions.

### No duplicate transactions

The client sends an `Idempotency-Key: <uuid>` header, new per sale and the same on every retry.

1. The first statement of the checkout transaction claims the key:
   `INSERT INTO idempotency_keys ... ON CONFLICT (key) DO NOTHING`.
2. A concurrent request with the same key blocks on the primary key until the first transaction ends.
   After a commit it replays the original transaction (**200**, same id); after a rollback it proceeds
   as a fresh attempt.
3. The key stores a hash of the normalized request. The same key with a different cart gets **422**.
4. A failed checkout (409, timeout) rolls the key back too, so a retry is judged against current stock.

The frontend adds two guards: a ref ignores clicks while a request is in flight (a ref, not state, so a
second click before re-render is still blocked), and the key is reused on retry and replaced when the
cart, outlet or cashier changes.

### Void

`UPDATE transactions SET status = 'VOID' WHERE id = $1 AND status = 'PAID' RETURNING outlet_id`: only
the request that flips the status gets a row back and restores stock to that outlet. A concurrent or
repeated void finds nothing to flip and returns the transaction unchanged (200), so retrying a void is
harmless. Unknown id: 404.

### Peak load

- A stock row stays locked from its `UPDATE` until commit, and every checkout of that product waits
  for it. So the checkout does everything that needs no stock row first (outlet check, prices,
  inserting the transaction and its items, linking the key) and takes the stock last; only the
  commit runs while the locks are held. A shortage still rolls the whole sale back. Measured with the
  load test below, against the previous order (stock first, then four statements under the lock):
  219–236 → 260–274 checkouts/s, p50 209–221 → 179–184 ms (3 runs each).
- Side effect: a 409 has already used a transaction id, which the rollback doesn't return (Postgres
  sequences never roll back), so ids have gaps. Ids identify transactions; nothing counts or numbers
  receipts by them. A legally gapless receipt number would need a separate per-outlet counter
  assigned at commit, which would make checkouts in one outlet run one at a time.
- Every connection has `lock_timeout = 2s` and `statement_timeout = 5s`. A checkout stuck behind a
  busy row gets **503 with `Retry-After: 1`** instead of hanging; its transaction is rolled back, so a
  retry with the same key is safe.
- Load test (`benchmark/hotproduct`, 1000 checkouts from 50 clients on one product with 800 in stock):
  800 × 201, 200 × 409, final stock 0, 800 transactions; 267 checkouts/s, p50 181 ms, p95 398 ms.
  Every checkout of the same product serializes on one row, so this is the worst case.
  Report: `benchmark/bench_hot_product_20261119-155703.txt`.

### Input and errors

- Prices always come from `products`. The request has no price field, and unknown JSON fields are
  rejected, so a client-sent price is a 400.
- Bodies over 64 KiB, wrong types, trailing data and invalid field values are 400 with a message
  naming the field. Unknown outlet or product: 404.
- Every error body has the same shape: `{"error": "..."}` (a 409 adds `short_items`).
- 500s log the real error with the request ID and return only `"internal error"`.

### Structure

Packages are split by feature, each owning its tables:

| Package | Routes | Writes |
|---|---|---|
| `internal/sales` | `POST /checkout`, `POST /transactions/{id}/void` | `stocks`, `transactions`, `transaction_items`, `idempotency_keys` |
| `internal/catalog` | `GET /outlets`, `GET /products` | nothing |
| `internal/reports` | `GET /reports/daily-sales`, `GET /reports/top-products` | nothing |
| `internal/web` | JSON helpers, error responses, request-ID middleware | — |

Handlers run SQL directly; there is no service or repository layer, since each would have exactly one
implementation. `cmd/api/main.go` only wires things together and handles graceful shutdown.

Observability: one JSON access log line per request and an `X-Request-ID` (taken from the client or
generated) on every response and log line.

### Reports and search

Summary only; details in [QUERY_NOTES.md](QUERY_NOTES.md).

- Top products: the legacy query ran two correlated subqueries per product (5000 × a scan of 1M
  items). The rewrite is one `GROUP BY` over a half-open date range on the bare `created_at`.
  144.2 s → 39 ms (rewrite) → 15 ms (rewrite + index).
- `transactions (outlet_id, created_at) INCLUDE (id, total) WHERE status = 'PAID'` serves both
  reports; `transaction_items (transaction_id) INCLUDE (product_id, qty, subtotal)` serves top
  products and loading a transaction's items (void, replay).
  Measured cost on checkout writes: about 3%, within run-to-run noise.
- Product search uses trigram GIN indexes on `name` and `sku` for `ILIKE '%q%'`, with keyset
  pagination (`cursor` = last id). Measured through the API (`benchmark/search`, every prefix of 9
  typed terms, limit 20 like the web app): **p95 4 ms** from one client, **29 ms** from 10 clients at
  once, against the 50 ms target. Report: `benchmark/bench_search_20261119-160812.txt`.

### Benchmarks

Each tool writes one self-describing report into `benchmark/`. Tools marked * copy `pos` into a
throwaway database, so stop the API first (`docker compose stop api`).

| Command | Measures |
|---|---|
| `go run ./benchmark/topproducts` * | Top products: legacy, rewrite, rewrite + `004` indexes, with EXPLAIN (ANALYZE, BUFFERS) |
| `go run ./benchmark/checkout` * | Checkout throughput and latency with and without the `004` indexes |
| `go run ./benchmark/hotproduct` | Checkout load on one product through the API (API must be running) |
| `go run ./benchmark/search` | Product search latency through the API while typing (API must be running) |

---

## 3. Assumptions

- Report dates are WIB (`Asia/Jakarta`). `from` and `to` are inclusive calendar days.
- Product search lists only products that have a stock row at the selected outlet. `q` matches
  anywhere in the name or SKU, case-insensitive.
- A repeated void is a success (200) rather than a conflict, so a retried void is harmless.
- A client-sent price is rejected (400), not silently ignored, because it signals a client bug.
- There is no authentication; the cashier name is free text, as in the brief.
- Idempotency keys are UUIDs generated by the client (`crypto.randomUUID()` in the web app).

---

## 4. What I would improve with more time

- **Fewer round trips:** the stock step sends one `UPDATE` per item, and the inserts are three
  statements. Each could become one statement (`UPDATE ... FROM unnest(...)`, and a CTE chaining the
  inserts), but a single multi-row `UPDATE` doesn't guarantee the `product_id` lock order that
  prevents deadlocks, so it needs an explicit `SELECT ... ORDER BY product_id FOR UPDATE` first.
- **Idempotency key cleanup:** keys are kept forever, but only need to outlive a retry. An hourly
  `DELETE FROM idempotency_keys WHERE created_at < now() - interval '72 hours'` is enough (a key
  deleted too early would let a late retry create a second sale, hence the wide margin). Where to run
  it: `pg_cron` on managed Postgres (runs once, next to the data; the official `postgres:16` image
  doesn't include the extension, so plain Docker would need a custom image), otherwise a ticker
  goroutine in pos-api (harmless if several instances run it, since the delete is idempotent) or the
  platform's scheduler (Kubernetes CronJob). Not cron inside the API container.
- **Reports at 10x data:** with 10x more sales per day, the indexed report reads 10x the rows (the
  index already skips older history). The fix is a daily summary per outlet and product: a nightly job
  aggregates closed days, and reports read past days from the summary and today live from
  `transactions`. Updating the summary inside checkout would instead add a second hot row to every
  checkout. Monthly partitions of `transactions` would help long history, not busy days.
- **Void in the UI** and a receipt view; void is API-only now.
- **Frontend tests**, starting with the cart reducer, and API tests for catalog and reports.
- **Authentication** and a real cashier identity instead of a free-text name.
