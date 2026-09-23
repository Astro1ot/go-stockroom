package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Astro1ot/go-stockroom/internal/catalog"
	"github.com/Astro1ot/go-stockroom/internal/shop"
	"github.com/gin-gonic/gin"
)

func Router(store shop.Store, products catalog.Catalog) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })
	r.GET("/readyz", func(c *gin.Context) {
		if err := store.Ping(c.Request.Context()); err != nil {
			c.JSON(503, gin.H{"error": "database unavailable"})
			return
		}
		c.JSON(200, gin.H{"status": "ready"})
	})
	r.GET("/products", func(c *gin.Context) {
		result, cache, err := products.Products(c.Request.Context())
		if err != nil {
			fail(c, err)
			return
		}
		c.Header("X-Cache", cache)
		c.JSON(200, result)
	})
	r.GET("/products/:id/stock", func(c *gin.Context) {
		id, ok := identifier(c)
		if !ok {
			return
		}
		stock, err := store.Stock(c.Request.Context(), id)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, gin.H{"product_id": id, "stock": stock})
	})
	r.POST("/orders", func(c *gin.Context) {
		var request shop.CreateRequest
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON body (maximum 64 KiB)"})
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			c.JSON(400, gin.H{"error": "exactly one JSON object required"})
			return
		}
		order, replayed, err := (shop.Service{Store: store}).Create(c.Request.Context(), c.GetHeader("Idempotency-Key"), request)
		if err != nil {
			fail(c, err)
			return
		}
		status := http.StatusCreated
		if replayed {
			status = http.StatusOK
			c.Header("Idempotency-Replayed", "true")
		}
		c.Header("Location", "/orders/"+strconv.FormatInt(order.ID, 10))
		c.JSON(status, order)
	})
	r.GET("/orders/:id", func(c *gin.Context) {
		id, ok := identifier(c)
		if !ok {
			return
		}
		order, err := store.Get(c.Request.Context(), id)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, order)
	})
	r.POST("/orders/:id/cancel", func(c *gin.Context) {
		id, ok := identifier(c)
		if !ok {
			return
		}
		order, err := store.Cancel(c.Request.Context(), id)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, order)
	})
	return r
}
func identifier(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(400, gin.H{"error": "positive integer ID required"})
		return 0, false
	}
	return id, true
}
func fail(c *gin.Context, err error) {
	code, message := 500, "internal server error"
	switch {
	case errors.Is(err, shop.ErrInvalid):
		code, message = 400, err.Error()
	case errors.Is(err, shop.ErrNotFound):
		code, message = 404, "resource not found"
	case errors.Is(err, shop.ErrStock), errors.Is(err, shop.ErrConflict):
		code, message = 409, err.Error()
	case errors.Is(err, context.DeadlineExceeded):
		code, message = 504, "request timed out"
	case errors.Is(err, context.Canceled):
		code, message = 408, "request cancelled"
	default:
		slog.Error("request failed", "error", err)
	}
	c.JSON(code, gin.H{"error": message})
}
