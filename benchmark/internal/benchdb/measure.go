package benchdb

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Result is one query measured on a prepared copy.
type Result struct {
	Plan               string
	Hit, Read          int64 // pages touched by the whole plan: from cache, from OS/disk
	Min, Avg, P50, P95 time.Duration
}

// Measure runs one EXPLAIN (ANALYZE, BUFFERS), which also warms the cache, then
// times runs executions that read every row, as an API handler would.
func Measure(ctx context.Context, conn *pgx.Conn, query string, runs int) (Result, error) {
	var r Result
	rows, _ := conn.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+query)
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return r, err
	}
	r.Plan = strings.Join(lines, "\n")
	r.Hit, r.Read = rootBuffers(lines)

	durations := make([]time.Duration, runs)
	for i := range durations {
		start := time.Now()
		rows, _ := conn.Query(ctx, query)
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			return r, err
		}
		durations[i] = time.Since(start)
		fmt.Printf("  run %d/%d: %s\n", i+1, runs, durations[i])
	}
	slices.Sort(durations)
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	r.Min, r.Avg = durations[0], total/time.Duration(len(durations))
	r.P50, r.P95 = Percentile(durations, 0.50), Percentile(durations, 0.95)
	return r, nil
}

var buffersRe = regexp.MustCompile(`Buffers: shared(?: hit=(\d+))?(?: read=(\d+))?`)

// rootBuffers returns the page counts of the plan's top node, which include
// every node below it.
func rootBuffers(plan []string) (hit, read int64) {
	for _, line := range plan {
		if m := buffersRe.FindStringSubmatch(line); m != nil {
			hit, _ = strconv.ParseInt(m[1], 10, 64)
			read, _ = strconv.ParseInt(m[2], 10, 64)
			return hit, read
		}
	}
	return 0, 0
}

// Percentile uses the nearest-rank method on an ascending slice.
func Percentile(sorted []time.Duration, p float64) time.Duration {
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(idx, 0)]
}

// Short formats a duration as seconds or milliseconds for reports.
func Short(d time.Duration) string {
	if d >= time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
}
