package benchdb

import (
	"context"
	"crypto/rand"
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
// ColdHit/ColdRead come from the first EXPLAIN (ANALYZE, BUFFERS) before cache
// warmup; WarmHit/WarmRead come from an EXPLAIN after the timed warm runs.
type Result struct {
	Plan                 string
	ColdHit, ColdRead    int64
	WarmHit, WarmRead    int64
	Min, Avg, P50, P95   time.Duration
}

// Measure runs one cold EXPLAIN (ANALYZE, BUFFERS), times runs warm executions
// that read every row, then runs one warm EXPLAIN (ANALYZE, BUFFERS) so buffer
// counts are available for both cache states.
func Measure(ctx context.Context, conn *pgx.Conn, query string, runs int) (Result, error) {
	var r Result
	coldLines, err := explain(ctx, conn, query)
	if err != nil {
		return r, err
	}
	r.Plan = strings.Join(coldLines, "\n")
	r.ColdHit, r.ColdRead = rootBuffers(coldLines)

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

	warmLines, err := explain(ctx, conn, query)
	if err != nil {
		return r, err
	}
	r.WarmHit, r.WarmRead = rootBuffers(warmLines)

	slices.Sort(durations)
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	r.Min, r.Avg = durations[0], total/time.Duration(len(durations))
	r.P50, r.P95 = Percentile(durations, 0.50), Percentile(durations, 0.95)
	return r, nil
}

func explain(ctx context.Context, conn *pgx.Conn, query string) ([]string, error) {
	rows, _ := conn.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+query)
	return pgx.CollectRows(rows, pgx.RowTo[string])
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
	return Ms(d)
}

// Ms formats a duration in milliseconds with two decimals.
func Ms(d time.Duration) string {
	return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
}

// UUID4 returns a random canonical UUIDv4 string.
func UUID4() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
