package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	sqlpath := flag.String("filepath", "", "sql file to run") // required

	dsn := flag.String("dsn", "postgres://postgres:postgres@localhost:5432/pos?connect_timeout=10", "dsn")

	flag.Parse()

	if err := run(*sqlpath, *dsn); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

}

func run(sqlpath, dsn string) error {

	if sqlpath == "" {
		return fmt.Errorf("Missing required flag: -filepath")
	}

	rawSQL, err := os.ReadFile(sqlpath)
	if err != nil {
		return fmt.Errorf("Failed to read SQL file %w", err)
	}

	// clean query
	query := strings.TrimSpace(string(rawSQL))
	query = "EXPLAIN (ANALYZE, BUFFERS) " + query

	// create connection
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("Cannot connect to database %w", err)
	}

	defer conn.Close(ctx)

	// run
	rows, err := conn.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("Query failed %w", err)
	}

	defer rows.Close()

	// convert to string
	var out strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("Scan failed %w", err)
		}

		out.WriteString(line)
		out.WriteByte('\n')
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("Rows error: %w", err)
	}

	// output filename
	base := filepath.Base(sqlpath)
	fname := strings.TrimSuffix(base, filepath.Ext(base))
	// Mon Jan 2 15:04:05 MST 2006
	// always save into benchmark/, regardless of the working directory
	_, thisFile, _, _ := runtime.Caller(0)
	benchDir := filepath.Dir(filepath.Dir(thisFile))
	outFileName := filepath.Join(benchDir, fmt.Sprintf("explain_%s_%s.txt", fname, time.Now().Format("060102150405")))

	// save
	if err := os.WriteFile(outFileName, []byte(out.String()), 0644); err != nil {
		return fmt.Errorf("Failed to write output: %w", err)
	}

	fmt.Printf("Saved %s\n", outFileName)
	return nil
}
