package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/taxonomydata"
)

type taxonomyContractResponse struct {
	body    string
	etag    string
	version string
}

type releaseStore struct {
	*fake.Store
	reads  int
	queues int
}

type releaseTx struct {
	ports.Tx
	store *releaseStore
}

type releaseRepository struct {
	taxonomy.Repository
	store *releaseStore
}

func TestArtifactRelease17PublishedContract(t *testing.T) {
	release, _, err := taxonomydata.Load()
	if err != nil {
		t.Fatal(err)
	}
	before := previousTaxonomySnapshot(release)
	store, service := applyTaxonomyRelease(t, release, before)
	published := captureTaxonomyContract(t, store, service)
	var attributes struct {
		Attributes      []taxonomy.Attribute
		SelectionValues map[string]taxonomy.DictionaryValues
	}
	if err := json.Unmarshal([]byte(published["attributes"].body), &attributes); err != nil {
		t.Fatal(err)
	}
	size := taxonomy.Definition{Attributes: attributes.Attributes}.Fields()["size_clothing_label"]
	if size.Name != "Размер одежды по маркировке" || size.DisplayName != "Размер" ||
		size.Hint == "" || !size.DictionarySelection {
		t.Fatalf("каталог атрибутов: %+v", size)
	}
	if pool := attributes.SelectionValues["size_clothing_men_ru"]; pool.ValuesMeta == nil {
		t.Fatal("снимок не отдаёт действующий пул")
	}
	assertArtifactFacets(t, published["attributes"].body, release.Definition())
	assertArtifactCategorySchema(t, service)
	var tree struct {
		Categories []taxonomy.Category
	}
	if err := json.Unmarshal([]byte(published["categories"].body), &tree); err != nil ||
		!reflect.DeepEqual(tree.Categories, before.Categories) {
		t.Fatal("поставка со схемами изменила дерево", err)
	}
	for part, response := range published {
		if response.etag != `"taxonomy-10"` || response.version != "10" {
			t.Errorf("%s: ETag=%q, X-Taxonomy-Version=%q", part, response.etag, response.version)
		}
	}
	assertReleaseRecord(t, store, release)
	assertReleaseRetry(t, store, service, release)
	if retry := captureTaxonomyContract(t, store, service); !reflect.DeepEqual(published, retry) {
		t.Fatal("повтор поставки изменил опубликованные данные")
	}
}

func assertArtifactFacets(t *testing.T, body string, definition taxonomy.Definition) {
	t.Helper()
	var response struct {
		Attributes []struct {
			Code  string
			Facet *bool
		}
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatal(err)
	}
	fields := definition.Fields()
	facets := 0
	for _, attribute := range response.Attributes {
		_, found := fields[attribute.Code]
		want := slices.Contains(definition.FacetKeys, attribute.Code)
		if !found || attribute.Facet == nil || *attribute.Facet != want {
			t.Fatalf("неверный признак facet у %s", attribute.Code)
		}
		delete(fields, attribute.Code)
		if *attribute.Facet {
			facets++
		}
	}
	if len(fields) != 0 || len(response.Attributes) != 2009 || facets != 809 {
		t.Fatalf("определений %d, потеряно %d, фасетов %d", len(response.Attributes), len(fields), facets)
	}
}

func assertArtifactCategorySchema(t *testing.T, service *apptaxonomy.Service) {
	t.Helper()
	handler := taxonomyHandlers{service: service}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.SetPathValue("id", "electronics.smartphones")
	handler.schema(response, request)
	var leaf taxonomyCategorySchema
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &leaf) != nil ||
		len(leaf.Filters) != 18 || leaf.Filters[0] != (taxonomy.Filter{Code: "storage_capacity", Primary: true}) ||
		leaf.Filters[6] != (taxonomy.Filter{Code: "esim_support"}) {
		t.Fatalf("неверная панель листа: %v", leaf.Filters)
	}
	fields := map[string]bool{}
	for _, field := range leaf.Attributes {
		fields[field.Code] = field.Facet
	}
	if !fields["esim_support"] || fields["country"] || fields["model"] {
		t.Fatalf("неверный признак фильтруемости листа: %v", fields)
	}
}

func previousTaxonomySnapshot(release taxonomy.Release) taxonomy.Definition {
	before := release.Definition()
	before.Version = 9
	before.Attributes = slices.Clone(before.Attributes)
	for i := range before.Attributes {
		before.Attributes[i].DictionarySelection = false
	}
	before.Schemas = make(map[string]taxonomy.Schema, len(release.Schemas))
	for name, schema := range release.Schemas {
		if slices.Contains(schema.Attributes, "brand") {
			schema.Optional = append(slices.Clone(schema.Optional), "brand")
		}
		before.Schemas[name] = schema
	}
	return before
}

