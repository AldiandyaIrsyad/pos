SELECT p.id, p.name,
  (SELECT SUM(ti.qty) FROM transaction_items ti
     JOIN transactions t ON t.id = ti.transaction_id
    WHERE ti.product_id = p.id
      AND t.outlet_id = 1
      AND DATE(t.created_at) BETWEEN '2026-01-01' AND '2026-03-31'
      AND t.status = 'PAID') AS qty_sold,
  (SELECT SUM(ti.subtotal) FROM transaction_items ti
     JOIN transactions t ON t.id = ti.transaction_id
    WHERE ti.product_id = p.id
      AND t.outlet_id = 1
      AND DATE(t.created_at) BETWEEN '2026-01-01' AND '2026-03-31'
      AND t.status = 'PAID') AS revenue
FROM products p
ORDER BY qty_sold DESC NULLS LAST
LIMIT 10;
