package catalog

// Runs against a real, migrated Postgres:
//
//	DATABASE_URL=postgres://postgres:postgres@localhost:5432/pos go test ./...
//
// Each test creates its own outlet and products and deletes them afterwards.

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

type testProduct struct {
	name, category string
	stock          int // -1: no stock row at the outlet
}

// newFixture creates an outlet and products. Names contain "Zebrakopi", which
// no seed product does, so searches see only these.
func newFixture(t *testing.T, items ...testProduct) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{t: t}
	if err := testPool.QueryRow(ctx,
		`INSERT INTO outlets (code, name) VALUES ('TEST', 'Test outlet') RETURNING id`).Scan(&f.outletID); err != nil {
		t.Fatal(err)
	}
	for i, it := range items {
		var id int
		if err := testPool.QueryRow(ctx,
			`INSERT INTO products (sku, name, category, price) VALUES ($1, $2, $3, 1500) RETURNING id`,
			fmt.Sprintf("TEST-%d-%d", f.outletID, i), it.name, it.category).Scan(&id); err != nil {
			t.Fatal(err)
		}
		f.products = append(f.products, id)
		if it.stock >= 0 {
			if _, err := testPool.Exec(ctx, `INSERT INTO stocks (outlet_id, product_id, qty) VALUES ($1, $2, $3)`,
				f.outletID, id, it.stock); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() {
		for _, sql := range []string{
			`DELETE FROM stocks WHERE outlet_id = $1`,
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

func (f *fixture) get(path string) (int, productPage) {
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	var page productPage
	json.Unmarshal(rec.Body.Bytes(), &page)
	return rec.Code, page
}

func (f *fixture) search(query string) (int, productPage) {
	return f.get(fmt.Sprintf("/products?outlet_id=%d&%s", f.outletID, query))
}

func names(page productPage) []string {
	var out []string
	for _, p := range page.Items {
		out = append(out, p.Name)
	}
	return out
}

func TestSearchByNameAndSKUWithOutletStock(t *testing.T) {
	f := newFixture(t,
		testProduct{"Zebrakopi Susu", "Minuman", 7},
		testProduct{"Zebrakopi Hitam", "Minuman", 0},
		testProduct{"Zebrakopi Tanpa Stok", "Minuman", -1},
	)

	code, page := f.search("q=ZEBRAKOPI")
	if code != http.StatusOK || len(page.Items) != 2 {
		t.Fatalf("status %d, items %v: want 200 and the 2 products with a stock row", code, names(page))
	}
	if page.Items[0].Name != "Zebrakopi Susu" || page.Items[0].Stock != 7 || page.Items[1].Stock != 0 {
		t.Fatalf("items = %+v, want stock 7 and 0 for this outlet", page.Items)
	}

	sku := fmt.Sprintf("TEST-%d-1", f.outletID)
	if _, page := f.search("q=" + sku); len(page.Items) != 1 || page.Items[0].SKU != sku {
		t.Fatalf("search by SKU %s = %v", sku, names(page))
	}
}

func TestSearchFiltersPagesAndEscapes(t *testing.T) {
	f := newFixture(t,
		testProduct{"Zebrakopi A", "Minuman", 1},
		testProduct{"Zebrakopi B", "Snack", 1},
		testProduct{"Zebrakopi C", "Minuman", 1},
		testProduct{"Zebrakopi 100%", "Minuman", 1},
	)

	if _, page := f.search("q=zebrakopi&category=Snack"); len(page.Items) != 1 || page.Items[0].Name != "Zebrakopi B" {
		t.Fatalf("category filter = %v", names(page))
	}

	_, first := f.search("q=zebrakopi&limit=2")
	if len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatalf("first page = %v, cursor %v: want 2 items and a cursor", names(first), first.NextCursor)
	}
	_, second := f.search("q=zebrakopi&limit=2&cursor=" + *first.NextCursor)
	if len(second.Items) != 2 || second.NextCursor != nil || second.Items[0].ID <= first.Items[1].ID {
		t.Fatalf("second page = %v, cursor %v: want the next 2 items and no cursor", names(second), second.NextCursor)
	}

	// "%" in the search text matches a literal percent sign, not "anything".
	if _, page := f.search("q=100%25"); len(page.Items) != 1 || page.Items[0].Name != "Zebrakopi 100%" {
		t.Fatalf("q=100%% = %v", names(page))
	}
}

func TestSearchValidation(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{
		"/products",
		"/products?outlet_id=abc",
		"/products?outlet_id=2147483648",
		fmt.Sprintf("/products?outlet_id=%d&limit=0", f.outletID),
		fmt.Sprintf("/products?outlet_id=%d&limit=101", f.outletID),
		fmt.Sprintf("/products?outlet_id=%d&cursor=-1", f.outletID),
		fmt.Sprintf("/products?outlet_id=%d&cursor=2147483648", f.outletID),
		fmt.Sprintf("/products?outlet_id=%d&q=kopi%%00", f.outletID),
		fmt.Sprintf("/products?outlet_id=%d&category=snack%%00", f.outletID),
	} {
		if code, _ := f.get(path); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", path, code)
		}
	}
}
