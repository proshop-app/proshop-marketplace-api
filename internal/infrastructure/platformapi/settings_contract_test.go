package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"
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
