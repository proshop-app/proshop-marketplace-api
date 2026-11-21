package http

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/taxonomydata"
)

type releaseFixture struct {
	taxonomy.Definition
	MigrationSource taxonomy.Definition `json:"migrationSource"`
}

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

func TestArtifactRelease15PublishedContract(t *testing.T) {
	release, _, err := taxonomydata.Load()
	if err != nil {
		t.Fatal(err)
	}
	before := taxonomyReleaseFixture(t).MigrationSource
	store, service := applyTaxonomyRelease(t, release, before)
	published := captureTaxonomyContract(t, store, service)
	// Эталоны сняты с YAML-загрузчика после публикации 15 поверх снимка 9.
	for part, want := range map[string]string{
		"definition":          "9932768973b702369d65f908857a70592886027ddcf036d561422396ac18d4f7",
		"attributes":          "9b5843feb317530f9a3ec6db5482b484960f0811b1f1c7dbd1ec6ac5cac49389",
		"categories":          "fc1ebecdcd7f1afa752b73a6a869278f1223d23f6f654c0efbc292d081c114c0",
		"schemas":             "14f3fe8108d2f44fbc5b0d4d64dce17aba15753f7a36a90a0e434f7270655c09",
		"dictionary-bindings": "c4741c1e6a42beb2242a71d74689d3c71202aed5f352a5b7e814344147c0269b",
		"dictionary-sources":  "434f1404357c5a2950073bd70829ad8e25d8eb1be94e5e47d10cd45bb5adecb2",
		"version":             "8ad0cba0e0bdcfb94b565ec4d0a75850d662c28d5bc732beff4374a6b83a601c",
	} {
		response := published[part]
		if got := fmt.Sprintf("%x", sha256.Sum256([]byte(response.body))); got != want {
			t.Errorf("%s: SHA-256 %s, ожидался %s", part, got, want)
		}
		if part != "definition" && (response.etag != `"taxonomy-10"` || response.version != "10") {
			t.Errorf("%s: ETag=%q, X-Taxonomy-Version=%q", part, response.etag, response.version)
		}
	}

	// Публикация только сменила формат входа. Журнал распознаёт прежний идентификатор.
	assertHistoricalReleaseRecord(t, store)
	assertReleaseRetry(t, store, service, release)
	if retry := captureTaxonomyContract(t, store, service); !reflect.DeepEqual(published, retry) {
		t.Fatal("повтор поставки изменил опубликованные данные")
	}
}

func taxonomyReleaseFixture(t *testing.T) releaseFixture {
	t.Helper()
	body, err := os.ReadFile("../../infrastructure/taxonomydata/taxonomy-bundle.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture releaseFixture
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
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
	for _, binding := range release.DictionaryBindings {
		for _, code := range binding.Dictionaries {
			store.SeedReferenceDictionary(reference.DictionaryState{
				Code:   code,
				Kind:   reference.KindOpen,
				Limits: reference.DefaultLimits(),
			})
		}
	}
	service := apptaxonomy.NewService(store, fake.NewClock(time.Unix(0, 0)), nil)
	release.ExpectedVersion = before.Version
	release.ConfirmCategoryReplacement = true
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
	d, err := service.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	definition, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]taxonomyContractResponse{"definition": {body: string(definition)}}
	handler := NewHandler(Deps{
		RequestTimeout:         time.Minute,
		ReferenceImportTimeout: time.Minute,
		Registerer:             prometheus.NewRegistry(),
		Taxonomy:               service,
		Logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	for _, part := range []string{
		"attributes",
		"categories",
		"schemas",
		"dictionary-bindings",
		"dictionary-sources",
		"version",
	} {
		reads := store.reads
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/taxonomy/"+part, nil))
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

func assertHistoricalReleaseRecord(t *testing.T, store *releaseStore) {
	t.Helper()
	err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		record, err := tx.Taxonomy().LatestRelease(ctx)
		if err != nil {
			return err
		}
		want := taxonomy.ReleaseState{
			Version:          15,
			Checksum:         "d634b123343dff3bc5b3766d48eb14ebf0aa08dcb8b42bc1b2a280e8f81dfb5f",
			PublishedVersion: 10,
		}
		if record == nil || record.ReleaseState != want {
			t.Fatalf("идентификатор YAML-поставки потерян: %+v", record)
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
	release.ConfirmCategoryReplacement = true
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
