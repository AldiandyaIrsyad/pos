// beforeafter measures one query without and with a migration's indexes.
//
// Each side runs on a fresh copy of pos (internal/benchdb): one
// EXPLAIN (ANALYZE, BUFFERS), then timed runs. One report with a summary and
// both plans is written to benchmark/. pos must have no other open
// connections, since it is the template for the copies.
//
//	go run ./benchmark/beforeafter -sql benchmark/daily_sales.sql -migration migrations/004_indexes2.sql
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

func main() {
	sqlPath := flag.String("sql", "", "query to measure (required)")
	migration := flag.String("migration", "", "migration whose indexes make the difference (required)")
	n := flag.Int("n", 20, "timed runs per side")
	dsn := flag.String("dsn", "postgres://postgres:postgres@localhost:5432/postgres?connect_timeout=10", "admin dsn; must not point at pos, which is copied")
	flag.Parse()

	if err := run(*sqlPath, *migration, *dsn, *n); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(sqlPath, migrationPath, dsn string, n int) error {
	if sqlPath == "" || migrationPath == "" || n < 1 {
		return fmt.Errorf("-sql and -migration are required, -n must be at least 1")
	}
	raw, err := os.ReadFile(sqlPath)
	if err != nil {
		return err
	}
	query := strings.TrimSpace(string(raw))
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

	var results [2]benchdb.Result
	for i, apply := range []bool{false, true} {
		fmt.Printf("%s\n", map[bool]string{false: "before", true: "after"}[apply])
		if err := benchdb.Fresh(ctx, admin); err != nil {
			return err
		}
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			return err
		}
		err = m.Prepare(ctx, conn, apply)
		if err == nil {
			results[i], err = benchdb.Measure(ctx, conn, query, n)
		}
		conn.Close(ctx)
		if err != nil {
			return err
		}
	}

	sqlName := strings.TrimSuffix(filepath.Base(sqlPath), ".sql")
	var b strings.Builder
	fmt.Fprintf(&b, "%s: before and after migrations/%s\n", sqlName, m.File)
	fmt.Fprintf(&b, "date:     %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "query:    benchmark/%s\n", filepath.Base(sqlPath))
	fmt.Fprintf(&b, "before:   %s dropped\n", strings.Join(m.Indexes, ", "))
	fmt.Fprintf(&b, "after:    migration applied\n")
	fmt.Fprintf(&b, "method:   each side on a fresh copy of pos; cold EXPLAIN (ANALYZE, BUFFERS),\n")
	fmt.Fprintf(&b, "          %d timed warm runs reading every row, then warm EXPLAIN (ANALYZE, BUFFERS)\n\n", n)
	fmt.Fprintf(&b, "%-8s %10s %10s %10s %10s %10s %10s %10s %10s\n", "", "min", "avg", "p50", "p95", "cold_hit", "cold_read", "warm_hit", "warm_read")
	for i, name := range []string{"before", "after"} {
		r := results[i]
		fmt.Fprintf(&b, "%-8s %10s %10s %10s %10s %10d %10d %10d %10d\n", name,
			benchdb.Short(r.Min), benchdb.Short(r.Avg), benchdb.Short(r.P50), benchdb.Short(r.P95),
			r.ColdHit, r.ColdRead, r.WarmHit, r.WarmRead)
	}
	fmt.Fprintf(&b, "\ncold_hit/cold_read = first EXPLAIN before cache warmup; warm_hit/warm_read = EXPLAIN after warm runs (touches, not distinct pages)\n")
	fmt.Fprintf(&b, "\n=== query\n%s\n", query)
	fmt.Fprintf(&b, "\n=== before (cold EXPLAIN)\n%s\n", results[0].Plan)
	fmt.Fprintf(&b, "\n=== after (cold EXPLAIN)\n%s\n", results[1].Plan)

	report := b.String()
	fmt.Print("\n" + report)
	out := filepath.Join(benchdb.RepoDir(), "benchmark",
		fmt.Sprintf("bench_%s_%s_%s.txt", sqlName, strings.TrimSuffix(m.File, ".sql"), time.Now().Format("20060102-150405")))
	if err := os.WriteFile(out, []byte(report), 0644); err != nil {
		return err
	}
	fmt.Printf("saved %s\n", out)
	return nil
}
