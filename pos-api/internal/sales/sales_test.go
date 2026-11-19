package sales

// These tests run against a real, migrated Postgres (the concurrency guarantees
// come from Postgres, so a fake would prove nothing):
//
//	DATABASE_URL=postgres://postgres:postgres@localhost:5433/pos go test ./...
//
// Each test creates its own outlet and products and deletes them afterwards,
// so the seed data is left as it was.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL must point at a migrated pos database")
		os.Exit(1)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		panic(err)
	}
	// Enough connections that all 50 checkouts really are in the database at once,
	// instead of queueing for a connection in the pool.
	cfg.MaxConns = 60
	// main.go uses 2s. 1s is still longer than any lock wait in the concurrency
	// tests, and keeps the 503 test quick.
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "1s"
	testPool, err = pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		panic(err)
	}
	code := m.Run()
	testPool.Close()
	os.Exit(code)
}

type fixture struct {
	t        *testing.T
	handler  http.Handler
	outletID int
	products []int
}

// newFixture creates an outlet with one product per entry of stock, holding that qty.
func newFixture(t *testing.T, stock ...int) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{t: t}
	err := testPool.QueryRow(ctx,
		`INSERT INTO outlets (code, name) VALUES ('TEST', 'Test outlet') RETURNING id`).Scan(&f.outletID)
	if err != nil {
		t.Fatal(err)
	}
	for i, qty := range stock {
		var id int
		err := testPool.QueryRow(ctx,
			`INSERT INTO products (sku, name, category, price) VALUES ($1, $2, 'Test', 1500) RETURNING id`,
			fmt.Sprintf("TEST-%d-%d", f.outletID, i), fmt.Sprintf("Test product %d", i)).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := testPool.Exec(ctx,
			`INSERT INTO stocks (outlet_id, product_id, qty) VALUES ($1, $2, $3)`, f.outletID, id, qty); err != nil {
			t.Fatal(err)
		}
		f.products = append(f.products, id)
	}
	t.Cleanup(func() {
		for _, sql := range []string{
			`DELETE FROM idempotency_keys WHERE transaction_id IN (SELECT id FROM transactions WHERE outlet_id = $1)`,
			`DELETE FROM transaction_items WHERE transaction_id IN (SELECT id FROM transactions WHERE outlet_id = $1)`,
			`DELETE FROM transactions WHERE outlet_id = $1`,
			`DELETE FROM stocks WHERE outlet_id = $1`,
		} {
			if _, err := testPool.Exec(ctx, sql, f.outletID); err != nil {
				t.Error(err)
			}
		}
		if _, err := testPool.Exec(ctx, `DELETE FROM products WHERE id = ANY($1)`, f.products); err != nil {
			t.Error(err)
		}
		if _, err := testPool.Exec(ctx, `DELETE FROM outlets WHERE id = $1`, f.outletID); err != nil {
			t.Error(err)
		}
	})

	mux := http.NewServeMux()
	New(testPool).Routes(mux)
	f.handler = mux
	return f
}

func (f *fixture) stock(productID int) int {
	f.t.Helper()
	var qty int
	err := testPool.QueryRow(context.Background(),
		`SELECT qty FROM stocks WHERE outlet_id = $1 AND product_id = $2`, f.outletID, productID).Scan(&qty)
	if err != nil {
		f.t.Fatal(err)
	}
	return qty
}

