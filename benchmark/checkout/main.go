// checkout measures what a migration's indexes cost checkout writes, by
// running the same concurrent checkouts with and without them.
//
// Every phase works on a fresh copy of pos (internal/benchdb), so both sides
// start from the same data and pos itself is never written:
//
//	before: drop the migration's indexes
//	after:  drop them, then apply the migration file again
//
// Phases alternate for -rounds rounds to spread out noise, and one report
// comparing the medians is written to benchmark/. pos must have no other open
// connections, since it is the template for the copies.
package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
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

type item struct {
	productID int32
	qty       int32
}

type cart struct {
	outletID int32
	items    []item // sorted by productID so concurrent carts lock stock rows in the same order
}

type settings struct {
	workers, n, warmup, items, rounds int
	seed                              uint64
}

// result is one measured phase.
type result struct {
	tps                     float64
	avg, p50, p95, p99, max time.Duration
	rejected                int64
}

func main() {
	repo := benchdb.RepoDir()
	migration := flag.String("migration", filepath.Join(repo, "migrations", "004_indexes2.sql"), "migration whose indexes are measured")
	workers := flag.Int("workers", 8, "concurrent connections, each running checkouts back to back")
	n := flag.Int("n", 5000, "measured checkouts per phase")
	warmup := flag.Int("warmup", 200, "unmeasured checkouts before measuring")
	items := flag.Int("items", 3, "distinct products per checkout")
	rounds := flag.Int("rounds", 3, "before/after pairs to run")
	seed := flag.Uint64("seed", 1, "random seed for the carts; every phase gets the same carts")
	dsn := flag.String("dsn", "postgres://postgres:postgres@localhost:5432/postgres?connect_timeout=10", "admin dsn; must not point at pos, which is copied")
	flag.Parse()

	s := settings{workers: *workers, n: *n, warmup: *warmup, items: *items, rounds: *rounds, seed: *seed}
	if err := run(*migration, *dsn, filepath.Join(repo, "benchmark"), s); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(migrationPath, dsn, outDir string, s settings) error {
	if s.workers < 1 || s.n < 1 || s.items < 1 || s.rounds < 1 {
		return fmt.Errorf("-workers, -n, -items and -rounds must be at least 1")
	}

	m, err := benchdb.LoadMigration(migrationPath)
	if err != nil {
		return err
	}

	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("cannot connect to database: %w", err)
	}
	defer admin.Close(ctx)
	defer benchdb.Drop(ctx, admin)

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return err
	}
	cfg.Database = benchdb.Name

	var before, after []result
	for r := 1; r <= s.rounds; r++ {
		for _, apply := range []bool{false, true} {
			name := map[bool]string{false: "before", true: "after"}[apply]
			fmt.Printf("round %d/%d, %s\n", r, s.rounds, name)
			res, err := phase(ctx, admin, cfg, m, apply, s)
			if err != nil {
				return fmt.Errorf("round %d %s: %w", r, name, err)
			}
			if apply {
				after = append(after, res)
			} else {
				before = append(before, res)
			}
		}
	}

	report := formatReport(m.File, m.Indexes, s, before, after)
	fmt.Print("\n" + report)

	stem := strings.TrimSuffix(m.File, ".sql")
	out := filepath.Join(outDir, fmt.Sprintf("bench_checkout_write_cost_%s_%s.txt", stem, time.Now().Format("20060102-150405")))
	if err := os.WriteFile(out, []byte(report), 0644); err != nil {
		return err
	}
	fmt.Printf("saved %s\n", out)
	return nil
}

