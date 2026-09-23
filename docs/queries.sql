-- Run in psql against the local demo database after creating orders.
-- A JOIN reports historical line prices rather than today's product prices.
SELECT o.id, o.customer, o.status, p.name, i.quantity, i.unit_price_cents,
       i.quantity * i.unit_price_cents AS line_total_cents
FROM orders o
JOIN order_items i ON i.order_id = o.id
JOIN products p ON p.id = i.product_id
ORDER BY o.created_at DESC, o.id DESC, i.product_id;

-- Inspect actual row counts, timing and buffer usage. For a tiny demo table,
-- a sequential scan can be cheaper than using the index; that is expected.
EXPLAIN (ANALYZE, BUFFERS)
SELECT id, status, total_cents, created_at
FROM orders
WHERE customer = 'Demo Customer'
ORDER BY created_at DESC
LIMIT 20;
