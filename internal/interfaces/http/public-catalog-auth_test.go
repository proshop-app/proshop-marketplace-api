package http_test

import (
	"net/http"
	"testing"
)

// Публичный каталог не требует входа: недействительный или истёкший access
// означает гостя, а не 401. Иначе устаревшая cookie ломает выдачу до перезагрузки.
func TestPublicCatalogTreatsInvalidAccessAsGuest(t *testing.T) {
	e := newEnv(t)
	paths := []string{
		"/api/v1/catalog/offers?limit=1",
	}
	for _, path := range paths {
		result := e.session(t, http.MethodGet, path, "", "invalid")
		if result.status != http.StatusOK {
			t.Fatalf("%s: %+v", path, result)
		}
	}
	// Подборке нужен контекст; здесь важно только, что вход не проверяется строго.
	result := e.session(t, http.MethodGet, "/api/v1/recommendations?limit=1", "", "invalid")
	if result.status == http.StatusUnauthorized {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodGet, "/api/v1/catalog/offers?limit=1", "", "")
	if result.status != http.StatusOK {
		t.Fatal(result)
	}
	if items, _ := result.body["items"].([]any); len(items) > 0 {
		id, _ := items[0].(map[string]any)["id"].(string)
		card := e.session(t, http.MethodGet, "/api/v1/catalog/offers/"+id, "", "invalid")
		if card.status != http.StatusOK {
			t.Fatal(card)
		}
	}
}