// phase copies pos, sets up the indexes for one side of the comparison and
// measures checkouts on the copy, then drops it.
func phase(ctx context.Context, admin *pgx.Conn, cfg *pgx.ConnConfig, m benchdb.Migration, apply bool, s settings) (result, error) {
	if err := benchdb.Fresh(ctx, admin); err != nil {
		return result{}, err
	}
	setup, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return result{}, err
	}
	defer setup.Close(ctx)
	if err := m.Prepare(ctx, setup, apply); err != nil {
		return result{}, err
	}

	var maxOutlet, maxProduct int32
	if err := setup.QueryRow(ctx, "SELECT (SELECT max(id) FROM outlets), (SELECT max(id) FROM products)").Scan(&maxOutlet, &maxProduct); err != nil {
		return result{}, err
	}
	// Flush dirty pages so every phase starts measuring from the same state.
	if _, err := setup.Exec(ctx, "CHECKPOINT"); err != nil {
		return result{}, err
	}

	rng := rand.New(rand.NewPCG(s.seed, s.seed))
	carts := makeCarts(rng, s.warmup+s.n, s.items, maxOutlet, maxProduct)

	conns := make([]*pgx.Conn, s.workers)
	for i := range conns {
		c, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			return result{}, err
		}
		defer c.Close(ctx)
		conns[i] = c
	}

	if _, _, _, err := runPhase(ctx, conns, carts[:s.warmup]); err != nil {
		return result{}, err
	}
	durations, rejected, wall, err := runPhase(ctx, conns, carts[s.warmup:])
	if err != nil {
		return result{}, err
	}

	slices.Sort(durations)
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	return result{
		tps:      float64(s.n) / wall.Seconds(),
		avg:      total / time.Duration(s.n),
		p50:      percentile(durations, 0.50),
		p95:      percentile(durations, 0.95),
		p99:      percentile(durations, 0.99),
		max:      durations[len(durations)-1],
		rejected: rejected,
	}, nil
}

func formatReport(migration string, indexes []string, s settings, before, after []result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Checkout write cost of the indexes in migrations/%s\n", migration)
	fmt.Fprintf(&b, "date:     %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "setup:    %d workers, %d checkouts per phase (warmup %d), %d items each, seed %d\n", s.workers, s.n, s.warmup, s.items, s.seed)
	fmt.Fprintf(&b, "rounds:   %d, alternating before/after, each phase on a fresh copy of pos\n", s.rounds)
	fmt.Fprintf(&b, "before:   indexes below dropped\n")
	fmt.Fprintf(&b, "after:    migration applied\n")
	fmt.Fprintf(&b, "indexes:  %s\n\n", strings.Join(indexes, ", "))

	fmt.Fprintf(&b, "median of %d rounds\n", s.rounds)
	fmt.Fprintf(&b, "%-9s %12s %12s %9s\n", "", "before", "after", "change")
	tps := func(r result) float64 { return r.tps }
	fmt.Fprintf(&b, "%-9s %12.1f %12.1f %+8.1f%%\n", "tps", median(before, tps), median(after, tps), change(median(before, tps), median(after, tps)))
	for _, m := range []struct {
		name string
		get  func(result) time.Duration
	}{
		{"avg", func(r result) time.Duration { return r.avg }},
		{"p50", func(r result) time.Duration { return r.p50 }},
		{"p95", func(r result) time.Duration { return r.p95 }},
		{"p99", func(r result) time.Duration { return r.p99 }},
		{"max", func(r result) time.Duration { return r.max }},
	} {
		bv, av := median(before, m.get), median(after, m.get)
		fmt.Fprintf(&b, "%-9s %12s %12s %+8.1f%%\n", m.name, ms(bv), ms(av), change(float64(bv), float64(av)))
	}

	fmt.Fprintf(&b, "\nper round\n")
	fmt.Fprintf(&b, "%-11s %9s %9s %9s %9s %9s\n", "", "tps", "p50", "p95", "p99", "rejected")
	for i := range before {
		for _, x := range []struct {
			name string
			r    result
		}{{"before", before[i]}, {"after", after[i]}} {
			fmt.Fprintf(&b, "%d %-9s %9.1f %9s %9s %9s %9d\n", i+1, x.name, x.r.tps, ms(x.r.p50), ms(x.r.p95), ms(x.r.p99), x.r.rejected)
		}
	}
	fmt.Fprintf(&b, "\nrejected = checkouts refused for low stock; rolled back, still timed\n")
	return b.String()
}

func median[T cmp.Ordered](rs []result, get func(result) T) T {
	vs := make([]T, len(rs))
	for i, r := range rs {
		vs[i] = get(r)
	}
	slices.Sort(vs)
	return vs[len(vs)/2]
}

func change(before, after float64) float64 { return (after - before) / before * 100 }

func ms(d time.Duration) string { return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond)) }

