package http

import (
	"context"
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
	handler := NewHandler(Deps{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Taxonomy:      apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil),
		OperatorToken: "token",
		Registerer:    prometheus.NewRegistry(),
	})
	for _, test := range []struct {
		token    string
		query    string
		code     int
		contains string
	}{
		{code: 401},
		{token: "token", query: "?limit=1", code: 200, contains: "decode: invalid assets"},
		{token: "token", query: "?afterTenant=t1&afterProduct=p1", code: 200, contains: `"items":[]`},
		{token: "token", query: "?limit=101", code: 400},
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
