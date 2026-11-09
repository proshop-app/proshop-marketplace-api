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
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
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
	for _, value := range []string{
		`null`,
		`{"background":"#125f67","logoBackground":"#e9eee7","textColor":"white"}`,
		`{}`,
		`{"background":"#FFFFFF","logoBackground":"#E9EEE7","textColor":"white"}`,
		`{"background":"#125F67","textColor":"white"}`,
	} {
		var payload map[string]any
		if err := json.Unmarshal(original, &payload); err != nil {
			t.Fatal(err)
		}
		var appearance any
		if err := json.Unmarshal([]byte(value), &appearance); err != nil {
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
		valid := value == `null` || bytes.Contains([]byte(value), []byte("#125f67"))
		if valid {
			if err != nil || actual.Profile.Appearance != participant.DefaultCardAppearance() || !actual.Card.Ready {
				t.Fatal(actual, err)
			}
		} else if !errors.Is(err, ports.ErrInvalidSettings) {
			t.Fatal(value, err)
		}
	}
}
