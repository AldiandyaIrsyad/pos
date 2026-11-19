// topproducts times the top-products query in three variants:
//
//  1. legacy query, without the migration's indexes
//  2. rewritten query, without the indexes
//  3. rewritten query, with the migration applied
//
// Each variant runs on a fresh copy of pos: one EXPLAIN (ANALYZE, BUFFERS),
// which also warms the cache, then timed runs that read every row. One report
// with a summary and the three plans is written to benchmark/. pos must have
// no other open connections, since it is the template for the copies.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AldiandyaIrsyad/benchmark/internal/benchdb"
)

type variant struct {
	name    string
	sqlFile string
	apply   bool // migration indexes present
	runs    int
}

type result struct {
	variant
	benchdb.Result
}

func main() {
	repo := benchdb.RepoDir()
	migration := flag.String("migration", filepath.Join(repo, "migrations", "004_indexes2.sql"), "migration whose indexes variant 3 adds")
	legacy := flag.String("legacy", filepath.Join(repo, "benchmark", "legacy.sql"), "legacy query")
	proposed := flag.String("proposed", filepath.Join(repo, "benchmark", "proposed.sql"), "rewritten query")
	n := flag.Int("n", 10, "timed runs of the rewritten query, per variant")
	legacyN := flag.Int("legacy-n", 3, "timed runs of the legacy query (about 2.5 minutes each)")
	dsn := flag.String("dsn", "postgres://postgres:postgres@localhost:5432/postgres?connect_timeout=10", "admin dsn; must not point at pos, which is copied")
	flag.Parse()

	variants := []variant{
		{"1 legacy query, no index", *legacy, false, *legacyN},
		{"2 rewritten query, no index", *proposed, false, *n},
		{"3 rewritten query + index", *proposed, true, *n},
	}
	if err := run(*migration, *dsn, filepath.Join(repo, "benchmark"), variants); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(migrationPath, dsn, outDir string, variants []variant) error {
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

	var results []result
	for _, s := range variants {
		if s.runs < 1 {
			return fmt.Errorf("%s: runs must be at least 1", s.name)
		}
		fmt.Printf("variant %s\n", s.name)
		r, err := measure(ctx, admin, cfg, m, s)
		if err != nil {
			return fmt.Errorf("variant %s: %w", s.name, err)
		}
		results = append(results, r)
	}

	report := formatReport(m, results)
	fmt.Print("\n" + report)
	out := filepath.Join(outDir, fmt.Sprintf("bench_top_products_%s.txt", time.Now().Format("20060102-150405")))
	if err := os.WriteFile(out, []byte(report), 0644); err != nil {
		return err
	}
	fmt.Printf("saved %s\n", out)
	return nil
}

func measure(ctx context.Context, admin *pgx.Conn, cfg *pgx.ConnConfig, m benchdb.Migration, s variant) (result, error) {
	raw, err := os.ReadFile(s.sqlFile)
	if err != nil {
		return result{}, err
	}
	query := strings.TrimSpace(string(raw))

	if err := benchdb.Fresh(ctx, admin); err != nil {
		return result{}, err
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return result{}, err
	}
	defer conn.Close(ctx)
	if err := m.Prepare(ctx, conn, s.apply); err != nil {
		return result{}, err
	}

	res, err := benchdb.Measure(ctx, conn, query, s.runs)
	return result{variant: s, Result: res}, err
}

func formatReport(m benchdb.Migration, results []result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Top products query: legacy, rewrite, rewrite + index\n")
	fmt.Fprintf(&b, "date:     %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "index:    migrations/%s (%s)\n", m.File, strings.Join(m.Indexes, ", "))
	fmt.Fprintf(&b, "method:   each variant on a fresh copy of pos; cold EXPLAIN (ANALYZE, BUFFERS),\n")
	fmt.Fprintf(&b, "          timed warm runs reading every row, then warm EXPLAIN (ANALYZE, BUFFERS)\n")
	fmt.Fprintf(&b, "pages:    cold = cold EXPLAIN (hit+read); warm = warm EXPLAIN (hit+read)\n\n")

	fmt.Fprintf(&b, "%-29s %5s %10s %10s %10s %10s %12s %12s %10s\n", "variant", "runs", "min", "avg", "p50", "p95", "cold_read", "warm_pages", "vs 1")
	base := results[0].Avg
	for _, r := range results {
		fmt.Fprintf(&b, "%-29s %5d %10s %10s %10s %10s %12d %12d %9.0fx\n",
			r.name, r.runs, benchdb.Short(r.Min), benchdb.Short(r.Avg), benchdb.Short(r.P50), benchdb.Short(r.P95),
			r.ColdRead, r.WarmHit+r.WarmRead, float64(base)/float64(r.Avg))
	}
	fmt.Fprintf(&b, "\nqueries:  %s, %s\n", filepath.Base(results[0].sqlFile), filepath.Base(results[1].sqlFile))

	for _, r := range results {
		fmt.Fprintf(&b, "\n=== %s (%s) | cold hit=%d read=%d | warm hit=%d read=%d\n%s\n",
			r.name, filepath.Base(r.sqlFile), r.ColdHit, r.ColdRead, r.WarmHit, r.WarmRead, r.Plan)
	}
	return b.String()
}
