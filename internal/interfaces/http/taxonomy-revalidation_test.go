package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestRevalidationFailuresAreOperatorOnlyAndPaginated(t *testing.T) {
	store := fake.NewStore()
	store.SeedProduct(catalog.ProductState{TenantID: "t1", ProductID: "p1", Revision: 1, Version: 1})
	err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Revalidation().RecordFailures(ctx, []ports.RevalidationFailure{
			{
				TenantID:        "t1",
				ProductID:       "p1",
				ProductVersion:  1,
				SourceRevision:  1,
				TaxonomyVersion: 2,
				Diagnostic:      "decode: invalid assets",
				At:              time.Now(),
			},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := revalidationFailureHandler(store)
	for _, test := range []struct {
		token    string
		query    string
		code     int
		contains string
	}{
		{code: 401},
		{token: "wrong", code: 401},
		{token: "token", query: "?limit=1", code: 200, contains: "decode: invalid assets"},
		{token: "token", query: "?afterTenant=t1&afterProduct=p1", code: 200, contains: `"items":[]`},
		{token: "token", query: "?limit=101", code: 400},
		{token: "token", query: "?limit=0", code: 400},
		{token: "token", query: "?limit=-1", code: 400},
		{token: "token", query: "?limit=abc", code: 400},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/operator/taxonomy/revalidation-failures"+test.query, nil)
		if test.token != "" {
			req.Header.Set("Authorization", "Bearer "+test.token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != test.code || !strings.Contains(response.Body.String(), test.contains) {
			t.Fatal(response.Code, response.Body.String())
		}
	}
}

func TestRevalidationFailureResponseHasDefaultAndMaximum(t *testing.T) {
	store := fake.NewStore()
	failures := []ports.RevalidationFailure{}
	for i := 0; i < 130; i++ {
		tenant := ids.TenantID(fmt.Sprintf("t%d", i/65+1))
		product := ids.ProductID(fmt.Sprintf("p%03d", i%65))
		store.SeedProduct(catalog.ProductState{TenantID: tenant, ProductID: product, Revision: 1, Version: 1})
		failures = append(failures, ports.RevalidationFailure{
			TenantID:       tenant,
			ProductID:      product,
			SourceRevision: 1,
			ProductVersion: 1,
			Diagnostic:     strings.Repeat("я", 5000),
		})
	}
	err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Revalidation().RecordFailures(ctx, failures)
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := revalidationFailureHandler(store)
	for _, test := range []struct {
		query string
		count int
		last  string
	}{
		{count: 50, last: "t1~p049"},
		{query: "?limit=100", count: 100, last: "t2~p034"},
		{query: "?afterTenant=t2&afterProduct=p034", count: 30, last: "t2~p064"},
		{query: "?afterTenant=t2&afterProduct=p064", count: 0},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/operator/taxonomy/revalidation-failures"+test.query, nil)
		req.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		var body struct {
			Items []ports.RevalidationFailure `json:"items"`
			Next  *revalidationCursor         `json:"next"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || len(body.Items) != test.count {
			t.Fatal(response.Code, len(body.Items))
		}
		if strings.Contains(response.Body.String(), `"status":`) {
			t.Fatal("ответ содержит общий статус вместо записей отказов")
		}
		if test.count < 50 && body.Next != nil || test.count >= 50 && body.Next == nil {
			t.Fatal("неверный признак окончания страницы", body.Next)
		}
		if test.count > 0 {
			last := body.Items[len(body.Items)-1]
			if string(last.TenantID)+"~"+string(last.ProductID) != test.last {
				t.Fatal("страница перескочила ключ", last)
			}
			if body.Next != nil && (body.Next.TenantID != last.TenantID || body.Next.ProductID != last.ProductID) {
				t.Fatal("next не указывает на последнюю запись", body.Next)
			}
		}
	}
}

func revalidationFailureHandler(store *fake.Store) http.Handler {
	return NewHandler(Deps{
		RequestTimeout:         time.Minute,
		ReferenceImportTimeout: time.Minute,
		Logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
		Taxonomy:               apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil),
		OperatorToken:          "token",
		Registerer:             prometheus.NewRegistry(),
	})
}
