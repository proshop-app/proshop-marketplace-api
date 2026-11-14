package platformapi

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type pickupTransport struct {
	t       *testing.T
	fixture string
}

func (p pickupTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	p.t.Helper()
	if r.Method != http.MethodGet || r.URL.Path != "/api/v1/internal/marketplace/tenants/t1/delivery/pickup-points" {
		p.t.Fatal(r.Method, r.URL)
	}
	if r.URL.Query().Get("carrier") != "cdek" || r.URL.Query().Get("city") != "Город & район" ||
		r.Header.Get(HeaderToken) != "service-token" {
		p.t.Fatal(r.URL, r.Header)
	}
	body, err := os.ReadFile("testdata/" + p.fixture)
	if err != nil {
		p.t.Fatal(err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Header:     make(http.Header),
	}, nil
}

func TestPickupPointsContract(t *testing.T) {
	for _, available := range []bool{true, false} {
		fixture := "pickups.json"
		if !available {
			fixture = "pickups_unavailable.json"
		}
		client := New(Config{BaseURL: "http://server.test", Token: "service-token"})
		client.http.Transport = pickupTransport{t: t, fixture: fixture}
		answer, err := client.PickupPoints(context.Background(), "t1", "cdek", "Город & район")
		if err != nil || answer.Available != available {
			t.Fatal(answer, err)
		}
		if available {
			if len(answer.Items) != 1 || answer.Items[0].RegionCode != "RU-AD" || answer.Items[0].Schedule != "" {
				t.Fatal(answer)
			}
		} else if answer.Reason != "carrier_not_connected" {
			t.Fatal(answer)
		}
	}
}
