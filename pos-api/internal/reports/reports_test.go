package reports

// Runs against a real, migrated Postgres:
//
//	DATABASE_URL=postgres://postgres:postgres@localhost:5432/pos go test ./...
//
// Each test creates its own outlet, products and transactions and deletes them
// afterwards.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
	var err error
	testPool, err = pgxpool.New(context.Background(), dsn)
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

func newFixture(t *testing.T, productCount int) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{t: t}
	if err := testPool.QueryRow(ctx,
		`INSERT INTO outlets (code, name) VALUES ('TEST', 'Test outlet') RETURNING id`).Scan(&f.outletID); err != nil {
		t.Fatal(err)
	}
	for i := range productCount {
		var id int
		if err := testPool.QueryRow(ctx,
			`INSERT INTO products (sku, name, category, price) VALUES ($1, $2, 'Test', 1000) RETURNING id`,
			fmt.Sprintf("TEST-%d-%d", f.outletID, i), fmt.Sprintf("Test product %d", i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		f.products = append(f.products, id)
	}
	t.Cleanup(func() {
		for _, sql := range []string{
			`DELETE FROM transaction_items WHERE transaction_id IN (SELECT id FROM transactions WHERE outlet_id = $1)`,
			`DELETE FROM transactions WHERE outlet_id = $1`,
			`DELETE FROM outlets WHERE id = $1`,
		} {
			if _, err := testPool.Exec(ctx, sql, f.outletID); err != nil {
				t.Error(err)
			}
		}
		if _, err := testPool.Exec(ctx, `DELETE FROM products WHERE id = ANY($1)`, f.products); err != nil {
			t.Error(err)
		}
	})
	mux := http.NewServeMux()
	New(testPool).Routes(mux)
	f.handler = mux
	return f
}

type line struct{ product, qty, price int }

// sale inserts a transaction at createdAt (any Postgres timestamptz literal).
func (f *fixture) sale(createdAt, status string, lines ...line) {
	f.t.Helper()
	ctx := context.Background()
	total := 0
	for _, l := range lines {
		total += l.qty * l.price
	}
	var id int64
	if err := testPool.QueryRow(ctx,
		`INSERT INTO transactions (outlet_id, cashier_name, status, total, created_at)
		 VALUES ($1, 'test', $2, $3, $4::timestamptz) RETURNING id`,
		f.outletID, status, total, createdAt).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	for _, l := range lines {
		if _, err := testPool.Exec(ctx,
			`INSERT INTO transaction_items (transaction_id, product_id, qty, price, subtotal) VALUES ($1, $2, $3, $4, $5)`,
			id, f.products[l.product], l.qty, l.price, l.qty*l.price); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fixture) get(path string, out any) int {
	f.t.Helper()
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	if out != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			f.t.Fatal(err)
		}
	}
	return rec.Code
}

// Days are WIB calendar days, VOID is left out, and the average is per transaction.
func TestDailySales(t *testing.T) {
	f := newFixture(t, 1)
	f.sale("2026-01-01 00:30+07", "PAID", line{0, 1, 1000}) // 2025-12-31 in UTC, 1 Jan in WIB
	f.sale("2026-01-01 21:00+07", "PAID", line{0, 2, 1000})
	f.sale("2026-01-01 12:00+07", "VOID", line{0, 9, 1000})
	f.sale("2026-01-02 10:00+07", "PAID", line{0, 5, 1000})
	f.sale("2026-01-03 00:00+07", "PAID", line{0, 1, 1000}) // after "to"

	var rows []dailyRow
	code := f.get(fmt.Sprintf("/reports/daily-sales?outlet_id=%d&from=2026-01-01&to=2026-01-02", f.outletID), &rows)
	want := []dailyRow{
		{Date: "2026-01-01", Transactions: 2, Revenue: 3000, Average: 1500},
		{Date: "2026-01-02", Transactions: 1, Revenue: 5000, Average: 5000},
	}
	if code != http.StatusOK || fmt.Sprint(rows) != fmt.Sprint(want) {
		t.Fatalf("status %d, rows %+v, want %+v", code, rows, want)
	}
}

// Ranked by qty or by revenue, VOID left out, limit applied.
func TestTopProducts(t *testing.T) {
	f := newFixture(t, 3)
	f.sale("2026-01-01 10:00+07", "PAID", line{0, 5, 1000}, line{1, 3, 4000})
	f.sale("2026-01-02 10:00+07", "PAID", line{2, 1, 500})
	f.sale("2026-01-02 11:00+07", "VOID", line{2, 50, 500})

	path := fmt.Sprintf("/reports/top-products?outlet_id=%d&from=2026-01-01&to=2026-01-31", f.outletID)
	var byQty, byRevenue, top1 []topRow
	f.get(path, &byQty)
	f.get(path+"&sort=revenue", &byRevenue)
	f.get(path+"&limit=1", &top1)

	if len(byQty) != 3 || byQty[0].ProductID != f.products[0] || byQty[0].QtySold != 5 || byQty[2].QtySold != 1 {
		t.Fatalf("by qty = %+v, want product 0 (qty 5) first and the voided 50 left out", byQty)
	}
	if byRevenue[0].ProductID != f.products[1] || byRevenue[0].Revenue != 12000 {
		t.Fatalf("by revenue = %+v, want product 1 (12000) first", byRevenue)
	}
	if len(top1) != 1 {
		t.Fatalf("limit=1 returned %d rows", len(top1))
	}
}

func TestReportValidation(t *testing.T) {
	f := newFixture(t, 0)
	base := fmt.Sprintf("outlet_id=%d", f.outletID)
	for _, path := range []string{
		"/reports/daily-sales?from=2026-01-01&to=2026-01-31",
		"/reports/daily-sales?outlet_id=2147483648&from=2026-01-01&to=2026-01-31",
		"/reports/daily-sales?" + base + "&from=2026-13-01&to=2026-01-31",
		"/reports/daily-sales?" + base + "&from=2026-02-01&to=2026-01-31",
		"/reports/daily-sales?" + base + "&from=2025-01-01&to=2026-01-02",
		"/reports/top-products?" + base + "&from=2026-01-01&to=2026-01-31&sort=price",
		"/reports/top-products?" + base + "&from=2026-01-01&to=2026-01-31&limit=0",
	} {
		if code := f.get(path, nil); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", path, code)
		}
	}
}
