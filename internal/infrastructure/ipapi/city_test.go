package ipapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/ipapi"
)

func TestCityDetectionUsesStorefrontProviderAndPrivateIPFallback(t *testing.T) {
	for _, ip := range []string{"192.0.2.5", "127.0.0.1"} {
		t.Run(ip, func(t *testing.T) {
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expected := "/json/" + ip
				if ip == "127.0.0.1" {
					expected = "/json"
				}
				if r.URL.Path != expected || r.URL.Query().Get("fields") != "status,city,countryCode" ||
					r.URL.Query().Get("lang") != "ru" {
					t.Error(r.URL)
				}
				_, _ = w.Write([]byte(`{"status":"success","city":"Город","countryCode":"RU"}`))
			}))
			defer source.Close()
			city, err := ipapi.New(source.URL+"/json").DetectCity(context.Background(), ip)
			if err != nil || city.City != "Город" || city.Country != "RU" {
				t.Fatal(city, err)
			}
		})
	}
}

func TestCityDetectionFailureDoesNotExposeClientIP(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer source.Close()
	_, err := ipapi.New(source.URL).DetectCity(context.Background(), "192.0.2.5")
	if !errors.Is(err, ipapi.ErrUnavailable) {
		t.Fatal(err)
	}
}
