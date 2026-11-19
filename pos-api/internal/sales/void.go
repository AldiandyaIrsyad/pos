package sales

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/AldiandyaIrsyad/pos-api/internal/web"
)

// POST /transactions/{id}/void
//
// Sets the status to VOID and gives the stock back, exactly once. Voiding an
// already voided transaction returns it unchanged (200) without touching stock,
// so a retried void is harmless.
func (h *Handler) void(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		web.WriteError(w, http.StatusBadRequest, "transaction id must be a positive integer")
		return
	}

	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		web.ServerError(w, r, err)
		return
	}
	defer rollback(ctx, tx)

	// Only the request that flips PAID -> VOID gets a row back. A concurrent void
	// waits on the row lock, then re-checks status = 'PAID', finds VOID, gets nothing.
	var t transaction
	err = tx.QueryRow(ctx,
		`UPDATE transactions SET status = 'VOID'
		 WHERE id = $1 AND status = 'PAID'
		 RETURNING id, outlet_id, cashier_name, status, total, created_at`, id).
		Scan(&t.ID, &t.OutletID, &t.CashierName, &t.Status, &t.Total, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		rollback(ctx, tx)
		existing, err := h.loadTransaction(r, id)
		if errors.Is(err, pgx.ErrNoRows) {
			web.WriteError(w, http.StatusNotFound, "transaction not found")
			return
		}
		if err != nil {
			web.ServerError(w, r, err)
			return
		}
		if existing.Status != "VOID" {
			web.WriteError(w, http.StatusConflict, fmt.Sprintf("transaction status is %s and cannot be voided", existing.Status))
			return
		}
		web.WriteJSON(w, http.StatusOK, existing)
		return
	}
	if err != nil {
		web.ServerError(w, r, err)
		return
	}

	rows, _ := tx.Query(ctx,
		`SELECT product_id, qty, price, subtotal
		 FROM transaction_items
		 WHERE transaction_id = $1
		 ORDER BY id`, id)
	t.Items, err = pgx.CollectRows(rows, pgx.RowToStructByPos[itemLine])
	if err != nil {
		web.ServerError(w, r, err)
		return
	}

	// Merge any repeated product_ids and lock stock rows in product_id order,
	// matching checkout's lock order to prevent deadlocks.
	raw := make([]lineItem, len(t.Items))
	for i, it := range t.Items {
		raw[i] = lineItem{ProductID: it.ProductID, Qty: it.Qty}
	}
	restock := normalize(raw)
	if len(restock) > 0 {
		productIDs := make([]int32, len(restock))
		qtys := make([]int32, len(restock))
		for i, it := range restock {
			productIDs[i] = int32(it.ProductID)
			qtys[i] = int32(it.Qty)
		}
		if _, err := tx.Exec(ctx,
			`SELECT product_id FROM stocks
			 WHERE outlet_id = $1 AND product_id = ANY($2)
			 ORDER BY product_id
			 FOR UPDATE`,
			t.OutletID, productIDs); err != nil {
			web.ServerError(w, r, err)
			return
		}
		if _, err := tx.Exec(ctx,
			`UPDATE stocks s
			 SET qty = s.qty + u.qty
			 FROM unnest($2::int[], $3::int[]) AS u(product_id, qty)
			 WHERE s.outlet_id = $1 AND s.product_id = u.product_id`,
			t.OutletID, productIDs, qtys); err != nil {
			web.ServerError(w, r, err)
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		web.ServerError(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, t)
}
