package platformapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/platformapi"
)

func TestCitySuggestionsUseExistingInternalClientWithoutShop(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/marketplace/delivery/cities" ||
			r.URL.Query().Get("value") != "Город" || r.Header.Get(platformapi.HeaderToken) != "token" ||
			r.Header.Get(platformapi.HeaderDeadline) == "" {
			t.Error(r.URL, r.Header)
		}
		deadline, err := strconv.ParseInt(r.Header.Get(platformapi.HeaderDeadline), 10, 64)
		if err != nil || time.Until(time.UnixMilli(deadline)) > time.Second {
			t.Error("срок источника не должен превышать срок клиента", deadline, err)
		}
		_, _ = w.Write([]byte(`[{"city":"Город","label":"Город, регион","fiasId":"city-id"}]`))
	}))
	defer source.Close()
	client := platformapi.New(platformapi.Config{BaseURL: source.URL, Token: "token", Timeout: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	items, err := client.SuggestCities(ctx, "Город")
	if err != nil || len(items) != 1 || items[0].FiasID != "city-id" {
		t.Fatal(items, err)
	}
}
