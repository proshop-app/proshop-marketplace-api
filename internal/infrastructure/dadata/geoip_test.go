package dadata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func fixtureClient(t *testing.T, handler http.HandlerFunc) *GeoIP {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New("secret", server.URL+"/iplocate/address")
}

func TestLocateFixtures(t *testing.T) {
	for _, fixture := range []string{"region", "none", "unknown"} {
		t.Run(fixture, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/" + fixture + ".json")
			if err != nil {
				t.Fatal(err)
			}
			client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/iplocate/address" || r.Method != http.MethodGet ||
					r.Header.Get("Authorization") != "Token secret" || r.URL.Query().Get("ip") != "192.0.2.1" {
					t.Errorf("неверный запрос")
				}
				_, _ = w.Write(raw)
			})
			region, err := client.Locate(context.Background(), "192.0.2.1")
			if err != nil {
				t.Fatal(err)
			}
			if (region != nil) != (fixture == "region") {
				t.Fatalf("регион: %+v", region)
			}
			if region != nil && region.Code != "RU-AD" {
				t.Fatal(region)
			}
		})
	}
}

func TestDisabledAndFailure(t *testing.T) {
	calls := 0
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	})
	ctx := context.Background()
	region, err := New("", "https://geo.example").Locate(ctx, "192.0.2.1")
	if err != nil || region != nil || calls != 0 {
		t.Fatal("без ключа не должно быть запроса")
	}
	if _, err := client.Locate(ctx, "192.0.2.1"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
