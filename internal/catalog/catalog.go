package catalog

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Astro1ot/go-stockroom/internal/shop"
	"github.com/redis/go-redis/v9"
)

type Source interface {
	Products(context.Context) ([]shop.Product, error)
}
type Catalog struct {
	Source Source
	Redis  *redis.Client
	TTL    time.Duration
}

const key = "stockroom:catalog:v1"

// The cache contains catalogue descriptions/prices, never authoritative stock.
// A Redis outage only causes a cache miss; orders always use PostgreSQL.
func (c Catalog) Products(ctx context.Context) ([]shop.Product, string, error) {
	if c.Redis != nil {
		cacheCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		data, err := c.Redis.Get(cacheCtx, key).Bytes()
		cancel()
		var products []shop.Product
		if err == nil && json.Unmarshal(data, &products) == nil && products != nil {
			return products, "HIT", nil
		}
	}
	products, err := c.Source.Products(ctx)
	if err != nil {
		return nil, "MISS", err
	}
	if c.Redis != nil {
		data, err := json.Marshal(products)
		if err == nil {
			ttl := c.TTL
			if ttl <= 0 {
				ttl = 30 * time.Second
			}
			cacheCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
			_ = c.Redis.Set(cacheCtx, key, data, ttl).Err()
			cancel()
		}
	}
	return products, "MISS", nil
}
