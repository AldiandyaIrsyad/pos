package sales

import (
	"errors"
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
	defer tx.Rollback(ctx)

	// Only the request that flips PAID -> VOID gets a row back. A concurrent void
	// waits on the row lock, then re-checks status = 'PAID', finds VOID, gets nothing.
	var outletID int
	err = tx.QueryRow(ctx,
		`UPDATE transactions SET status = 'VOID' WHERE id = $1 AND status = 'PAID' RETURNING outlet_id`, id).
		Scan(&outletID)
	if errors.Is(err, pgx.ErrNoRows) {
		tx.Rollback(ctx)
		t, err := h.loadTransaction(r, id)
		if errors.Is(err, pgx.ErrNoRows) {
			web.WriteError(w, http.StatusNotFound, "transaction not found")
			return
		}
		if err != nil {
			web.ServerError(w, r, err)
			return
		}
		web.WriteJSON(w, http.StatusOK, t) // already VOID
		return
	}
	if err != nil {
		web.ServerError(w, r, err)
		return
	}

	// Give the stock back in product_id order, the same lock order as checkout.
	rows, _ := tx.Query(ctx,
		`SELECT product_id, SUM(qty)::int FROM transaction_items
		 WHERE transaction_id = $1 GROUP BY product_id ORDER BY product_id`, id)
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[lineItem])
	if err != nil {
		web.ServerError(w, r, err)
		return
	}
	for _, it := range items {
		_, err := tx.Exec(ctx,
			`UPDATE stocks SET qty = qty + $3 WHERE outlet_id = $1 AND product_id = $2`,
			outletID, it.ProductID, it.Qty)
		if err != nil {
			web.ServerError(w, r, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		web.ServerError(w, r, err)
		return
	}

	t, err := h.loadTransaction(r, id)
	if err != nil {
		web.ServerError(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, t)
}
