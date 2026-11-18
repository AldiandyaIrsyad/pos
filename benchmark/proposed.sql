SELECT p.id, p.name, s.qty_sold, s.revenue
FROM (
  SELECT ti.product_id,
         SUM(ti.qty)      AS qty_sold,
         SUM(ti.subtotal) AS revenue
  FROM transactions t
  JOIN transaction_items ti ON ti.transaction_id = t.id
  WHERE t.outlet_id = 1
    AND t.status = 'PAID'
    AND t.created_at >= '2026-01-01'
    AND t.created_at <  '2026-04-01'
  GROUP BY ti.product_id
  ORDER BY qty_sold DESC, ti.product_id
  LIMIT 10
) s
JOIN products p ON p.id = s.product_id
ORDER BY s.qty_sold DESC, s.product_id;