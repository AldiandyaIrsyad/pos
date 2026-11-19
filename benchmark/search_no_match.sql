SELECT p.id, p.sku, p.name, p.category, p.price, s.qty
FROM products p
JOIN stocks s ON s.outlet_id = 1 AND s.product_id = p.id
WHERE p.id > 0
  AND (p.name ILIKE '%zzzz%' OR p.sku ILIKE '%zzzz%')
ORDER BY p.id
LIMIT 21;
