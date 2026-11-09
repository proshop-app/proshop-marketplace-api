package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestSettingsIgnoreRemovedShowcase(t *testing.T) {
	raw, err := os.ReadFile("testdata/settings_marketplace.json")
	if err != nil {
		t.Fatal(err)
	}
	client := New(Config{BaseURL: "http://platform.test", Token: "service-token", Timeout: time.Second})
	client.http.Transport = reviewTransport(func(_ *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		_, _ = response.Write(raw)
		return response.Result(), nil
	})
	expected, err := client.MarketplaceSettings(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(raw)
	for _, value := range []string{`{"mode":"pinned","productIds":["p1"]}`, `"invalid"`, `null`} {
		raw = bytes.Replace(original, []byte(`"profile": {`), []byte(`"profile": {"showcase":`+value+`,`), 1)
		actual, err := client.MarketplaceSettings(context.Background(), "t1")
		if err != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatal(actual, err)
		}
		encoded, err := json.Marshal(actual.Profile)
		if err != nil || bytes.Contains(encoded, []byte(`"showcase"`)) {
			t.Fatal(string(encoded), err)
		}
	}
}

func TestSettingsCardAppearanceContract(t *testing.T) {
	original, err := os.ReadFile("testdata/settings_marketplace.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	client := New(Config{BaseURL: "http://platform.test", Token: "service-token", Timeout: time.Second})
	client.http.Transport = reviewTransport(func(_ *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		_, _ = response.Write(raw)
		return response.Result(), nil
	})
	for _, item := range []struct {
		value string
		color string
	}{
		{value: `null`, color: "#125F67"},
		{value: `{"color":"#12efab"}`, color: "#12EFAB"},
		{value: `{"color":"#ffffff"}`, color: "#FFFFFF"},
		{value: `{"color":"#ABCDEF","background":"#FF0000"}`, color: "#ABCDEF"},
		{value: `{"background":"#e9eee7","logoBackground":{},"textColor":[]}`, color: "#E9EEE7"},
		{value: `{"color":"invalid","background":"#204a56"}`, color: "#204A56"},
		{value: `{}`},
		{value: `{"color":"#123"}`},
		{value: `{"background":"invalid"}`},
	} {
		var payload map[string]any
		if err := json.Unmarshal(original, &payload); err != nil {
			t.Fatal(err)
		}
		var appearance any
		if err := json.Unmarshal([]byte(item.value), &appearance); err != nil {
			t.Fatal(err)
		}
		profile := payload["profile"].(map[string]any)
		profile["appearance"] = appearance
		profile["color"] = map[string]any{"card": "#FF0000"}
		payload["card"] = map[string]any{"ready": false, "missing": []string{"brandColor", "brandColor"}}
		raw, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := client.MarketplaceSettings(context.Background(), "t1")
		if item.color == "" {
			if !errors.Is(err, ports.ErrInvalidSettings) {
				t.Fatal(item.value, err)
			}
			continue
		}
		if err != nil || actual.Profile.Appearance.Color != item.color || !actual.Card.Ready {
			t.Fatal(actual, err)
		}
		encoded, err := json.Marshal(actual.Profile.Appearance)
		if err != nil || string(encoded) != `{"color":"`+item.color+`"}` {
			t.Fatal(string(encoded), err)
		}
	}
}

func TestSettingsCardProductsContract(t *testing.T) {
	original, err := os.ReadFile("testdata/settings_marketplace.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	client := New(Config{BaseURL: "http://platform.test", Token: "service-token", Timeout: time.Second})
	client.http.Transport = reviewTransport(func(_ *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		_, _ = response.Write(raw)
		return response.Result(), nil
	})
	for _, item := range []struct {
		value string
		want  string
	}{
		{value: `null`, want: `{"enabled":true,"productIds":[]}`},
		{value: `{"enabled":true,"productIds":["p2","p1"]}`, want: `{"enabled":true,"productIds":["p2","p1"]}`},
		{value: `{"enabled":false,"productIds":["p1"]}`, want: `{"enabled":false,"productIds":[]}`},
		{value: `{}`},
		{value: `{"enabled":true}`},
		{value: `{"enabled":true,"productIds":null}`},
		{value: `{"enabled":true,"productIds":["p1","p1"]}`},
		{value: `{"enabled":true,"productIds":["p1","p2","p3","p4"]}`},
		{value: `{"enabled":true,"productIds":["invalid id"]}`},
	} {
		var payload map[string]any
		if err := json.Unmarshal(original, &payload); err != nil {
			t.Fatal(err)
		}
		var products any
		if err := json.Unmarshal([]byte(item.value), &products); err != nil {
			t.Fatal(err)
		}
		payload["products"] = products
		raw, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		settings, err := client.MarketplaceSettings(context.Background(), "t1")
		if item.want == "" {
			if !errors.Is(err, ports.ErrInvalidSettings) {
				t.Fatal(item.value, err)
			}
			continue
		}
		actual, marshalErr := json.Marshal(settings.Products)
		if err != nil || marshalErr != nil || string(actual) != item.want {
			t.Fatal(item.value, string(actual), err, marshalErr)
		}
	}
}
