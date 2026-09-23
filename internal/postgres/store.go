package postgres

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/Astro1ot/go-stockroom/internal/shop"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

type Store struct{ Pool *pgxpool.Pool }

func (s *Store) Migrate(ctx context.Context) error { _, err := s.Pool.Exec(ctx, schema); return err }
func (s *Store) Seed(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO products(id,name,price_cents,stock) VALUES
  (1,'Mechanical keyboard',750000,10),(2,'USB-C cable',90000,20),(3,'Notebook',25000,30)
  ON CONFLICT(id) DO NOTHING;
  SELECT setval(pg_get_serial_sequence('products','id'), (SELECT MAX(id) FROM products));`)
	return err
}
func (s *Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }
func (s *Store) Products(ctx context.Context) ([]shop.Product, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,name,price_cents FROM products ORDER BY id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]shop.Product, 0)
	for rows.Next() {
		var p shop.Product
		if err := rows.Scan(&p.ID, &p.Name, &p.PriceCents); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}
func (s *Store) Stock(ctx context.Context, id int64) (int64, error) {
	var stock int64
	err := s.Pool.QueryRow(ctx, `SELECT stock FROM products WHERE id=$1`, id).Scan(&stock)
	return stock, mapError(err)
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// One JOIN yields a consistent order snapshot, even during concurrent cancellation.
func readOrder(ctx context.Context, q queryer, id int64) (shop.Order, error) {
	rows, err := q.Query(ctx, `SELECT o.id,o.customer,o.status,o.total_cents,o.created_at,
  i.product_id,p.name,i.quantity,i.unit_price_cents
  FROM orders o JOIN order_items i ON i.order_id=o.id JOIN products p ON p.id=i.product_id
  WHERE o.id=$1 ORDER BY i.product_id`, id)
	if err != nil {
		return shop.Order{}, err
	}
	defer rows.Close()
	var order shop.Order
	order.Items = make([]shop.Line, 0)
	for rows.Next() {
		var line shop.Line
		if err := rows.Scan(&order.ID, &order.Customer, &order.Status, &order.TotalCents, &order.CreatedAt, &line.ProductID, &line.Name, &line.Quantity, &line.UnitPriceCents); err != nil {
			return shop.Order{}, err
		}
		order.Items = append(order.Items, line)
	}
	if err := rows.Err(); err != nil {
		return shop.Order{}, err
	}
	if order.ID == 0 {
		return shop.Order{}, shop.ErrNotFound
	}
	return order, nil
}
func (s *Store) Get(ctx context.Context, id int64) (shop.Order, error) {
	return readOrder(ctx, s.Pool, id)
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func (s *Store) Create(ctx context.Context, key, hash string, req shop.CreateRequest) (shop.Order, bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return shop.Order{}, false, err
	}
	defer rollback(tx)
	var id int64
	// The unique index serializes simultaneous requests with the same key.
	err = tx.QueryRow(ctx, `INSERT INTO orders(idempotency_key,request_hash,customer,status)
  VALUES($1,$2,$3,'reserved') ON CONFLICT(idempotency_key) DO NOTHING RETURNING id`, key, hash, req.Customer).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		var existingHash string
		if err = tx.QueryRow(ctx, `SELECT id,request_hash FROM orders WHERE idempotency_key=$1`, key).Scan(&id, &existingHash); err != nil {
			return shop.Order{}, false, err
		}
		if existingHash != hash {
			return shop.Order{}, false, shop.ErrConflict
		}
		order, err := readOrder(ctx, tx, id)
		return order, true, err
	}
	if err != nil {
		return shop.Order{}, false, err
	}
	var total int64
	// req.Items is normalized by Service: duplicates merged and IDs sorted.
	for _, item := range req.Items {
		var stock, price int64
		if err := tx.QueryRow(ctx, `SELECT stock,price_cents FROM products WHERE id=$1 FOR UPDATE`, item.ProductID).Scan(&stock, &price); err != nil {
			return shop.Order{}, false, mapError(err)
		}
		if stock < item.Quantity {
			return shop.Order{}, false, fmt.Errorf("%w: product %d", shop.ErrStock, item.ProductID)
		}
		if _, err := tx.Exec(ctx, `UPDATE products SET stock=stock-$1 WHERE id=$2`, item.Quantity, item.ProductID); err != nil {
			return shop.Order{}, false, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO order_items(order_id,product_id,quantity,unit_price_cents) VALUES($1,$2,$3,$4)`, id, item.ProductID, item.Quantity, price); err != nil {
			return shop.Order{}, false, err
		}
		total += price * item.Quantity
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET total_cents=$1 WHERE id=$2`, total, id); err != nil {
		return shop.Order{}, false, err
	}
	order, err := readOrder(ctx, tx, id)
	if err != nil {
		return shop.Order{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return shop.Order{}, false, err
	}
	return order, false, nil
}

func (s *Store) Cancel(ctx context.Context, id int64) (shop.Order, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return shop.Order{}, err
	}
	defer rollback(tx)
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1 FOR UPDATE`, id).Scan(&status); err != nil {
		return shop.Order{}, mapError(err)
	}
	order, err := readOrder(ctx, tx, id)
	if err != nil {
		return shop.Order{}, err
	}
	if status == "cancelled" {
		return order, nil
	}
	// readOrder returns lines sorted by product_id, matching Create's lock order.
	for _, line := range order.Items {
		if _, err := tx.Exec(ctx, `UPDATE products SET stock=stock+$1 WHERE id=$2`, line.Quantity, line.ProductID); err != nil {
			return shop.Order{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET status='cancelled' WHERE id=$1`, id); err != nil {
		return shop.Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return shop.Order{}, err
	}
	order.Status = "cancelled"
	return order, nil
}
func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return shop.ErrNotFound
	}
	return err
}
