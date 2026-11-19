// search measures GET /products latency through the HTTP API, the way the
// cashier screen calls it while someone types.
//
// Every prefix of each term is a request ("k", "ko", "kop", "kopi"), spread
// over the outlets, limit 20 like the web app. That is more requests than the
// debounced web app sends, and includes 1–2 character prefixes, which are too
// short for the trigram indexes. One warmup pass, then the measured passes,
// first from one client, then from -concurrency clients at once. Read-only; the
// report is written to benchmark/.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AldiandyaIrsyad/benchmark/internal/benchdb"
)

// Terms a cashier would type: common names (PK early stop), SKU/number fragments,
// rare terms, and misses (trigram bitmap scan).
var terms = []string{"kopi", "indomie", "teh botol", "shampo", "roti", "gold", "sku-0012", "3675", "zzzz"}

// Extra queries exercising category filtering and cursor pagination.
var extraQueries = []struct {
	q, category, cursor string
}{
	{"kopi", "Minuman", ""},
	{"kopi", "Minuman", "2500"},
	{"sku-0049", "", "4000"},
	{"zzzz", "Snack", ""},
}

type request struct {
	path  string
	short bool // q shorter than 3 characters: no trigram index
}

type sample struct {
	d     time.Duration
	short bool
}

func main() {
	api := flag.String("api", "http://localhost:8080", "pos-api base URL")
	passes := flag.Int("passes", 5, "measured passes over all prefixes, per phase")
	outlets := flag.Int("outlets", 10, "outlet ids 1..n to spread requests over")
	concurrency := flag.Int("concurrency", 10, "clients in the concurrent phase")
	flag.Parse()

	if err := run(*api, *passes, *outlets, *concurrency); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(api string, passes, outlets, concurrency int) error {
	if passes < 1 || outlets < 1 || concurrency < 1 {
		return fmt.Errorf("-passes, -outlets and -concurrency must be at least 1")
	}
	var reqs []request
	i := 0
	for _, term := range terms {
		for n := 1; n <= len(term); n++ {
			q := term[:n]
			outlet := i%outlets + 1
			i++
			reqs = append(reqs, request{
				path:  fmt.Sprintf("%s/products?outlet_id=%d&limit=20&q=%s", api, outlet, url.QueryEscape(q)),
				short: len([]rune(q)) < 3,
			})
		}
	}
	for _, ex := range extraQueries {
		outlet := i%outlets + 1
		i++
		params := url.Values{"outlet_id": {fmt.Sprint(outlet)}, "limit": {"20"}, "q": {ex.q}}
		if ex.category != "" {
			params.Set("category", ex.category)
		}
		if ex.cursor != "" {
			params.Set("cursor", ex.cursor)
		}
		reqs = append(reqs, request{
			path:  fmt.Sprintf("%s/products?%s", api, params.Encode()),
			short: len([]rune(ex.q)) < 3,
		})
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: concurrency}}

	fmt.Printf("warmup: %d requests\n", len(reqs))
	if _, err := fire(client, reqs, 1); err != nil {
		return err
	}

	var all []request
	for range passes {
		all = append(all, reqs...)
	}
	fmt.Printf("1 client: %d requests\n", len(all))
	one, err := fire(client, all, 1)
	if err != nil {
		return err
	}
	fmt.Printf("%d clients: %d requests\n", concurrency, len(all))
	many, err := fire(client, all, concurrency)
	if err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Product search latency: GET /products through the HTTP API\n")
	fmt.Fprintf(&b, "date:     %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "api:      %s\n", api)
	fmt.Fprintf(&b, "requests: every prefix of %q,\n", terms)
	fmt.Fprintf(&b, "          outlets 1..%d in turn, limit=20; %d prefixes x %d passes per phase, after 1 warmup pass\n", outlets, len(reqs), passes)
	fmt.Fprintf(&b, "target:   p95 < 50 ms\n\n")
	fmt.Fprintf(&b, "%-26s %6s %9s %9s %9s %9s %9s\n", "", "n", "min", "p50", "p95", "p99", "max")
	for _, phase := range []struct {
		name    string
		samples []sample
	}{{"1 client", one}, {fmt.Sprintf("%d clients", concurrency), many}} {
		writeRow(&b, phase.name+", all", phase.samples, func(sample) bool { return true })
		writeRow(&b, phase.name+", q < 3 chars", phase.samples, func(s sample) bool { return s.short })
		writeRow(&b, phase.name+", q >= 3 chars", phase.samples, func(s sample) bool { return !s.short })
	}
	fmt.Fprintf(&b, "\nlatency is measured at the client: HTTP, routing, query and JSON included.\n")
	fmt.Fprintf(&b, "On Windows the clock advances in ~0.5 ms steps, so values are +-0.5 ms and a min of 0 means under one step.\n")

	report := b.String()
	fmt.Print("\n" + report)
	out := filepath.Join(benchdb.RepoDir(), "benchmark", fmt.Sprintf("bench_search_%s.txt", time.Now().Format("20060102-150405")))
	if err := os.WriteFile(out, []byte(report), 0644); err != nil {
		return err
	}
	fmt.Printf("saved %s\n", out)
	return nil
}

// fire sends reqs from the given number of clients and times each one.
// Any status other than 200 is an error: the measurement would be meaningless.
func fire(client *http.Client, reqs []request, clients int) ([]sample, error) {
	samples := make([]sample, len(reqs))
	errs := make([]error, clients)
	var next atomic.Int64
	var wg sync.WaitGroup
	for c := range clients {
		wg.Go(func() {
			for {
				i := int(next.Add(1) - 1)
				if i >= len(reqs) {
					return
				}
				t := time.Now()
				resp, err := client.Get(reqs[i].path)
				if err != nil {
					errs[c] = err
					return
				}
				_, err = io.Copy(io.Discard, resp.Body) // read the whole body, as the browser does
				resp.Body.Close()
				if err != nil {
					errs[c] = err
					return
				}
				if resp.StatusCode != http.StatusOK {
					errs[c] = fmt.Errorf("%s: status %d", reqs[i].path, resp.StatusCode)
					return
				}
				samples[i] = sample{d: time.Since(t), short: reqs[i].short}
			}
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return samples, nil
}

func writeRow(b *strings.Builder, name string, samples []sample, keep func(sample) bool) {
	var ds []time.Duration
	for _, s := range samples {
		if keep(s) {
			ds = append(ds, s.d)
		}
	}
	slices.Sort(ds)
	fmt.Fprintf(b, "%-26s %6d %9s %9s %9s %9s %9s\n", name, len(ds),
		benchdb.Ms(ds[0]), benchdb.Ms(benchdb.Percentile(ds, 0.50)), benchdb.Ms(benchdb.Percentile(ds, 0.95)),
		benchdb.Ms(benchdb.Percentile(ds, 0.99)), benchdb.Ms(ds[len(ds)-1]))
}
