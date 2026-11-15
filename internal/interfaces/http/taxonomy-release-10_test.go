package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/taxonomyfile"
)

const releaseDataPath = "../../infrastructure/taxonomydata/"

type releaseSeedEntry struct {
	Canonical string          `json:"canonical"`
	Status    string          `json:"status"`
	Metadata  json.RawMessage `json:"metadata"`
}

func TestRelease10ActualFilesThroughHTTP(t *testing.T) {
	store := fake.NewStore()
	var previous taxonomy.Definition
	if err := json.Unmarshal(release10Read(t, "testdata/release-9.json"), &previous); err != nil {
		t.Fatal(err)
	}
	store.SeedTaxonomy(previous)
	attributes := release10Read(t, "data/attributes.yaml")
	input, err := taxonomyfile.DecodePart(bytes.NewReader(attributes), "attributes")
	if err != nil {
		t.Fatal(err)
	}
	generator := &fake.IDs{}
	seedRelease10Dictionaries(t, store, generator, input.Attributes, false)
	service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil)
	handler := NewHandler(Deps{
		Registerer:    prometheus.NewRegistry(),
		Taxonomy:      service,
		OperatorToken: "token",
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	call := func(part string, body []byte, version int64, query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/v1/operator/taxonomy/"+part+query, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer token")
		r.Header.Set("Content-Type", "application/yaml")
		r.Header.Set("If-Match", `"taxonomy-`+strconv.FormatInt(version, 10)+`"`)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	// Файлы не подменены маленькой моделью. Нет target_gender в deploy/reference:
	// ошибка реестра не должна частично опубликовать новые атрибуты.
	w := call("attributes", attributes, previous.Version, "")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "target_gender") {
		t.Fatal(w.Code, w.Body.String())
	}
	current, _ := service.Current(context.Background())
	if current.Version != previous.Version {
		t.Fatal("ошибка реестра изменила версию")
	}
	seedRelease10Dictionaries(t, store, generator, input.Attributes, true)
	for index, part := range []string{"attributes", "schemas", "categories"} {
		version := previous.Version + int64(index)
		body := release10Read(t, "data/"+part+".yaml")
		if len(body) > maxTaxonomyBody {
			t.Fatal(part, "превышает лимит HTTP")
		}
		query := ""
		if part == "categories" {
			w = call(part, body, version, "")
			if w.Code != http.StatusConflict {
				t.Fatal("замена дерева принята без подтверждения", w.Code, w.Body.String())
			}
			query = "?replaceCategories=true"
		}
		w = call(part, body, version, query)
		if w.Code != http.StatusOK || w.Header().Get("ETag") != `"taxonomy-`+strconv.FormatInt(version+1, 10)+`"` {
			t.Fatal(part, w.Code, w.Body.String())
		}
		if stale := call(part, body, version, query); stale.Code != http.StatusConflict {
			t.Fatal("потерян CAS", part, stale.Code)
		}
	}
	current, err = service.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, schema := range current.Schemas {
		if schema.Extends != "" || schema.Abstract {
			t.Fatal("API сохранил неразвёрнутую схему", name)
		}
	}
	if field := current.Fields()["size_footwear_label"]; field.Description == "" || !field.Variant {
		t.Fatal("потеряны свойства атрибута", field)
	}
	for _, part := range []string{"categories", "attributes", "schemas"} {
		body, err := taxonomyfile.EncodePart(current, part)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := taxonomyfile.DecodePart(bytes.NewReader(body), part); err != nil {
			t.Fatal(part, "не проходит обратная выгрузка", err)
		}
	}
	for _, query := range []string{"?replaceCategories=yes", "?replaceCategories=true&replaceCategories=true"} {
		if w = call("categories", []byte("[]"), current.Version, query); w.Code != http.StatusBadRequest {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w = call("schemas", []byte("{}"), current.Version, "?replaceCategories=true"); w.Code != 400 {
		t.Fatal("подтверждение принято вне categories", w.Code)
	}
	w = call("categories", bytes.Repeat([]byte(" "), maxTaxonomyBody+1), current.Version, "")
	if w.Code != http.StatusBadRequest {
		t.Fatal("HTTP принял файл больше 2 МиБ", w.Code)
	}
}

func release10Read(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(releaseDataPath + name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func seedRelease10Dictionaries(
	t *testing.T,
	store *fake.Store,
	generator *fake.IDs,
	attributes []taxonomy.Attribute,
	targetOnly bool,
) {
	t.Helper()
	for _, attribute := range attributes {
		if attribute.Dictionary == "" || (attribute.Dictionary == "target_gender") != targetOnly {
			continue
		}
		kind := reference.KindOpen
		if attribute.Type == "enum" {
			kind = reference.KindClosed
		}
		state := reference.DictionaryState{Code: attribute.Dictionary, Kind: kind, Limits: reference.DefaultLimits()}
		store.SeedReferenceDictionary(state)
		if kind != reference.KindClosed {
			continue
		}
		code := attribute.Dictionary
		if targetOnly {
			code = "gender"
		}
		body, err := os.ReadFile("../../../deploy/reference/" + code + "/entries.json")
		if err != nil {
			t.Fatal(err)
		}
		var entries []releaseSeedEntry
		if err := json.Unmarshal(body, &entries); err != nil {
			t.Fatal(err)
		}
		err = store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
			var records []*reference.Entry
			for _, seed := range entries {
				// Только для fake-теста: существующие взрослые значения gender,
				// без возрастных вариантов. Это не поставка отсутствующего пула.
				if seed.Status != "confirmed" || targetOnly && strings.HasPrefix(seed.Canonical, "Для ") {
					continue
				}
				entry, err := reference.NewEntry(
					reference.RestoreDictionary(state),
					reference.NewEntryInput{
						ID:               generator.NewUUID(),
						CanonicalAliasID: generator.NewUUID(),
						Name:             seed.Canonical,
						Status:           reference.StatusConfirmed,
						Source:           reference.SourceSeed,
						Metadata:         seed.Metadata,
					},
					time.Now(),
				)
				if err != nil {
					return err
				}
				entry.Bump(time.Now())
				records = append(records, entry)
			}
			return tx.Reference().Save(ctx, records, nil)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
