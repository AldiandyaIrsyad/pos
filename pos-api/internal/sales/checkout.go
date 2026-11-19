// Package sales creates and cancels sales. It is the only package that writes
// stocks, transactions, transaction_items and idempotency_keys, so every stock
// and money rule lives here.
package sales

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AldiandyaIrsyad/pos-api/internal/web"
)

type Handler struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Handler { return &Handler{db: db} }

func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /checkout", h.checkout)
	mux.HandleFunc("POST /transactions/{id}/void", h.void)
}

type checkoutRequest struct {
	OutletID    int        `json:"outlet_id"`
	CashierName string     `json:"cashier_name"`
	Items       []lineItem `json:"items"`
}

type lineItem struct {
	ProductID int `json:"product_id"`
	Qty       int `json:"qty"`
}

type shortItem struct {
	ProductID int `json:"product_id"`
	Requested int `json:"requested"`
	Available int `json:"available"`
}

type transaction struct {
	ID          int64      `json:"id"`
	OutletID    int        `json:"outlet_id"`
	CashierName string     `json:"cashier_name"`
	Status      string     `json:"status"`
	Total       int64      `json:"total"`
	CreatedAt   time.Time  `json:"created_at"`
	Items       []itemLine `json:"items"`
}

type itemLine struct {
	ProductID int   `json:"product_id"`
	Qty       int   `json:"qty"`
	Price     int64 `json:"price"`
	Subtotal  int64 `json:"subtotal"`
}

const (
	maxItems       = 100
	maxQtyPerItem  = 10_000
	maxCashierName = 100
)

// validate returns a message for the first invalid field, or "".
func (req *checkoutRequest) validate() string {
	switch {
	case req.OutletID <= 0:
		return "outlet_id is required and must be a positive integer"
	case strings.TrimSpace(req.CashierName) == "":
		return "cashier_name is required"
	case len(req.CashierName) > maxCashierName:
		return fmt.Sprintf("cashier_name must be at most %d characters", maxCashierName)
	case len(req.Items) == 0:
		return "items must contain at least one item"
	case len(req.Items) > maxItems:
		return fmt.Sprintf("items must contain at most %d items", maxItems)
	}
	for i, it := range req.Items {
		if it.ProductID <= 0 {
			return fmt.Sprintf("items[%d].product_id must be a positive integer", i)
		}
		if it.Qty <= 0 || it.Qty > maxQtyPerItem {
			return fmt.Sprintf("items[%d].qty must be between 1 and %d", i, maxQtyPerItem)
		}
	}
	return ""
}

// normalize merges repeated products and sorts by product_id. The fixed order
// makes every checkout lock stock rows in the same sequence, so two carts that
// share products wait for each other instead of deadlocking.
func normalize(items []lineItem) []lineItem {
	qty := map[int]int{}
	for _, it := range items {
		qty[it.ProductID] += it.Qty
	}
	out := make([]lineItem, 0, len(qty))
	for id, q := range qty {
		out = append(out, lineItem{ProductID: id, Qty: q})
	}
	slices.SortFunc(out, func(a, b lineItem) int { return cmp.Compare(a.ProductID, b.ProductID) })
	return out
}

