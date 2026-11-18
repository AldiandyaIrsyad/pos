package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

func WriteJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshal response", "err", err)
		status = http.StatusInternalServerError
		b = []byte(`{"error":"internal error"}`)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}

// WriteError sends {"error": msg}. Every error response in the API has this field;
// endpoints may add more (e.g. short_items on a 409) with WriteJSON.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// ServerError logs the real error with the request ID and hides it from the client.
func ServerError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("internal error", "request_id", RequestID(r.Context()), "method", r.Method, "path", r.URL.Path, "err", err)
	WriteError(w, http.StatusInternalServerError, "internal error")
}

const maxBodyBytes = 64 << 10

// DecodeJSON reads one JSON object into v: at most 64 KiB, no unknown fields,
// nothing after it. The returned error message is safe to show the client.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields() // a misspelled field is a client bug; reject it instead of ignoring it

	if err := dec.Decode(v); err != nil {
		var typeErr *json.UnmarshalTypeError
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &typeErr):
			return fmt.Errorf("field %q must be a %s", typeErr.Field, typeErr.Type)
		case errors.As(err, &maxErr):
			return errors.New("request body too large")
		default:
			return fmt.Errorf("invalid JSON: %v", err)
		}
	}

	if dec.More() {
		return errors.New("request body must contain a single JSON object")
	}

	return nil
}

type ctxKey struct{}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Middleware gives every request an ID (the client's X-Request-ID, or a new one),
// echoes it in the response, and writes one structured log line per request.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 64 {
			b := make([]byte, 8)
			rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-ID", id)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
		slog.Info("request",
			"request_id", id,
			"method", r.Method,
			"path", r.URL.Path,
			"query", r.URL.RawQuery,
			"status", rec.status,
			"duration_ms", float64(time.Since(start).Microseconds())/1000,
		)
	})
}
