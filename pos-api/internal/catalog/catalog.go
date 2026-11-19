// Package catalog serves read-only product and outlet data for the cashier screen.
package catalog

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AldiandyaIrsyad/pos-api/internal/web"
)

type Handler struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Handler { return &Handler{db: db} }

func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /outlets", h.outlets)
	mux.HandleFunc("GET /products", h.products)
}

type outlet struct {
	ID   int    `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

func (h *Handler) outlets(w http.ResponseWriter, r *http.Request) {
	rows, _ := h.db.Query(r.Context(), `SELECT id, code, name FROM outlets ORDER BY id`)
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[outlet])
	if err != nil {
		web.ServerError(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, list)
}

type product struct {
	ID       int    `json:"id"`
	SKU      string `json:"sku"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Price    int64  `json:"price"`
	Stock    int    `json:"stock"` // at the requested outlet
}

type productPage struct {
	Items      []product `json:"items"`
	NextCursor *string   `json:"next_cursor"` // null on the last page
}

const (
	defaultLimit = 20
	maxLimit     = 100
	maxQueryLen  = 100
)

// GET /products?outlet_id=&q=&category=&cursor=&limit=
//
// q matches anywhere in the name or SKU, case-insensitive.
// Results are ordered by id; cursor is the last id of the previous page.
func (h *Handler) products(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()

	outletID64, err := strconv.ParseInt(qs.Get("outlet_id"), 10, 32)
	if err != nil || outletID64 <= 0 {
		web.WriteError(w, http.StatusBadRequest, "outlet_id is required and must be a positive integer")
		return
	}
	outletID := int(outletID64)
	limit := defaultLimit
	if s := qs.Get("limit"); s != "" {
		limit, err = strconv.Atoi(s)
		if err != nil || limit < 1 || limit > maxLimit {
			web.WriteError(w, http.StatusBadRequest, fmt.Sprintf("limit must be an integer between 1 and %d", maxLimit))
			return
		}
	}
	afterID := 0
	if s := qs.Get("cursor"); s != "" {
		cursor64, err := strconv.ParseInt(s, 10, 32)
		if err != nil || cursor64 < 0 {
			web.WriteError(w, http.StatusBadRequest, "cursor is invalid")
			return
		}
		afterID = int(cursor64)
	}
	q := strings.TrimSpace(qs.Get("q"))
	if utf8.RuneCountInString(q) > maxQueryLen {
		web.WriteError(w, http.StatusBadRequest, fmt.Sprintf("q must be at most %d characters", maxQueryLen))
		return
	}
	category := strings.TrimSpace(qs.Get("category"))
	// Postgres rejects invalid UTF-8 and NUL bytes in TEXT, which would surface as a 500.
	if !utf8.ValidString(q) || !utf8.ValidString(category) || strings.ContainsRune(q, 0) || strings.ContainsRune(category, 0) {
		web.WriteError(w, http.StatusBadRequest, "q and category must be valid UTF-8 without NUL bytes")
		return
	}

	// Filters are appended only when present, instead of "$2 = '' OR ...": with a
	// fixed SQL text, a cached generic plan could not use the trigram index.
	sql := `SELECT p.id, p.sku, p.name, p.category, p.price, s.qty
	        FROM products p
	        JOIN stocks s ON s.outlet_id = $1 AND s.product_id = p.id
	        WHERE p.id > $2`
	args := []any{outletID, afterID}
	if q != "" {
		args = append(args, "%"+escapeLike(q)+"%")
		sql += fmt.Sprintf(" AND (p.name ILIKE $%[1]d OR p.sku ILIKE $%[1]d)", len(args))
	}
	if category != "" {
		args = append(args, category)
		sql += fmt.Sprintf(" AND p.category = $%d", len(args))
	}
	// One extra row tells us whether another page exists.
	args = append(args, limit+1)
	sql += fmt.Sprintf(" ORDER BY p.id LIMIT $%d", len(args))

	rows, _ := h.db.Query(r.Context(), sql, args...)
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[product])
	if err != nil {
		web.ServerError(w, r, err)
		return
	}

	page := productPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		next := strconv.Itoa(page.Items[limit-1].ID)
		page.NextCursor = &next
	}
	web.WriteJSON(w, http.StatusOK, page)
}

// escapeLike makes % and _ in the user's text match literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
