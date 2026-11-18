package reports

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AldiandyaIrsyad/pos-api/internal/web"
)

// Assumed indonesian WIB
var wib = time.FixedZone("WIB", 7*60*60)
const sqlZone = "Asia/Jakarta" 

type Handler struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Handler { return &Handler{db: db} }

func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /reports/daily-sales", h.dailySales)
	mux.HandleFunc("GET /reports/top-products", h.topProducts)
}

// filter is the common outlet_id + from/to part of both reports.
// from and to are inclusive dates; start/end is the half-open time range
// [from 00:00 WIB, the day after to 00:00 WIB), compared against the raw
// created_at column so the (outlet_id, created_at) index can be used.
type filter struct {
	outletID   int
	start, end time.Time
}

// parseFilter writes a 400 and returns false when the parameters are invalid.
func parseFilter(w http.ResponseWriter, r *http.Request) (filter, bool) {
	qs := r.URL.Query()
	outletID, err := strconv.Atoi(qs.Get("outlet_id"))
	if err != nil || outletID <= 0 {
		web.WriteError(w, http.StatusBadRequest, "outlet_id is required and must be a positive integer")
		return filter{}, false
	}
	from, err := time.ParseInLocation(time.DateOnly, qs.Get("from"), wib)
	if err != nil {
		web.WriteError(w, http.StatusBadRequest, "from is required, format YYYY-MM-DD")
		return filter{}, false
	}
	to, err := time.ParseInLocation(time.DateOnly, qs.Get("to"), wib)
	if err != nil {
		web.WriteError(w, http.StatusBadRequest, "to is required, format YYYY-MM-DD")
		return filter{}, false
	}
	if to.Before(from) {
		web.WriteError(w, http.StatusBadRequest, "from must be on or before to")
		return filter{}, false
	}
	return filter{outletID: outletID, start: from, end: to.AddDate(0, 0, 1)}, true
}

type dailyRow struct {
	Date         string `json:"date"` // YYYY-MM-DD, WIB
	Transactions int64  `json:"transactions"`
	Revenue      int64  `json:"revenue"`
	Average      int64  `json:"average"` // rounded to whole rupiah
}

// GET /reports/daily-sales?outlet_id=&from=&to=
// Days without sales are left out.
func (h *Handler) dailySales(w http.ResponseWriter, r *http.Request) {
	f, ok := parseFilter(w, r)
	if !ok {
		return
	}
	rows, _ := h.db.Query(r.Context(), `
		SELECT to_char(created_at AT TIME ZONE $4, 'YYYY-MM-DD') AS day,
		       count(*),
		       sum(total)::bigint,
		       round(avg(total))::bigint
		FROM transactions
		WHERE outlet_id = $1 AND status = 'PAID'
		  AND created_at >= $2 AND created_at < $3
		GROUP BY day
		ORDER BY day`,
		f.outletID, f.start, f.end, sqlZone)
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[dailyRow])
	if err != nil {
		web.ServerError(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, list)
}

type topRow struct {
	ProductID int    `json:"product_id"`
	Name      string `json:"name"`
	QtySold   int64  `json:"qty_sold"`
	Revenue   int64  `json:"revenue"`
}

const (
	defaultTopLimit = 10
	maxTopLimit     = 100
)

// Whitelisted ORDER BY clauses
var topOrder = map[string]string{
	"qty":     "qty_sold DESC, revenue DESC, product_id",
	"revenue": "revenue DESC, qty_sold DESC, product_id",
}

// GET /reports/top-products?outlet_id=&from=&to=&limit=10&sort=qty|revenue
func (h *Handler) topProducts(w http.ResponseWriter, r *http.Request) {
	f, ok := parseFilter(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	limit := defaultTopLimit
	if s := qs.Get("limit"); s != "" {
		var err error
		limit, err = strconv.Atoi(s)
		if err != nil || limit < 1 || limit > maxTopLimit {
			web.WriteError(w, http.StatusBadRequest, fmt.Sprintf("limit must be an integer between 1 and %d", maxTopLimit))
			return
		}
	}
	sort := qs.Get("sort")
	if sort == "" {
		sort = "qty"
	}
	order, ok := topOrder[sort]
	if !ok {
		web.WriteError(w, http.StatusBadRequest, "sort must be qty or revenue")
		return
	}

	rows, _ := h.db.Query(r.Context(), `
		SELECT s.product_id, p.name, s.qty_sold, s.revenue
		FROM (
		  SELECT ti.product_id,
		         sum(ti.qty)::bigint      AS qty_sold,
		         sum(ti.subtotal)::bigint AS revenue
		  FROM transactions t
		  JOIN transaction_items ti ON ti.transaction_id = t.id
		  WHERE t.outlet_id = $1 AND t.status = 'PAID'
		    AND t.created_at >= $2 AND t.created_at < $3
		  GROUP BY ti.product_id
		  ORDER BY `+order+`
		  LIMIT $4
		) s
		JOIN products p ON p.id = s.product_id
		ORDER BY `+order,
		f.outletID, f.start, f.end, limit)
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[topRow])
	if err != nil {
		web.ServerError(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, list)
}
