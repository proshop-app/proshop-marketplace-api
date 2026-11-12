package searchapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestCandidatesContract(t *testing.T) {
	fixture, err := os.ReadFile("testdata/attribute-values.json")
	if err != nil {
		t.Fatal(err)
	}
	token := "candidates-token-0123456789abcdef012345"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/internal/catalog/attribute-values" ||
			r.Header.Get("X-Admin-Id") != token || r.URL.Query().Get("key") != "b1" ||
			r.URL.Query().Get("size") != "500" || r.URL.Query().Get("after") != "next/+" {
			t.Error("нарушен контракт запроса")
		}
		query := r.URL.Query()
		if !reflect.DeepEqual(query["categoryId"], []string{"c1", "c2"}) ||
			!reflect.DeepEqual(query["excludeCategoryId"], []string{"c1.x", "c2.y"}) {
			t.Error("потеряны повторяемые параметры разделов")
		}
		_, _ = w.Write(fixture)
	}))
	defer server.Close()
	client, err := NewCandidates(Config{URL: server.URL, Token: token, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.AttributeValues(context.Background(), ports.AttributeValuesQuery{
		Attribute:         "b1",
		Size:              500,
		After:             "next/+",
		Categories:        []string{"c1", "c2"},
		ExcludeCategories: []string{"c1.x", "c2.y"},
	})
	if err != nil || page.After != "next/+" || len(page.Items) != 1 || page.Items[0].Documents != 12 {
		t.Fatalf("страница: %+v, %v", page, err)
	}
}

func TestCandidatesErrors(t *testing.T) {
	for _, status := range []int{400, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(status)
			}))
			defer server.Close()
			client, err := NewCandidates(Config{
				URL:     server.URL,
				Token:   "candidates-token-0123456789abcdef012345",
				Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			query := ports.AttributeValuesQuery{Attribute: "b1", Size: 500}
			if _, err := client.AttributeValues(context.Background(), query); err == nil {
				t.Fatal("ошибка поиска потеряна")
			}
		})
	}
}

func TestCandidateQueryValuesWithoutSockets(t *testing.T) {
	query := ports.AttributeValuesQuery{
		Attribute:         "a1",
		Size:              500,
		After:             "next/+",
		Categories:        []string{"c1", "c2"},
		ExcludeCategories: []string{"c1.x", "c2.y"},
	}
	values, err := url.ParseQuery(candidateQueryValues(query).Encode())
	if err != nil {
		t.Fatal(err)
	}
	expected := url.Values{
		"key":               {"a1"},
		"size":              {"500"},
		"after":             {"next/+"},
		"categoryId":        {"c1", "c2"},
		"excludeCategoryId": {"c1.x", "c2.y"},
	}
	if !reflect.DeepEqual(values, expected) {
		t.Fatal("нарушен контракт фильтров или курсора", values)
	}
	global := candidateQueryValues(ports.AttributeValuesQuery{Attribute: "a1", Size: 500})
	if global.Has("categoryId") || global.Has("excludeCategoryId") {
		t.Fatal("пустые фильтры должны отсутствовать", global)
	}
}
