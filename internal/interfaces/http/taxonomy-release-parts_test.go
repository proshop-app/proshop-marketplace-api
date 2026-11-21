package http

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
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
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/referencedata"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/taxonomydata"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/taxonomyfile"
)

func TestArtifactPartsThroughOperatorHTTP(t *testing.T) {
	store := fake.NewStore()
	release, _, err := taxonomydata.Load()
	if err != nil {
		t.Fatal(err)
	}
	fixture := release.Definition()
	previous := previousTaxonomySnapshot(release)
	store.SeedTaxonomy(previous)
	attributes := encodeTaxonomyPart(t, fixture, "attributes")
	generator := &fake.IDs{}
	seedTaxonomyDictionaries(t, store, generator, fixture.Attributes, false)
	seedTaxonomyBindingHeaders(store, release.DictionaryBindings)
	service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil)
	handler := NewHandler(Deps{
		RequestTimeout:         time.Minute,
		ReferenceImportTimeout: time.Minute,
		Registerer:             prometheus.NewRegistry(),
		Taxonomy:               service,
		OperatorToken:          "token",
		Logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
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
	// Файлы не подменены маленькой моделью. Намеренно не загружаем target_gender:
	// ошибка реестра не должна частично опубликовать новые атрибуты.
	w := call("attributes", attributes, previous.Version, "")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "target_gender") {
		t.Fatal(w.Code, w.Body.String())
	}
	current, _ := service.Current(context.Background())
	if current.Version != previous.Version {
		t.Fatal("ошибка реестра изменила версию")
	}
	seedTaxonomyDictionaries(t, store, generator, fixture.Attributes, true)
	for index, part := range []string{"attributes", "schemas", "categories"} {
		version := previous.Version + int64(index)
		body := encodeTaxonomyPart(t, fixture, part)
		if len(body) > maxTaxonomyBody {
			t.Fatal(part, "превышает лимит HTTP")
		}
		w = call(part, body, version, "")
		if w.Code != http.StatusOK || w.Header().Get("ETag") != `"taxonomy-`+strconv.FormatInt(version+1, 10)+`"` {
			t.Fatal(part, w.Code, w.Body.String())
		}
		if stale := call(part, body, version, ""); stale.Code != http.StatusConflict {
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

func seedTaxonomyBindingHeaders(store *fake.Store, bindings []taxonomy.DictionaryBinding) {
	for _, binding := range bindings {
		for _, code := range binding.Dictionaries {
			store.SeedReferenceDictionary(reference.DictionaryState{
				Code:   code,
				Kind:   reference.KindOpen,
				Limits: reference.DefaultLimits(),
			})
		}
	}
}

func encodeTaxonomyPart(t *testing.T, definition taxonomy.Definition, part string) []byte {
	t.Helper()
	body, err := taxonomyfile.EncodePart(definition, part)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func seedTaxonomyDictionaries(
	t *testing.T,
	store *fake.Store,
	generator *fake.IDs,
	attributes []taxonomy.Attribute,
	targetOnly bool,
) {
	t.Helper()
	seeded := map[string]bool{}
	for _, attribute := range attributes {
		if attribute.Dictionary == "" || seeded[attribute.Dictionary] ||
			(attribute.Dictionary == "target_gender") != targetOnly {
			continue
		}
		seeded[attribute.Dictionary] = true
		kind := reference.KindOpen
		if attribute.Type == "enum" {
			kind = reference.KindClosed
		}
		state := reference.DictionaryState{Code: attribute.Dictionary, Kind: kind, Limits: reference.DefaultLimits()}
		store.SeedReferenceDictionary(state)
		if kind != reference.KindClosed {
			continue
		}
		entries := taxonomyDictionaryEntries(t, attribute.Dictionary)
		if entries == nil {
			entries = testConfirmedValues(attributes, attribute.Dictionary)
		}
		err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
			var records []*reference.Entry
			for _, seed := range entries {
				if seed.Status != reference.StatusConfirmed {
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

func taxonomyDictionaryEntries(t *testing.T, code string) []reference.DeliveryEntry {
	t.Helper()
	root := "../../../deploy/reference"
	directory := root + "/" + code
	if _, err := os.Stat(directory); err == nil {
		delivery, _, err := referencedata.Load(os.DirFS(directory))
		if err != nil {
			t.Fatal(err)
		}
		return delivery.Entries
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	input, err := referencedata.LoadImport(os.DirFS(root), "bootstrap.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, dictionary := range input.Dictionaries {
		if dictionary.Code == code && dictionary.Kind == reference.KindClosed {
			return dictionary.Entries
		}
	}
	return nil
}

func testConfirmedValues(attributes []taxonomy.Attribute, dictionary string) []reference.DeliveryEntry {
	// Для проверки HTTP-контракта считаем, что оператор принял используемые значения нового словаря.
	values := map[string]bool{"Значение": true}
	for _, attribute := range attributes {
		if attribute.Dictionary == dictionary {
			for value := range attribute.SearchTermsByValue {
				values[value] = true
			}
			for value := range attribute.ValuesMeta {
				values[value] = true
			}
		}
	}
	entries := make([]reference.DeliveryEntry, 0, len(values))
	for _, value := range slices.Sorted(maps.Keys(values)) {
		entries = append(entries, reference.DeliveryEntry{
			Canonical: value,
			Status:    reference.StatusConfirmed,
			Metadata:  []byte(`{}`),
		})
	}
	return entries
}
