// hotproduct load-tests checkout through the HTTP API on one product that
// every cashier is buying at once: the worst case for stock row locks.
//
// It creates its own outlet and product in pos with -stock units, sends
// -requests checkouts of qty 1 from -concurrency clients, then checks the database:
// stock must equal the starting stock minus the 201s, never below zero, with
// one transaction per 201. Everything it created is deleted afterwards. The
// report is written to benchmark/.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AldiandyaIrsyad/benchmark/internal/benchdb"
)

type settings struct {
	api, dsn                     string
	requests, concurrency, stock int
}

type fixture struct {
	outletID, productID int
}

func main() {
	var s settings
	flag.StringVar(&s.api, "api", "http://localhost:8080", "pos-api base URL")
	flag.StringVar(&s.dsn, "dsn", "postgres://postgres:postgres@localhost:5432/pos?connect_timeout=10", "the pos database the API uses, for setup, checks and cleanup")
	flag.IntVar(&s.requests, "requests", 1000, "checkouts to send")
	flag.IntVar(&s.concurrency, "concurrency", 50, "clients sending at the same time")
	flag.IntVar(&s.stock, "stock", 800, "starting stock; fewer than -requests so the end of the run also proves no oversell")
	flag.Parse()

	if err := run(s); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(s settings) error {
	if s.requests < 1 || s.concurrency < 1 || s.stock < 0 {
		return fmt.Errorf("-requests and -concurrency must be at least 1, -stock at least 0")
	}
	ctx := context.Background()
	db, err := pgx.Connect(ctx, s.dsn)
	if err != nil {
		return fmt.Errorf("cannot connect to database: %w", err)
	}
	defer db.Close(ctx)

	f, err := createFixture(ctx, db, s.stock)
	if err != nil {
		return err
	}
	defer func() {
		if err := f.cleanup(ctx, db); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup of outlet %d failed: %v\n", f.outletID, err)
		}
	}()

	fmt.Printf("sending %d checkouts from %d clients to product %d (stock %d)\n", s.requests, s.concurrency, f.productID, s.stock)
	durations, codes, wall, err := fire(s, f)
	if err != nil {
		return err
	}

	var finalStock, txCount int
	if err := db.QueryRow(ctx, `SELECT qty FROM stocks WHERE outlet_id = $1 AND product_id = $2`, f.outletID, f.productID).Scan(&finalStock); err != nil {
		return err
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM transactions WHERE outlet_id = $1`, f.outletID).Scan(&txCount); err != nil {
		return err
	}

	report := formatReport(s, durations, codes, wall, finalStock, txCount)
	fmt.Print("\n" + report)
	out := filepath.Join(benchdb.RepoDir(), "benchmark", fmt.Sprintf("bench_hot_product_%s.txt", time.Now().Format("20060102-150405")))
	if err := os.WriteFile(out, []byte(report), 0644); err != nil {
		return err
	}
	fmt.Printf("saved %s\n", out)
	return nil
}

func createFixture(ctx context.Context, db *pgx.Conn, stock int) (fixture, error) {
	var f fixture
	if err := db.QueryRow(ctx, `INSERT INTO outlets (code, name) VALUES ('BENCH', 'Hot product benchmark') RETURNING id`).Scan(&f.outletID); err != nil {
		return f, err
	}
	if err := db.QueryRow(ctx,
		`INSERT INTO products (sku, name, category, price) VALUES ($1, 'Hot product', 'Bench', 1500) RETURNING id`,
		fmt.Sprintf("BENCH-%d", f.outletID)).Scan(&f.productID); err != nil {
		return f, err
	}
	_, err := db.Exec(ctx, `INSERT INTO stocks (outlet_id, product_id, qty) VALUES ($1, $2, $3)`, f.outletID, f.productID, stock)
	return f, err
}

// cleanup deletes children before parents because of the foreign keys.
func (f fixture) cleanup(ctx context.Context, db *pgx.Conn) error {
	for _, sql := range []string{
		`DELETE FROM idempotency_keys WHERE transaction_id IN (SELECT id FROM transactions WHERE outlet_id = $1)`,
		`DELETE FROM transaction_items WHERE transaction_id IN (SELECT id FROM transactions WHERE outlet_id = $1)`,
		`DELETE FROM transactions WHERE outlet_id = $1`,
		`DELETE FROM stocks WHERE outlet_id = $1`,
		`DELETE FROM outlets WHERE id = $1`,
	} {
		if _, err := db.Exec(ctx, sql, f.outletID); err != nil {
			return err
		}
	}
	_, err := db.Exec(ctx, `DELETE FROM products WHERE id = $1`, f.productID)
	return err
}

// fire sends the checkouts and returns each latency and the count per status code.
func fire(s settings, f fixture) ([]time.Duration, map[int]int, time.Duration, error) {
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{MaxIdleConnsPerHost: s.concurrency},
	}
	body, err := json.Marshal(map[string]any{
		"outlet_id": f.outletID, "cashier_name": "bench",
		"items": []map[string]int{{"product_id": f.productID, "qty": 1}},
	})
	if err != nil {
		return nil, nil, 0, err
	}

	durations := make([]time.Duration, s.requests)
	statuses := make([]int, s.requests)
	errs := make([]error, s.concurrency)
	var next atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for c := range s.concurrency {
		wg.Go(func() {
			for {
				i := int(next.Add(1) - 1)
				if i >= s.requests {
					return
				}
				req, err := http.NewRequest("POST", s.api+"/checkout", bytes.NewReader(body))
				if err != nil {
					errs[c] = err
					return
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", uuid4())
				t := time.Now()
				resp, err := client.Do(req)
				if err != nil {
					errs[c] = err
					return
				}
				resp.Body.Close()
				durations[i], statuses[i] = time.Since(t), resp.StatusCode
			}
		})
	}
	wg.Wait()
	wall := time.Since(start)
	for _, err := range errs {
		if err != nil {
			return nil, nil, 0, err
		}
	}
	codes := map[int]int{}
	for _, c := range statuses {
		codes[c]++
	}
	return durations, codes, wall, nil
}

func formatReport(s settings, durations []time.Duration, codes map[int]int, wall time.Duration, finalStock, txCount int) string {
	slices.Sort(durations)
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	created := codes[http.StatusCreated]

	var b strings.Builder
	fmt.Fprintf(&b, "Checkout load test: one hot product, through the HTTP API\n")
	fmt.Fprintf(&b, "date:        %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "api:         %s\n", s.api)
	fmt.Fprintf(&b, "setup:       %d checkouts of qty 1 from %d concurrent clients, one product, starting stock %d\n", s.requests, s.concurrency, s.stock)
	fmt.Fprintf(&b, "             each request has its own Idempotency-Key; fixture created and deleted by the tool\n\n")

	fmt.Fprintf(&b, "status\n")
	for _, c := range slices.Sorted(maps.Keys(codes)) {
		fmt.Fprintf(&b, "  %d %-22s %6d\n", c, http.StatusText(c), codes[c])
	}

	fmt.Fprintf(&b, "\nthroughput  %.1f requests/s (wall %s)\n", float64(s.requests)/wall.Seconds(), wall.Round(time.Millisecond))
	fmt.Fprintf(&b, "latency     min %s  avg %s  p50 %s  p95 %s  p99 %s  max %s\n",
		ms(durations[0]), ms(total/time.Duration(len(durations))), ms(percentile(durations, 0.50)),
		ms(percentile(durations, 0.95)), ms(percentile(durations, 0.99)), ms(durations[len(durations)-1]))

	fmt.Fprintf(&b, "\nchecks\n")
	check := func(ok bool, msg string, args ...any) {
		mark := "PASS"
		if !ok {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "  %s  %s\n", mark, fmt.Sprintf(msg, args...))
	}
	check(finalStock >= 0, "final stock %d is not negative", finalStock)
	check(finalStock == s.stock-created, "final stock %d = starting %d - %d sold", finalStock, s.stock, created)
	check(txCount == created, "%d transactions recorded for %d x 201", txCount, created)
	check(created+codes[http.StatusConflict]+codes[http.StatusServiceUnavailable] == s.requests, "every request answered 201, 409 or 503")
	check(codes[http.StatusConflict] == 0 || finalStock == 0, "409 only once stock ran out (final stock %d)", finalStock)
	return b.String()
}

func uuid4() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond)) }

// percentile uses the nearest-rank method on an ascending slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(idx, 0)]
}