func (f *fixture) transactionCount() int {
	f.t.Helper()
	var n int
	err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM transactions WHERE outlet_id = $1`, f.outletID).Scan(&n)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

// do sends a request and decodes the JSON response body into a generic map.
// Safe to call from several goroutines.
func (f *fixture) do(method, path, key string, body any) (int, map[string]any) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			panic(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (f *fixture) checkout(key string, items ...lineItem) (int, map[string]any) {
	return f.do("POST", "/checkout", key, checkoutRequest{OutletID: f.outletID, CashierName: "Budi", Items: items})
}

func uuid4() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// 50 checkouts race for a product with 10 in stock. Exactly 10 may succeed,
// the other 40 must be refused, and stock ends at 0.
func TestConcurrentCheckoutNeverOversells(t *testing.T) {
	f := newFixture(t, 10)
	product := f.products[0]

	const requests = 50
	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, requests)
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release all goroutines at once
			codes[i], _ = f.checkout(uuid4(), lineItem{ProductID: product, Qty: 1})
		}()
	}
	close(start)
	wg.Wait()

	count := map[int]int{}
	for _, c := range codes {
		count[c]++
	}
	if count[http.StatusCreated] != 10 || count[http.StatusConflict] != 40 {
		t.Fatalf("want 10 x 201 and 40 x 409, got %v", count)
	}
	if got := f.stock(product); got != 0 {
		t.Fatalf("stock = %d, want 0", got)
	}
	if got := f.transactionCount(); got != 10 {
		t.Fatalf("transactions = %d, want 10", got)
	}
}

func TestCheckoutIsAllOrNothing(t *testing.T) {
	f := newFixture(t, 5, 2)
	plenty, scarce := f.products[0], f.products[1]

	code, body := f.checkout(uuid4(), lineItem{ProductID: plenty, Qty: 3}, lineItem{ProductID: scarce, Qty: 4})
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", code)
	}
	short := body["short_items"].([]any)
	if len(short) != 1 {
		t.Fatalf("short_items = %v, want only the scarce product", short)
	}
	item := short[0].(map[string]any)
	if int(item["product_id"].(float64)) != scarce || item["requested"].(float64) != 4 || item["available"].(float64) != 2 {
		t.Fatalf("short item = %v", item)
	}
	if f.stock(plenty) != 5 || f.stock(scarce) != 2 {
		t.Fatal("stock changed on a failed checkout")
	}
	if f.transactionCount() != 0 {
		t.Fatal("a failed checkout left a transaction")
	}
}

// Concurrent carts requesting the same products in opposite orders must lock in
// product_id order and never deadlock.
func TestConcurrentMultiItemOppositeOrderNeverDeadlocks(t *testing.T) {
	f := newFixture(t, 20, 20)
	a, b := f.products[0], f.products[1]

	const requests = 30
	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, requests)
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				codes[i], _ = f.checkout(uuid4(), lineItem{ProductID: a, Qty: 1}, lineItem{ProductID: b, Qty: 1})
			} else {
				codes[i], _ = f.checkout(uuid4(), lineItem{ProductID: b, Qty: 1}, lineItem{ProductID: a, Qty: 1})
			}
		}()
	}
	close(start)
	wg.Wait()

	count := map[int]int{}
	for _, c := range codes {
		count[c]++
	}
	if count[http.StatusCreated] != 20 || count[http.StatusConflict] != 10 {
		t.Fatalf("want 20 x 201 and 10 x 409, got %v", count)
	}
	if f.stock(a) != 0 || f.stock(b) != 0 || f.transactionCount() != 20 {
		t.Fatalf("stock=(%d,%d) transactions=%d, want (0,0) and 20", f.stock(a), f.stock(b), f.transactionCount())
	}
}

// Every short product (including one without a stock row) is reported together,
// and retrying the same Idempotency-Key succeeds once stock is available.
func TestCheckoutReportsAllShortItemsAndAllowsRetry(t *testing.T) {
	f := newFixture(t, 1, 2)
	p1, p2 := f.products[0], f.products[1]
	// Delete p2's stock row to verify missing stock rows report available=0.
	if _, err := testPool.Exec(context.Background(),
		`DELETE FROM stocks WHERE outlet_id = $1 AND product_id = $2`, f.outletID, p2); err != nil {
		t.Fatal(err)
	}

	key := uuid4()
	items := []lineItem{{ProductID: p1, Qty: 3}, {ProductID: p2, Qty: 1}}
	code, body := f.checkout(key, items...)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", code)
	}
	short := body["short_items"].([]any)
	if len(short) != 2 {
		t.Fatalf("short_items = %v, want 2 items", short)
	}
	first, second := short[0].(map[string]any), short[1].(map[string]any)
	if int(first["product_id"].(float64)) != p1 || first["available"].(float64) != 1 ||
		int(second["product_id"].(float64)) != p2 || second["available"].(float64) != 0 {
		t.Fatalf("short_items = %v", short)
	}

	// Replenish stock and retry with the exact same Idempotency-Key.
	if _, err := testPool.Exec(context.Background(),
		`UPDATE stocks SET qty = 5 WHERE outlet_id = $1 AND product_id = $2`, f.outletID, p1); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(),
		`INSERT INTO stocks (outlet_id, product_id, qty) VALUES ($1, $2, 5)`, f.outletID, p2); err != nil {
		t.Fatal(err)
	}
	if code, body := f.checkout(key, items...); code != http.StatusCreated {
		t.Fatalf("retry after 409: status = %d, body = %v, want 201", code, body)
	}
	if f.stock(p1) != 2 || f.stock(p2) != 4 || f.transactionCount() != 1 {
		t.Fatalf("after retry: stock=(%d,%d) tx=%d", f.stock(p1), f.stock(p2), f.transactionCount())
	}
}

func TestCheckoutTakesPriceFromDatabase(t *testing.T) {
	f := newFixture(t, 10)
	// "price" is not part of the request: an unknown field is rejected outright.
	code, _ := f.do("POST", "/checkout", uuid4(), map[string]any{
		"outlet_id": f.outletID, "cashier_name": "Budi",
		"items": []map[string]any{{"product_id": f.products[0], "qty": 2, "price": 1}},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a client-sent price", code)
	}
	code, body := f.checkout(uuid4(), lineItem{ProductID: f.products[0], Qty: 2})
	if code != http.StatusCreated || body["total"].(float64) != 3000 {
		t.Fatalf("status = %d, total = %v, want 201 and 3000", code, body["total"])
	}
}

func TestCheckoutRetryReturnsSameTransaction(t *testing.T) {
	f := newFixture(t, 10)
	key := uuid4()
	item := lineItem{ProductID: f.products[0], Qty: 2}

	code1, first := f.checkout(key, item)
	code2, second := f.checkout(key, item)
	if code1 != http.StatusCreated || code2 != http.StatusOK {
		t.Fatalf("statuses = %d, %d, want 201 then 200", code1, code2)
	}
	if first["id"] != second["id"] {
		t.Fatalf("replay returned transaction %v, want %v", second["id"], first["id"])
	}
	if f.stock(f.products[0]) != 8 || f.transactionCount() != 1 {
		t.Fatal("a replay changed stock or created a transaction")
	}

	code, _ := f.checkout(key, lineItem{ProductID: f.products[0], Qty: 3})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("same key, different body: status = %d, want 422", code)
	}
}

// A double click that slips past the UI: 20 identical requests, same key, at once.
func TestConcurrentSameKeyCreatesOneTransaction(t *testing.T) {
	f := newFixture(t, 100)
	key := uuid4()

	const requests = 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, requests)
	ids := make([]any, requests)
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var body map[string]any
			codes[i], body = f.checkout(key, lineItem{ProductID: f.products[0], Qty: 1})
			ids[i] = body["id"]
		}()
	}
	close(start)
	wg.Wait()

	created := 0
	for i, c := range codes {
		if c == http.StatusCreated {
			created++
		} else if c != http.StatusOK {
			t.Fatalf("request %d: status %d", i, c)
		}
		if ids[i] != ids[0] {
			t.Fatalf("request %d got transaction %v, want %v", i, ids[i], ids[0])
		}
	}
	if created != 1 || f.transactionCount() != 1 || f.stock(f.products[0]) != 99 {
		t.Fatalf("created = %d, transactions = %d, stock = %d; want 1, 1, 99",
			created, f.transactionCount(), f.stock(f.products[0]))
	}
}

func TestVoidRestoresStockOnce(t *testing.T) {
	f := newFixture(t, 10)
	_, tx := f.checkout(uuid4(), lineItem{ProductID: f.products[0], Qty: 3})
	path := fmt.Sprintf("/transactions/%d/void", int64(tx["id"].(float64)))

	// Ten voids at once: only one may give the stock back.
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, body := f.do("POST", path, "", nil); code != http.StatusOK || body["status"] != "VOID" {
				t.Errorf("void: status %d, body %v", code, body)
			}
		}()
	}
	wg.Wait()

	if got := f.stock(f.products[0]); got != 10 {
		t.Fatalf("stock = %d after voids, want 10", got)
	}
	if code, _ := f.do("POST", "/transactions/999999999/void", "", nil); code != http.StatusNotFound {
		t.Fatalf("unknown transaction: status %d, want 404", code)
	}
}

// A stock row held longer than lock_timeout: the checkout gives up with a
// retryable 503, writes nothing, and a retry with the same key succeeds.
func TestCheckoutReturns503WhenStockRowStaysLocked(t *testing.T) {
	f := newFixture(t, 10)
	ctx := context.Background()
	item := lineItem{ProductID: f.products[0], Qty: 1}

	blocker, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `UPDATE stocks SET qty = qty WHERE outlet_id = $1 AND product_id = $2`,
		f.outletID, item.ProductID); err != nil {
		t.Fatal(err)
	}

	key := uuid4()
	if code, body := f.checkout(key, item); code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body %v, want 503 while the row is locked", code, body)
	}
	if f.transactionCount() != 0 {
		t.Fatal("a timed-out checkout left a transaction")
	}

	blocker.Rollback(ctx)
	if code, _ := f.checkout(key, item); code != http.StatusCreated {
		t.Fatalf("retry with the same key: status = %d, want 201", code)
	}
	if f.stock(item.ProductID) != 9 || f.transactionCount() != 1 {
		t.Fatal("the retry did not record exactly one sale")
	}
}

func TestCheckoutMergesDuplicateProducts(t *testing.T) {
	f := newFixture(t, 10)
	p := f.products[0]

	code, body := f.checkout(uuid4(), lineItem{ProductID: p, Qty: 2}, lineItem{ProductID: p, Qty: 3})
	if code != http.StatusCreated {
		t.Fatalf("status = %d, body = %v, want 201", code, body)
	}
	items := body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["qty"].(float64) != 5 {
		t.Fatalf("items = %v, want single merged line of qty 5", items)
	}
	if f.stock(p) != 5 {
		t.Fatalf("stock = %d, want 5", f.stock(p))
	}
}

func TestCheckoutValidation(t *testing.T) {
	f := newFixture(t, 10)
	p := f.products[0]
	cases := []struct {
		name string
		key  string
		body any
		want int
	}{
		{"missing key", "", checkoutRequest{OutletID: f.outletID, CashierName: "Budi", Items: []lineItem{{p, 1}}}, 400},
		{"key not a uuid", "abc", checkoutRequest{OutletID: f.outletID, CashierName: "Budi", Items: []lineItem{{p, 1}}}, 400},
		{"no items", uuid4(), checkoutRequest{OutletID: f.outletID, CashierName: "Budi"}, 400},
		{"zero qty", uuid4(), checkoutRequest{OutletID: f.outletID, CashierName: "Budi", Items: []lineItem{{p, 0}}}, 400},
		{"blank cashier", uuid4(), checkoutRequest{OutletID: f.outletID, CashierName: "  ", Items: []lineItem{{p, 1}}}, 400},
		{"cashier with NUL", uuid4(), checkoutRequest{OutletID: f.outletID, CashierName: "Budi\x00", Items: []lineItem{{p, 1}}}, 400},
		{"cashier too long", uuid4(), checkoutRequest{OutletID: f.outletID, CashierName: strings.Repeat("é", maxCashierName+1), Items: []lineItem{{p, 1}}}, 400},
		{"outlet_id overflow", uuid4(), checkoutRequest{OutletID: math.MaxInt32 + 1, CashierName: "Budi", Items: []lineItem{{p, 1}}}, 400},
		{"product_id overflow", uuid4(), checkoutRequest{OutletID: f.outletID, CashierName: "Budi", Items: []lineItem{{math.MaxInt32 + 1, 1}}}, 400},
		{"merged qty exceeds max", uuid4(), checkoutRequest{OutletID: f.outletID, CashierName: "Budi", Items: []lineItem{{p, maxQtyPerItem}, {p, 1}}}, 400},
		{"wrong type", uuid4(), map[string]any{"outlet_id": "one"}, 400},
		{"unknown outlet", uuid4(), checkoutRequest{OutletID: 999999, CashierName: "Budi", Items: []lineItem{{p, 1}}}, 404},
		{"unknown product", uuid4(), checkoutRequest{OutletID: f.outletID, CashierName: "Budi", Items: []lineItem{{999999, 1}}}, 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := f.do("POST", "/checkout", c.key, c.body)
			if code != c.want {
				t.Fatalf("status = %d, want %d (body %v)", code, c.want, body)
			}
			if body["error"] == nil {
				t.Fatal("error response without an error message")
			}
		})
	}
	if f.stock(p) != 10 {
		t.Fatal("an invalid request changed stock")
	}

	// Multibyte cashier name at the rune limit (100 runes > 100 bytes) is accepted.
	code, body := f.do("POST", "/checkout", uuid4(), checkoutRequest{
		OutletID:    f.outletID,
		CashierName: "  " + strings.Repeat("é", maxCashierName) + "  ",
		Items:       []lineItem{{p, 1}},
	})
	if code != http.StatusCreated {
		t.Fatalf("100-rune cashier name: status = %d, body = %v, want 201", code, body)
	}
}