func applyTaxonomyRelease(
	t *testing.T,
	release taxonomy.Release,
	before taxonomy.Definition,
) (*releaseStore, *apptaxonomy.Service) {
	t.Helper()
	store := &releaseStore{Store: fake.NewStore()}
	store.SeedTaxonomy(before)
	generator := &fake.IDs{}
	seedTaxonomyDictionaries(t, store.Store, generator, release.Attributes, false)
	seedTaxonomyDictionaries(t, store.Store, generator, release.Attributes, true)
	seedTaxonomyBindingHeaders(store.Store, release.DictionaryBindings)
	if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		previous := taxonomy.Release{
			Version:  16,
			Checksum: "5e64e97f5c1bc99b2eb65e07ffdd97b1eda706076d1f3f43d8ab659b169cd323",
		}
		return tx.Taxonomy().RecordRelease(ctx, previous, before.Version, taxonomy.Definition{})
	}); err != nil {
		t.Fatal(err)
	}
	service := apptaxonomy.NewService(store, fake.NewClock(time.Unix(0, 0)), nil)
	result, err := service.Apply(context.Background(), release)
	if err != nil || !result.Changed || result.SnapshotVersion != before.Version+1 || store.queues != 1 {
		t.Fatalf("публикация: %+v, err=%v, очередей=%d", result, err, store.queues)
	}
	return store, service
}

func captureTaxonomyContract(
	t *testing.T,
	store *releaseStore,
	service *apptaxonomy.Service,
) map[string]taxonomyContractResponse {
	t.Helper()
	result := map[string]taxonomyContractResponse{}
	handler := NewHandler(Deps{
		RequestTimeout:         time.Minute,
		ReferenceImportTimeout: time.Minute,
		Registerer:             prometheus.NewRegistry(),
		Taxonomy:               service,
		OperatorToken:          "token",
		Logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	for _, part := range []string{
		"attributes",
		"categories",
		"schemas",
		"dictionary-bindings",
		"dictionary-sources",
		"version",
		"definition",
	} {
		reads := store.reads
		w := httptest.NewRecorder()
		path := "/api/v1/taxonomy/" + part
		if part == "definition" {
			path = "/api/v1/operator/taxonomy"
		}
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer token")
		handler.ServeHTTP(w, request)
		if w.Code != http.StatusOK || store.reads != reads+1 {
			t.Fatalf("%s: статус %d, чтений снимка %d", part, w.Code, store.reads-reads)
		}
		result[part] = taxonomyContractResponse{
			body:    w.Body.String(),
			etag:    w.Header().Get("ETag"),
			version: w.Header().Get("X-Taxonomy-Version"),
		}
	}
	return result
}

func assertReleaseRecord(t *testing.T, store *releaseStore, release taxonomy.Release) {
	t.Helper()
	err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		record, err := tx.Taxonomy().LatestRelease(ctx)
		if err != nil {
			return err
		}
		want := taxonomy.ReleaseState{
			Version:          release.Version,
			Checksum:         release.Checksum,
			PublishedVersion: 10,
		}
		if record == nil || record.ReleaseState != want {
			t.Fatalf("SHA-256 поставки потерян: %+v", record)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertReleaseRetry(
	t *testing.T,
	store *releaseStore,
	service *apptaxonomy.Service,
	release taxonomy.Release,
) {
	t.Helper()
	result, err := service.Apply(context.Background(), release)
	if err != nil || result.Changed || result.SnapshotVersion != 10 || store.queues != 1 {
		t.Fatalf("повтор поставки: %+v, err=%v, очередей=%d", result, err, store.queues)
	}
}

func (s *releaseStore) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return s.Store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return fn(ctx, releaseTx{Tx: tx, store: s})
	})
}

func (tx releaseTx) Taxonomy() taxonomy.Repository {
	return releaseRepository{Repository: tx.Tx.Taxonomy(), store: tx.store}
}

func (r releaseRepository) Current(ctx context.Context) (taxonomy.Definition, error) {
	r.store.reads++
	return r.Repository.Current(ctx)
}

func (r releaseRepository) QueueRevalidation(ctx context.Context, version int64, categories []string) error {
	r.store.queues++
	return r.Repository.QueueRevalidation(ctx, version, categories)
}
