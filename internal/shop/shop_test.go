package shop

import (
	"errors"
	"testing"
)

func TestNormalizeEquivalentRequests(t *testing.T) {
	a := CreateRequest{Customer: " Alice ", Items: []Item{{2, 1}, {1, 2}, {2, 3}}}
	b := CreateRequest{Customer: "Alice", Items: []Item{{1, 2}, {2, 4}}}
	normalized, hashA, err := Normalize("req-1", a)
	if err != nil {
		t.Fatal(err)
	}
	_, hashB, err := Normalize("req-1", b)
	if err != nil {
		t.Fatal(err)
	}
	if hashA != hashB || normalized.Items[0].ProductID != 1 || normalized.Items[1].Quantity != 4 {
		t.Fatalf("canonicalization failed: %+v", normalized)
	}
	b.Items[1].Quantity++
	_, hashC, _ := Normalize("req-1", b)
	if hashC == hashA {
		t.Fatal("different payload must have a different hash")
	}
}
func TestNormalizeRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name, key string
		input     CreateRequest
	}{
		{"missing key", "", CreateRequest{"Alice", []Item{{1, 1}}}},
		{"unsafe key", "spaces not allowed", CreateRequest{"Alice", []Item{{1, 1}}}},
		{"empty customer", "key", CreateRequest{"  ", []Item{{1, 1}}}},
		{"no items", "key", CreateRequest{"Alice", nil}},
		{"negative quantity", "key", CreateRequest{"Alice", []Item{{1, -1}}}},
		{"invalid ID", "key", CreateRequest{"Alice", []Item{{0, 1}}}},
		{"merged overflow", "key", CreateRequest{"Alice", []Item{{1, 600}, {1, 600}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Normalize(tc.key, tc.input)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
