package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestDecodeJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
		Qty  int    `json:"qty"`
	}

	valid := []string{
		`{"name":"kopi","qty":2}`,
		"{\"name\":\"kopi\",\"qty\":2}\n   \t ",
	}
	for _, body := range valid {
		var dst payload
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		if err := DecodeJSON(httptest.NewRecorder(), req, &dst); err != nil {
			t.Fatalf("DecodeJSON(%q) error = %v, want nil", body, err)
		}
		if dst.Name != "kopi" || dst.Qty != 2 {
			t.Fatalf("DecodeJSON(%q) = %+v", body, dst)
		}
	}

	invalid := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"unknown field", `{"name":"kopi","qty":1,"price":500}`},
		{"wrong type", `{"name":"kopi","qty":"two"}`},
		{"second object", `{"name":"kopi","qty":1}{"name":"teh","qty":2}`},
		{"trailing brace", `{"name":"kopi","qty":1}}`},
		{"trailing bracket", `{"name":"kopi","qty":1}]`},
		{"trailing garbage", `{"name":"kopi","qty":1} extra`},
		{"oversized payload", `{"name":"` + strings.Repeat("a", maxBodyBytes) + `","qty":1}`},
		{"oversized trailing whitespace", `{"name":"kopi","qty":1}` + strings.Repeat(" ", maxBodyBytes)},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			var dst payload
			req := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			if err := DecodeJSON(httptest.NewRecorder(), req, &dst); err == nil {
				t.Fatalf("DecodeJSON(%s) = nil, want error", tc.name)
			}
		})
	}
}

func TestServerError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantRetry  string
		wantMsg    string
	}{
		{"lock timeout", &pgconn.PgError{Code: "55P03"}, http.StatusServiceUnavailable, "1", "the database is busy, please retry"},
		{"statement timeout", &pgconn.PgError{Code: "57014"}, http.StatusServiceUnavailable, "1", "the database is busy, please retry"},
		{"unexpected error", errors.New("secret connection detail"), http.StatusInternalServerError, "", "internal error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/checkout", nil)
			ServerError(rec, req, tc.err)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("Retry-After"); got != tc.wantRetry {
				t.Fatalf("Retry-After = %q, want %q", got, tc.wantRetry)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["error"] != tc.wantMsg {
				t.Fatalf("error = %q, want %q", body["error"], tc.wantMsg)
			}
		})
	}
}

func TestServerErrorClientCanceled(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"context canceled", context.Canceled},
		{"pg query_canceled on canceled ctx", &pgconn.PgError{Code: "57014"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/products?outlet_id=1&q=kopi", nil).WithContext(ctx)
			ServerError(rec, req, tc.err)
			if rec.Code != 499 || rec.Body.Len() != 0 {
				t.Fatalf("status=%d body=%q, want 499 with empty body", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestMiddlewareRequestID(t *testing.T) {
	var seenID string
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenID = RequestID(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	// Preserves caller-provided X-Request-ID up to 64 bytes.
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("X-Request-ID", "req-abc-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if seenID != "req-abc-123" || rec.Header().Get("X-Request-ID") != "req-abc-123" {
		t.Fatalf("seen=%q header=%q, want req-abc-123", seenID, rec.Header().Get("X-Request-ID"))
	}

	// Replaces missing or overly long X-Request-ID with a generated 16-hex-char ID.
	for _, incoming := range []string{"", strings.Repeat("x", 65)} {
		req := httptest.NewRequest("GET", "/healthz", nil)
		if incoming != "" {
			req.Header.Set("X-Request-ID", incoming)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		got := rec.Header().Get("X-Request-ID")
		if len(got) != 16 || got != seenID {
			t.Fatalf("incoming len %d: seen=%q header=%q, want generated 16-char id", len(incoming), seenID, got)
		}
	}
}
