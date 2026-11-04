package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/referencedata"
)

const referenceBundle = "../../internal/infrastructure/referencedata/testdata/b1"

func TestReferenceCheckDoesNotNeedDatabase(t *testing.T) {
	var output bytes.Buffer
	args := []string{"check", "--dir", referenceBundle}
	err := runReferenceDelivery(context.Background(), config.Config{}, args, &output)
	if err != nil {
		t.Fatal(err)
	}
	var summary referencedata.Summary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil || summary.Entries != 2 || summary.Release != 1 {
		t.Fatalf("отчёт check: %s %v", output.String(), err)
	}
}

func TestReferenceCommandRejectsUsageAndHidesCredentials(t *testing.T) {
	usages := [][]string{nil, {"unknown"}, {"apply"}, {"check", "--dir"}, {"check", "--dir", referenceBundle, "x"}}
	for _, args := range usages {
		err := runReferenceDelivery(context.Background(), config.Config{}, args, &bytes.Buffer{})
		if !errors.Is(err, errReferenceUsage) {
			t.Fatalf("%v: %v", args, err)
		}
	}
	url := "postgres://user:secret-value@127.0.0.1:1/db?connect_timeout=1"
	cfg := config.Config{Database: config.Database{URL: url}}
	err := runReferenceDelivery(context.Background(), cfg, []string{"apply", "--dir", referenceBundle}, &bytes.Buffer{})
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("ошибка базы: %v", err)
	}
}

func TestReferenceDeliverySendsOriginalFileToOwnerWithoutOpeningPG(t *testing.T) {
	original, err := os.ReadFile(referenceBundle + "/entries.json")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/reference/b1/deliveries/apply" ||
			r.Header.Get("Authorization") != "Bearer writer" || r.Header.Get("X-Reference-Actor") != "cli" {
			t.Errorf("запрос поставки: %s %v", r.URL, r.Header)
		}
		var body struct {
			Release     int64  `json:"release"`
			Checksum    string `json:"checksum"`
			EntriesJSON string `json:"entriesJson"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Release != 1 || len(body.Checksum) != 64 ||
			body.EntriesJSON != string(original) {
			t.Errorf("точное содержимое файла не сохранено: %+v %v", body, err)
		}
		_, _ = io.WriteString(w, `{"dictionary":"b1","release":1,"changed":true}`)
	}))
	defer server.Close()
	cfg := config.Config{
		Platform:  config.Platform{URL: server.URL},
		Reference: config.Reference{WriteToken: "writer"},
		Database:  config.Database{URL: "invalid PostgreSQL URL must never be opened"},
	}
	var output bytes.Buffer
	err = runReferenceDelivery(context.Background(), cfg, []string{"apply", "--dir", referenceBundle}, &output)
	if err != nil || !strings.Contains(output.String(), `"changed":true`) {
		t.Fatalf("поставка владельцу: %s %v", output.String(), err)
	}
}
