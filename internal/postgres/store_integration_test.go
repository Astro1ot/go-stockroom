package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Astro1ot/go-stockroom/internal/shop"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for real PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := pgx.Identifier{fmt.Sprintf("stockroom_test_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("cleanup: %v", err)
		}
		admin.Close()
	})
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := &Store{Pool: pool}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	return store
}
func TestIntegrationRollbackIsAtomic(t *testing.T) {
	store := testStore(t)
	service := shop.Service{Store: store}
	ctx := context.Background()
	_, _, err := service.Create(ctx, "rollback", shop.CreateRequest{Customer: "Alice", Items: []shop.Item{{ProductID: 1, Quantity: 5}, {ProductID: 2, Quantity: 999}}})
	if !errors.Is(err, shop.ErrStock) {
		t.Fatalf("expected stock conflict, got %v", err)
	}
	stock, _ := store.Stock(ctx, 1)
	if stock != 10 {
		t.Fatalf("partial deduction: %d", stock)
	}
	var count int
	if err := store.Pool.QueryRow(ctx, "SELECT count(*) FROM orders").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial order: %d %v", count, err)
	}
	if _, _, err := service.Create(ctx, "rollback", shop.CreateRequest{Customer: "Alice", Items: []shop.Item{{ProductID: 1, Quantity: 1}}}); err != nil {
		t.Fatalf("rolled-back key should be reusable: %v", err)
	}
}
func TestIntegrationConcurrentLastItem(t *testing.T) {
	store := testStore(t)
	service := shop.Service{Store: store}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := store.Pool.Exec(ctx, "UPDATE products SET stock=1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 12)
	for i := range 12 {
		go func(i int) {
			<-start
			_, _, err := service.Create(ctx, fmt.Sprintf("last-%d", i), shop.CreateRequest{Customer: "Alice", Items: []shop.Item{{ProductID: 1, Quantity: 1}}})
			results <- err
		}(i)
	}
	close(start)
	success, conflicts := 0, 0
	for range 12 {
		err := <-results
		switch {
		case err == nil:
			success++
		case errors.Is(err, shop.ErrStock):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	stock, err := store.Stock(ctx, 1)
	if success != 1 || conflicts != 11 || stock != 0 || err != nil {
		t.Fatalf("success=%d conflicts=%d stock=%d err=%v", success, conflicts, stock, err)
	}
}
func TestIntegrationIdempotencyAndConcurrentCancellation(t *testing.T) {
	store := testStore(t)
	service := shop.Service{Store: store}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request := shop.CreateRequest{Customer: "Alice", Items: []shop.Item{{ProductID: 1, Quantity: 2}, {ProductID: 2, Quantity: 1}}}
	type outcome struct {
		order  shop.Order
		replay bool
		err    error
	}
	results := make(chan outcome, 8)
	start := make(chan struct{})
	for range 8 {
		go func() { <-start; o, r, e := service.Create(ctx, "same-key", request); results <- outcome{o, r, e} }()
	}
	close(start)
	var id int64
	originals := 0
	for range 8 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if !result.replay {
			originals++
		}
		if id != 0 && id != result.order.ID {
			t.Fatal("duplicate order")
		}
		id = result.order.ID
		if result.order.TotalCents != 1590000 || len(result.order.Items) != 2 {
			t.Fatalf("invalid joined order: %+v", result.order)
		}
	}
	if originals != 1 {
		t.Fatalf("created %d orders", originals)
	}
	stock, _ := store.Stock(ctx, 1)
	if stock != 8 {
		t.Fatalf("deducted more than once: %d", stock)
	}
	_, _, err := service.Create(ctx, "same-key", shop.CreateRequest{Customer: "Bob", Items: request.Items})
	if !errors.Is(err, shop.ErrConflict) {
		t.Fatalf("payload conflict: %v", err)
	}
	var wg sync.WaitGroup
	cancelErrors := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := store.Cancel(ctx, id); cancelErrors <- err }()
	}
	wg.Wait()
	close(cancelErrors)
	for err := range cancelErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	stock, _ = store.Stock(ctx, 1)
	if stock != 10 {
		t.Fatalf("stock restored incorrectly: %d", stock)
	}
	order, replay, err := service.Create(ctx, "same-key", request)
	if err != nil || !replay || order.Status != "cancelled" {
		t.Fatalf("replay must not resurrect cancelled order: %+v %v", order, err)
	}
}
func TestIntegrationPriceSnapshot(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	order, _, err := (shop.Service{Store: store}).Create(ctx, "price", shop.CreateRequest{Customer: "Alice", Items: []shop.Item{{ProductID: 1, Quantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, "UPDATE products SET price_cents=1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Get(ctx, order.ID)
	if err != nil || saved.Items[0].UnitPriceCents != 750000 || saved.TotalCents != 750000 {
		t.Fatalf("historical price changed: %+v %v", saved, err)
	}
}
