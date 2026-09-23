package shop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalid  = errors.New("invalid request")
	ErrNotFound = errors.New("not found")
	ErrStock    = errors.New("insufficient stock")
	ErrConflict = errors.New("idempotency key already used for a different request")
	keyPattern  = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
)

type Product struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
}
type Item struct {
	ProductID int64 `json:"product_id"`
	Quantity  int64 `json:"quantity"`
}
type CreateRequest struct {
	Customer string `json:"customer"`
	Items    []Item `json:"items"`
}
type Line struct {
	ProductID      int64  `json:"product_id"`
	Name           string `json:"name"`
	Quantity       int64  `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
}
type Order struct {
	ID         int64     `json:"id"`
	Customer   string    `json:"customer"`
	Status     string    `json:"status"`
	TotalCents int64     `json:"total_cents"`
	CreatedAt  time.Time `json:"created_at"`
	Items      []Line    `json:"items"`
}

// Store exposes business operations rather than a generic CRUD interface.
type Store interface {
	Products(context.Context) ([]Product, error)
	Stock(context.Context, int64) (int64, error)
	Create(context.Context, string, string, CreateRequest) (Order, bool, error)
	Get(context.Context, int64) (Order, error)
	Cancel(context.Context, int64) (Order, error)
	Ping(context.Context) error
}

type Service struct{ Store Store }

func Normalize(key string, request CreateRequest) (CreateRequest, string, error) {
	request.Customer = strings.TrimSpace(request.Customer)
	if !keyPattern.MatchString(key) || len(request.Customer) == 0 || len(request.Customer) > 100 || len(request.Items) == 0 || len(request.Items) > 50 {
		return CreateRequest{}, "", fmt.Errorf("%w: key 1-64 ASCII letters/digits/_/-, customer 1-100 bytes, 1-50 items required", ErrInvalid)
	}
	totals := make(map[int64]int64)
	for _, item := range request.Items {
		if item.ProductID <= 0 || item.Quantity < 1 || item.Quantity > 1000 {
			return CreateRequest{}, "", fmt.Errorf("%w: positive product_id and quantity 1-1000 required", ErrInvalid)
		}
		totals[item.ProductID] += item.Quantity
		if totals[item.ProductID] > 1000 {
			return CreateRequest{}, "", fmt.Errorf("%w: total quantity per product exceeds 1000", ErrInvalid)
		}
	}
	request.Items = make([]Item, 0, len(totals))
	for id, quantity := range totals {
		request.Items = append(request.Items, Item{ProductID: id, Quantity: quantity})
	}
	// Stable ordering is used both for hashing and for acquiring database locks.
	sort.Slice(request.Items, func(i, j int) bool { return request.Items[i].ProductID < request.Items[j].ProductID })
	encoded, _ := json.Marshal(request)
	hash := sha256.Sum256(encoded)
	return request, hex.EncodeToString(hash[:]), nil
}

func (s Service) Create(ctx context.Context, key string, request CreateRequest) (Order, bool, error) {
	normalized, hash, err := Normalize(key, request)
	if err != nil {
		return Order{}, false, err
	}
	return s.Store.Create(ctx, key, hash, normalized)
}