func makeCarts(rng *rand.Rand, count, items int, maxOutlet, maxProduct int32) []cart {
	carts := make([]cart, count)
	for i := range carts {
		c := cart{outletID: rng.Int32N(maxOutlet) + 1}
		for len(c.items) < items {
			p := rng.Int32N(maxProduct) + 1
			if !slices.ContainsFunc(c.items, func(it item) bool { return it.productID == p }) {
				c.items = append(c.items, item{productID: p, qty: rng.Int32N(3) + 1})
			}
		}
		slices.SortFunc(c.items, func(a, b item) int { return cmp.Compare(a.productID, b.productID) })
		carts[i] = c
	}
	return carts
}

// runPhase hands carts to the workers and times each checkout.
func runPhase(ctx context.Context, conns []*pgx.Conn, carts []cart) ([]time.Duration, int64, time.Duration, error) {
	durations := make([]time.Duration, len(carts))
	var next, rejected atomic.Int64
	errs := make([]error, len(conns))
	var wg sync.WaitGroup

	start := time.Now()
	for w, conn := range conns {
		wg.Go(func() {
			for {
				i := int(next.Add(1) - 1)
				if i >= len(carts) {
					return
				}
				t := time.Now()
				ok, err := checkout(ctx, conn, carts[i])
				durations[i] = time.Since(t)
				if err != nil {
					errs[w] = err
					return
				}
				if !ok {
					rejected.Add(1)
				}
			}
		})
	}
	wg.Wait()
	wall := time.Since(start)

	for _, err := range errs {
		if err != nil {
			return nil, 0, 0, err
		}
	}
	return durations, rejected.Load(), wall, nil
}

// checkout runs the statements of one checkout in a single transaction.
// It returns false when a product has too little stock; the transaction is
// then rolled back with nothing written.
func checkout(ctx context.Context, conn *pgx.Conn, c cart) (bool, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	for _, it := range c.items {
		tag, err := tx.Exec(ctx,
			`UPDATE stocks SET qty = qty - $3 WHERE outlet_id = $1 AND product_id = $2 AND qty >= $3`,
			c.outletID, it.productID, it.qty)
		if err != nil {
			return false, err
		}
		if tag.RowsAffected() == 0 {
			return false, nil
		}
	}

	productIDs := make([]int32, len(c.items))
	qtys := make([]int32, len(c.items))
	for i, it := range c.items {
		productIDs[i] = it.productID
		qtys[i] = it.qty
	}

	// Prices come from the products table, as in the real checkout. The items
	// insert joins products again, so no price is passed in from Go.
	var txID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO transactions (outlet_id, cashier_name, status, total)
		SELECT $1, 'bench', 'PAID', sum(p.price * q.qty)
		FROM unnest($2::int[], $3::int[]) AS q(product_id, qty)
		JOIN products p ON p.id = q.product_id
		RETURNING id`,
		c.outletID, productIDs, qtys).Scan(&txID)
	if err != nil {
		return false, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO transaction_items (transaction_id, product_id, qty, price, subtotal)
		SELECT $1, p.id, q.qty, p.price, p.price * q.qty
		FROM unnest($2::int[], $3::int[]) AS q(product_id, qty)
		JOIN products p ON p.id = q.product_id`,
		txID, productIDs, qtys); err != nil {
		return false, err
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO idempotency_keys (key, request_hash, transaction_id) VALUES (gen_random_uuid(), 'bench', $1)`,
		txID); err != nil {
		return false, err
	}

	return true, tx.Commit(ctx)
}

// percentile uses the nearest-rank method on an ascending slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(idx, 0)]
}
