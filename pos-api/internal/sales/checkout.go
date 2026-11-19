// Package sales creates and cancels sales. It is the only package that writes
// stocks, transactions, transaction_items and idempotency_keys, so every stock
// and money rule lives here.
package sales

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	defer rollback(ctx, tx) // no-op after Commit

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
		rollback(ctx, tx)
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

	// 3. Lock every requested stock row in product_id order in one statement,
	//    then check shortages against the locked quantities. A 409 rolls back
	//    before touching transactions/transaction_items and always reports the
	//    exact committed availability.
	available := make(map[int]int, len(items))
	rows, _ = tx.Query(ctx,
		`SELECT product_id, qty FROM stocks
		 WHERE outlet_id = $1 AND product_id = ANY($2)
		 ORDER BY product_id
		 FOR UPDATE`,
		req.OutletID, ids)
	var stockProductID, stockQty int
	_, err = pgx.ForEachRow(rows, []any{&stockProductID, &stockQty}, func() error {
		available[stockProductID] = stockQty
		return nil
	})
	if err != nil {
		web.ServerError(w, r, err)
		return
	}

	var short []shortItem
	for _, it := range items {
		if avail := available[it.ProductID]; avail < it.Qty {
			short = append(short, shortItem{ProductID: it.ProductID, Requested: it.Qty, Available: avail})
		}
	}
	if len(short) > 0 {
		web.WriteJSON(w, http.StatusConflict, map[string]any{
			"error":       "insufficient stock",
			"short_items": short,
		})
		return
	}

	// 4. Decrement stock and record the sale in one round trip while holding
	//    the stock locks, then commit.
	t := transaction{OutletID: req.OutletID, CashierName: cashier, Status: "PAID", Items: make([]itemLine, len(items))}
	qtys := make([]int32, len(items))
	unitPrices := make([]int64, len(items))
	subtotals := make([]int64, len(items))
	for i, it := range items {
		p := prices[it.ProductID]
		sub := p * int64(it.Qty)
		t.Items[i] = itemLine{ProductID: it.ProductID, Qty: it.Qty, Price: p, Subtotal: sub}
		t.Total += sub
		qtys[i], unitPrices[i], subtotals[i] = int32(it.Qty), p, sub
	}

	var updated int
	err = tx.QueryRow(ctx, `
		WITH dec AS (
			UPDATE stocks s
			SET qty = s.qty - u.qty
			FROM unnest($2::int[], $3::int[]) AS u(product_id, qty)
			WHERE s.outlet_id = $1 AND s.product_id = u.product_id AND s.qty >= u.qty
			RETURNING s.product_id
		),
		tx AS (
			INSERT INTO transactions (outlet_id, cashier_name, status, total)
			VALUES ($1, $4, 'PAID', $5)
			RETURNING id, created_at
		),
		ins_items AS (
			INSERT INTO transaction_items (transaction_id, product_id, qty, price, subtotal)
			SELECT tx.id, u.product_id, u.qty, u.price, u.subtotal
			FROM tx
			CROSS JOIN unnest($2::int[], $3::int[], $6::bigint[], $7::bigint[]) AS u(product_id, qty, price, subtotal)
		),
		link_key AS (
			UPDATE idempotency_keys
			SET transaction_id = (SELECT id FROM tx)
			WHERE key = $8
		)
		SELECT tx.id, tx.created_at, (SELECT count(*) FROM dec)
		FROM tx`,
		t.OutletID, ids, qtys, t.CashierName, t.Total, unitPrices, subtotals, key,
	).Scan(&t.ID, &t.CreatedAt, &updated)
	if err != nil {
		web.ServerError(w, r, err)
		return
	}
	if updated != len(items) {
		web.ServerError(w, r, fmt.Errorf("stock update mismatch: updated %d of %d items", updated, len(items)))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		web.ServerError(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusCreated, t)
}

// rollback ends an uncommitted transaction even if the request context was
// canceled, so pgx returns the connection to the pool instead of closing it.
func rollback(ctx context.Context, tx pgx.Tx) {
	rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(rbCtx)
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
