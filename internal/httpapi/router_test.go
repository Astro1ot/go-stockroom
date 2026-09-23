package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Astro1ot/go-stockroom/internal/catalog"
	"github.com/Astro1ot/go-stockroom/internal/shop"
	"github.com/gin-gonic/gin"
)

// Embedding causes any unexpected database call to fail the validation test.
type fakeStore struct {
	shop.Store
	createErr error
	replay    bool
	creates   int
}

func (s *fakeStore) Create(context.Context, string, string, shop.CreateRequest) (shop.Order, bool, error) {
	s.creates++
	return shop.Order{ID: 7, Items: []shop.Line{}}, s.replay, s.createErr
}
func TestRejectBadRequestsBeforeDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fakeStore{}
	router := Router(store, catalog.Catalog{})
	for _, body := range []string{`{`, `null`, `{}`, `{"customer":"A","items":[{"product_id":1,"quantity":-1}]}`, `{"customer":"A","items":[{"product_id":1,"quantity":1}],"admin":true}`, `{} {}`, strings.Repeat("x", 65537)} {
		request := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(body))
		request.Header.Set("Idempotency-Key", "key")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != 400 {
			t.Fatalf("body %.80s => %d", body, response.Code)
		}
	}
	if store.creates != 0 {
		t.Fatal("invalid input reached database")
	}
}
func TestCreateStatusAndReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		replay bool
		err    error
		code   int
	}{{"created", false, nil, 201}, {"replayed", true, nil, 200}, {"stock conflict", false, shop.ErrStock, 409}, {"key conflict", false, shop.ErrConflict, 409}, {"unknown product", false, shop.ErrNotFound, 404}} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{replay: tc.replay, createErr: tc.err}
			router := Router(store, catalog.Catalog{})
			request := httptest.NewRequest("POST", "/orders", strings.NewReader(`{"customer":"Alice","items":[{"product_id":1,"quantity":1}]}`))
			request.Header.Set("Idempotency-Key", "test")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.code {
				t.Fatalf("got %d: %s", response.Code, response.Body.String())
			}
			if tc.replay && response.Header().Get("Idempotency-Replayed") != "true" {
				t.Fatal("missing replay header")
			}
		})
	}
}
