package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	sqlpath := flag.String("filepath", "", "sql file to run") // required
	n := flag.Int("n", 10, "measured runs")
	warmup := flag.Int("warmup", 1, "unmeasured runs before measuring")
	dsn := flag.String("dsn", "postgres://postgres:postgres@localhost:5432/pos?connect_timeout=10", "dsn")

	flag.Parse()

	if err := run(*sqlpath, *dsn, *n, *warmup); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(sqlpath, dsn string, n, warmup int) error {
	if sqlpath == "" {
		return fmt.Errorf("Missing required flag: -filepath")
	}
	if n < 1 {
		return fmt.Errorf("-n must be at least 1")
	}

	rawSQL, err := os.ReadFile(sqlpath)
	if err != nil {
		return fmt.Errorf("Failed to read SQL file %w", err)
	}
	query := strings.TrimSpace(string(rawSQL))

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("Cannot connect to database %w", err)
	}
	defer conn.Close(ctx)

	for i := range warmup {
		d, err := timeQuery(ctx, conn, query)
		if err != nil {
			return err
		}
		fmt.Printf("warmup %d: %s\n", i+1, d)
	}

	durations := make([]time.Duration, n)
	for i := range n {
		d, err := timeQuery(ctx, conn, query)
		if err != nil {
			return err
		}
		durations[i] = d
		fmt.Printf("run %d/%d: %s\n", i+1, n, d)
	}

	// report
	sorted := slices.Clone(durations)
	slices.Sort(sorted)
	var total time.Duration
	for _, d := range durations {
		total += d
	}

	var out strings.Builder
	fmt.Fprintf(&out, "file:   %s\n", sqlpath)
	fmt.Fprintf(&out, "date:   %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&out, "runs:   %d (warmup %d)\n\n", n, warmup)
	fmt.Fprintf(&out, "min:    %s\n", sorted[0])
	fmt.Fprintf(&out, "avg:    %s\n", total/time.Duration(n))
	fmt.Fprintf(&out, "p50:    %s\n", percentile(sorted, 0.50))
	fmt.Fprintf(&out, "p95:    %s\n", percentile(sorted, 0.95))
	fmt.Fprintf(&out, "max:    %s\n\n", sorted[n-1])
	for i, d := range durations {
		fmt.Fprintf(&out, "run %d: %s\n", i+1, d)
	}
	fmt.Print("\n" + out.String())

	// always save into benchmark/, regardless of the working directory
	base := filepath.Base(sqlpath)
	fname := strings.TrimSuffix(base, filepath.Ext(base))
	_, thisFile, _, _ := runtime.Caller(0)
	benchDir := filepath.Dir(filepath.Dir(thisFile))
	outFileName := filepath.Join(benchDir, fmt.Sprintf("bench_%s_%s.txt", fname, time.Now().Format("060102150405")))

	if err := os.WriteFile(outFileName, []byte(out.String()), 0644); err != nil {
		return fmt.Errorf("Failed to write output: %w", err)
	}

	fmt.Printf("Saved %s\n", outFileName)
	return nil
}

// timeQuery measures one execution, including reading every row,
// as an API handler would.
func timeQuery(ctx context.Context, conn *pgx.Conn, query string) (time.Duration, error) {
	start := time.Now()
	rows, err := conn.Query(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("Query failed %w", err)
	}
	for rows.Next() {
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("Rows error: %w", err)
	}
	return time.Since(start), nil
}

// percentile uses the nearest-rank method on an ascending slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(idx, 0)]
}