// requestHash identifies what a key was first used for, so reusing the key
// with a different cart is rejected instead of replaying the wrong sale.
func requestHash(outletID int, cashier string, items []lineItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%s|", outletID, cashier)
	for _, it := range items {
		fmt.Fprintf(&b, "%d:%d,", it.ProductID, it.Qty)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// POST /checkout, header Idempotency-Key: <uuid>
//
//	201 new transaction        200 replay of the same key + same body
//	400 invalid input          404 unknown outlet or product
//	409 not enough stock       422 same key, different body
//
// Everything runs in one DB transaction: the key, the stock decrements and the
// inserts commit together or not at all.
func (h *Handler) checkout(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if !validUUID(key) {
		web.WriteError(w, http.StatusBadRequest, "Idempotency-Key header is required and must be a UUID")
		return
	}
	var req checkoutRequest
	if err := web.DecodeJSON(w, r, &req); err != nil {
		web.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if msg := req.validate(); msg != "" {
		web.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	cashier := strings.TrimSpace(req.CashierName)
	items := normalize(req.Items)
	hash := requestHash(req.OutletID, cashier, items)

	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		web.ServerError(w, r, err)
		return
	}
	defer tx.Rollback(ctx) // no-op after Commit

	// 1. Claim the key first. A concurrent request with the same key blocks here
	//    on the primary key until this transaction ends: after our commit it gets
	//    no row and replays; after our rollback it proceeds as a fresh attempt.
	tag, err := tx.Exec(ctx,
		`INSERT INTO idempotency_keys (key, request_hash) VALUES ($1, $2)
		 ON CONFLICT (key) DO NOTHING`, key, hash)
	if err != nil {
		web.ServerError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		tx.Rollback(ctx)
		h.replay(w, r, key, hash)
		return
	}

	// 2. Outlet and prices come from the database, never from the client.
	var outletExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM outlets WHERE id = $1)`, req.OutletID).Scan(&outletExists); err != nil {
		web.ServerError(w, r, err)
		return
	}
	if !outletExists {
		web.WriteError(w, http.StatusNotFound, fmt.Sprintf("outlet %d not found", req.OutletID))
		return
	}

	ids := make([]int32, len(items))
	for i, it := range items {
		ids[i] = int32(it.ProductID)
	}
	prices := make(map[int]int64, len(items))
	rows, _ := tx.Query(ctx, `SELECT id, price FROM products WHERE id = ANY($1)`, ids)
	var id int
	var price int64
	_, err = pgx.ForEachRow(rows, []any{&id, &price}, func() error {
		prices[id] = price
		return nil
	})
	if err != nil {
		web.ServerError(w, r, err)
		return
	}
	var unknown []string
	for _, it := range items {
		if _, ok := prices[it.ProductID]; !ok {
			unknown = append(unknown, strconv.Itoa(it.ProductID))
		}
	}
	if len(unknown) > 0 {
		web.WriteError(w, http.StatusNotFound, "unknown product_id: "+strings.Join(unknown, ", "))
		return
	}

	// 3. Record the sale. This needs no stock row, so it runs before step 4:
	//    the stock row locks taken there are held until commit, and nothing but
	//    the commit is left after them. A shortage rolls these inserts back.
	t := transaction{OutletID: req.OutletID, CashierName: cashier, Status: "PAID", Items: make([]itemLine, len(items))}
	productIDs := make([]int32, len(items))
	qtys := make([]int32, len(items))
	unitPrices := make([]int64, len(items))
	subtotals := make([]int64, len(items))
	for i, it := range items {
		p := prices[it.ProductID]
		sub := p * int64(it.Qty)
		t.Items[i] = itemLine{ProductID: it.ProductID, Qty: it.Qty, Price: p, Subtotal: sub}
		t.Total += sub
		productIDs[i], qtys[i], unitPrices[i], subtotals[i] = int32(it.ProductID), int32(it.Qty), p, sub
	}

	err = tx.QueryRow(ctx,
		`INSERT INTO transactions (outlet_id, cashier_name, status, total)
		 VALUES ($1, $2, 'PAID', $3) RETURNING id, created_at`,
		t.OutletID, t.CashierName, t.Total).Scan(&t.ID, &t.CreatedAt)
	if err != nil {
		web.ServerError(w, r, err)
		return
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO transaction_items (transaction_id, product_id, qty, price, subtotal)
		 SELECT $1, * FROM unnest($2::int[], $3::int[], $4::bigint[], $5::bigint[])`,
		t.ID, productIDs, qtys, unitPrices, subtotals)
	if err != nil {
		web.ServerError(w, r, err)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE idempotency_keys SET transaction_id = $1 WHERE key = $2`, t.ID, key); err != nil {
		web.ServerError(w, r, err)
		return
	}

	// 4. Take the stock, item by item in product_id order. The UPDATE locks the
	//    row; if another checkout holds it, this one waits, then re-checks
	//    "qty >= n" against the committed new qty (READ COMMITTED re-evaluates the
	//    WHERE on the latest row version). So stock can't go below zero, no matter
	//    how many checkouts race. Keep going after a shortage to report all of them.
	var short []shortItem
	for _, it := range items {
		tag, err := tx.Exec(ctx,
			`UPDATE stocks SET qty = qty - $3
			 WHERE outlet_id = $1 AND product_id = $2 AND qty >= $3`,
			req.OutletID, it.ProductID, it.Qty)
		if err != nil {
			web.ServerError(w, r, err)
			return
		}
		if tag.RowsAffected() == 1 {
			continue
		}
		var available int
		err = tx.QueryRow(ctx, `SELECT qty FROM stocks WHERE outlet_id = $1 AND product_id = $2`,
			req.OutletID, it.ProductID).Scan(&available)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) { // no stock row: available stays 0
			web.ServerError(w, r, err)
			return
		}
		short = append(short, shortItem{ProductID: it.ProductID, Requested: it.Qty, Available: available})
	}
	if len(short) > 0 {
		// Rolled back by the defer: the sale rows and the key are gone, so a retry
		// is judged against current stock. The transaction id stays used (sequences
		// don't roll back), which leaves a gap in ids; ids are not receipt numbers.
		web.WriteJSON(w, http.StatusConflict, map[string]any{
			"error":       "insufficient stock",
			"short_items": short,
		})
		return
	}
	if err := tx.Commit(ctx); err != nil {
		web.ServerError(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusCreated, t)
}

// replay answers a request whose key was already committed: the same body gets
// the original transaction, a different body is refused.
func (h *Handler) replay(w http.ResponseWriter, r *http.Request, key, hash string) {
	var storedHash string
	var txID int64
	err := h.db.QueryRow(r.Context(),
		`SELECT request_hash, transaction_id FROM idempotency_keys WHERE key = $1`, key).Scan(&storedHash, &txID)
	if err != nil {
		web.ServerError(w, r, err)
		return
	}
	if storedHash != hash {
		web.WriteError(w, http.StatusUnprocessableEntity, "Idempotency-Key was already used for a different request")
		return
	}
	t, err := h.loadTransaction(r, txID)
	if err != nil {
		web.ServerError(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, t)
}

// loadTransaction reads a committed transaction with its items.
// Returns pgx.ErrNoRows when the id doesn't exist.
func (h *Handler) loadTransaction(r *http.Request, id int64) (transaction, error) {
	ctx := r.Context()
	var t transaction
	err := h.db.QueryRow(ctx,
		`SELECT id, outlet_id, cashier_name, status, total, created_at FROM transactions WHERE id = $1`, id).
		Scan(&t.ID, &t.OutletID, &t.CashierName, &t.Status, &t.Total, &t.CreatedAt)
	if err != nil {
		return t, err
	}
	rows, _ := h.db.Query(ctx,
		`SELECT product_id, qty, price, subtotal FROM transaction_items WHERE transaction_id = $1 ORDER BY id`, id)
	t.Items, err = pgx.CollectRows(rows, pgx.RowToStructByPos[itemLine])
	return t, err
}

// validUUID accepts the canonical 8-4-4-4-12 hex form.
func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return false
			}
		}
	}
	return true
}
