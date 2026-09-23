package catalog

import (
	"context"
	"testing"
	"time"

	"github.com/Astro1ot/go-stockroom/internal/shop"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type source struct{ calls int }

func (s *source) Products(context.Context) ([]shop.Product, error) {
	s.calls++
	return []shop.Product{{ID: 1, Name: "Book", PriceCents: 1200}}, nil
}

func TestCacheMissHitExpiryAndOutage(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1, DialTimeout: 50 * time.Millisecond, ReadTimeout: 50 * time.Millisecond, WriteTimeout: 50 * time.Millisecond, ContextTimeoutEnabled: true})
	defer client.Close()
	data := &source{}
	catalog := Catalog{Source: data, Redis: client, TTL: time.Second}
	ctx := context.Background()
	for _, want := range []string{"MISS", "HIT"} {
		products, got, err := catalog.Products(ctx)
		if err != nil || got != want || len(products) != 1 {
			t.Fatalf("got %s %v %v", got, products, err)
		}
	}
	if data.calls != 1 {
		t.Fatalf("DB called %d times", data.calls)
	}
	server.FastForward(2 * time.Second)
	_, state, err := catalog.Products(ctx)
	if err != nil || state != "MISS" || data.calls != 2 {
		t.Fatal("expired cache was used")
	}
	server.Set(key, "broken JSON")
	_, state, err = catalog.Products(ctx)
	if err != nil || state != "MISS" {
		t.Fatal("invalid cache must fall back to database")
	}
	server.Close()
	products, state, err := catalog.Products(ctx)
	if err != nil || state != "MISS" || len(products) != 1 {
		t.Fatalf("outage should degrade to DB: %v", err)
	}
}
