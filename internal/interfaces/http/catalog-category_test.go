package http

import (
	"net/http/httptest"
	"testing"
)

func TestOfferCategoryAliases(t *testing.T) {
	for _, raw := range []string{"category=c1", "categoryId=c1", "category=c1&categoryId=c1"} {
		query, err := parseOfferQuery(httptest.NewRequest("GET", "/?"+raw, nil))
		if err != nil || query.CategoryID != "c1" {
			t.Fatalf("%s: category=%q err=%v", raw, query.CategoryID, err)
		}
	}
	for _, raw := range []string{
		"category=c1&categoryId=c2",
		"category=&categoryId=c1",
		"category=c1&category=c2",
		"categoryId=c1&categoryId=c2",
	} {
		if _, err := parseOfferQuery(httptest.NewRequest("GET", "/?"+raw, nil)); err == nil {
			t.Fatalf("противоречивый фильтр принят: %s", raw)
		}
	}
}
