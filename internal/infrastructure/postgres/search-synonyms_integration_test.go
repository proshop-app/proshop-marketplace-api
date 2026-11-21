//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	app "github.com/proshop-app/proshop-marketplace/internal/application/synonyms"
	"github.com/proshop-app/proshop-marketplace/internal/domain/synonyms"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/searchapi"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/taxonomydata"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
)

const (
	synonymReadToken = "synonym-read-token-for-fixture-0123456"
	synonymAdmin     = "synonym-admin-token-fixture-012345678"
	synonymOperator  = "synonym-operator-token-fixture-012345"
	snapshotURL      = "/api/v1/internal/reference/search-synonyms/snapshot"
	rulesURL         = "/api/v1/operator/reference/search-synonyms/rules"
	stopListURL      = "/api/v1/operator/reference/search-synonyms/stop-list"
	routerRuleID     = "syn_bc35b98cab9b3989b3790beb01a87d4d"
)

type synonymRequest struct {
	method string
	path   string
	token  string
	etag   string
	body   string
}

func TestSearchSynonymsFromSeedToSnapshotAndDisable(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	saveSynonymTaxonomy(t, uow)
	search, calls := startSynonymValidator(t)
	now := fake.NewClock(time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC))
	publisher := &fake.Publisher{}
	service := app.New(uow, now, search.SynonymValidator(synonymReadToken), publisher, app.EventTopic)
	handler := synonymHTTP(service)
	schedule := postgres.NewScheduledWork(pool, "test-reference-synonyms", time.Minute)

	get := synonymRequest{method: http.MethodGet, path: snapshotURL, token: synonymReadToken}
	empty := requestSynonyms(t, handler, get, http.StatusOK)
	if empty.Header().Get("ETag") != `"0"` {
		t.Fatalf("начальная ревизия: %s", empty.Header().Get("ETag"))
	}
	run := func(ctx context.Context) error {
		result, err := service.Run(ctx)
		if err == nil && (result.Pass != 1 || result.Retry != 1 || result.Activated != 1) {
			t.Fatalf("результат фоновой сверки: %+v", result)
		}
		return err
	}
	if err := schedule.WithLock(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := schedule.WithLock(context.Background(), run); !errors.Is(err, ports.ErrRebuildBusy) {
		t.Fatalf("вторая реплика не соблюла общий интервал: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("вызовов валидатора: %d", calls.Load())
	}

	assertPublishedRouter(t, handler)
	disablePublishedRouter(t, handler)
	assertSynonymStorage(t, pool)
	assertSynonymEvents(t, pool, service, now, publisher)
}

func assertPublishedRouter(t *testing.T, handler http.Handler) {
	t.Helper()
	get := synonymRequest{method: http.MethodGet, path: snapshotURL, token: synonymReadToken, etag: `"0"`}
	active := requestSynonyms(t, handler, get, http.StatusOK)
	var snapshot synonyms.Snapshot
	err := json.Unmarshal(active.Body.Bytes(), &snapshot)
	if err != nil || snapshot.Revision != 1 || len(snapshot.Rules) != 1 || snapshot.Rules[0].ID != routerRuleID ||
		snapshot.Rules[0].Scope.ID != "computers.routers" || snapshot.Rules[0].Phase != synonyms.Active {
		t.Fatalf("снимок: %s, %v", active.Body, err)
	}
	get.etag = `W/"1"`
	if reply := requestSynonyms(t, handler, get, http.StatusNotModified); reply.Body.Len() != 0 {
		t.Fatalf("304 с телом: %s", reply.Body)
	}

	list := synonymRequest{method: http.MethodGet, path: rulesURL + "?limit=1", token: synonymOperator}
	page := requestSynonyms(t, handler, list, http.StatusOK)
	var result app.Page
	if err := json.Unmarshal(page.Body.Bytes(), &result); err != nil || len(result.Entries) != 1 ||
		result.NextAfter == "" || result.Entries[0].LastCheck.Outcome != synonyms.Retry {
		t.Fatalf("страница проверок: %s, %v", page.Body, err)
	}
	list.path += "&after=" + result.NextAfter
	page = requestSynonyms(t, handler, list, http.StatusOK)
	if err := json.Unmarshal(page.Body.Bytes(), &result); err != nil || len(result.Entries) != 1 ||
		result.Entries[0].LastCheck.RescuedScopes != 3 || result.Entries[0].Source.Kind != synonyms.TaxonomyName {
		t.Fatalf("каталожный эффект: %s, %v", page.Body, err)
	}
}

func disablePublishedRouter(t *testing.T, handler http.Handler) {
	t.Helper()
	change := synonymRequest{
		method: http.MethodPatch,
		path:   rulesURL + "/" + routerRuleID,
		token:  synonymReadToken,
		body:   `{"expectedRevision":1,"enabled":false,"reason":"проверка отключения"}`,
	}
	requestSynonyms(t, handler, change, http.StatusUnauthorized)
	change.token = synonymOperator
	requestSynonyms(t, handler, change, http.StatusOK)
	requestSynonyms(t, handler, change, http.StatusConflict)
	get := synonymRequest{method: http.MethodGet, path: snapshotURL, token: synonymReadToken, etag: `"1"`}
	disabled := requestSynonyms(t, handler, get, http.StatusOK)
	if disabled.Header().Get("ETag") != `"2"` || !strings.Contains(disabled.Body.String(), `"rules":[]`) {
		t.Fatalf("отключение не изменило снимок: %s", disabled.Body)
	}

	list := synonymRequest{
		method: http.MethodPut,
		path:   stopListURL,
		token:  synonymOperator,
		body:   `{"expectedRevision":2,"blockedTerms":[" USB  ","usb"],"reason":"проверка стоп-листа"}`,
	}
	requestSynonyms(t, handler, list, http.StatusOK)
	get.etag = `"2"`
	withStop := requestSynonyms(t, handler, get, http.StatusOK)
	if withStop.Header().Get("ETag") != `"3"` || !strings.Contains(withStop.Body.String(), `"blockedTerms":["usb"]`) {
		t.Fatalf("стоп-лист: %s", withStop.Body)
	}
}

func assertSynonymStorage(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	const query = `SELECT COUNT(*) FILTER (WHERE to_phase <> ''),
		(SELECT count(*) FROM reference.search_synonym_checks),
		(SELECT count(*) FROM reference.search_synonym_rules)
		FROM reference.search_synonym_transitions`
	var transitions, checks, active int
	err := pool.QueryRow(context.Background(), query).Scan(&transitions, &checks, &active)
	if err != nil || transitions != 6 || checks != 2 || active != 0 {
		t.Fatalf("журнал: %d переходов, %d проверок, %d активных, %v", transitions, checks, active, err)
	}
}

func assertSynonymEvents(
	t *testing.T,
	pool *pgxpool.Pool,
	service *app.Service,
	now *fake.Clock,
	publisher *fake.Publisher,
) {
	t.Helper()
	dispatcher := messaging.NewDispatcher(
		postgres.NewTaskQueue(pool),
		now,
		messaging.DispatcherConfig{
			Batch:        10,
			PerTenant:    10,
			Lease:        time.Minute,
			MaxAttempts:  3,
			OrderedKinds: []string{app.TaskChanged},
		},
		map[string]messaging.Handler{app.TaskChanged: service.HandleTask},
	)
	for i := 0; i < 3; i++ {
		if _, err := dispatcher.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	messages := publisher.Messages()
	if len(messages) != 3 {
		t.Fatalf("opубликовано событий: %d", len(messages))
	}
	for index, event := range messages {
		var body app.Event
		err := json.Unmarshal(event.Value, &body)
		if err != nil || event.Topic != app.EventTopic || string(event.Key) != "search_synonyms" ||
			body.SchemaVersion != 1 || body.Revision != int64(index+1) {
			t.Fatalf("событие: %+v, %s, %v", event, event.Value, err)
		}
	}
}

func synonymHTTP(service *app.Service) http.Handler {
	return transport.NewHandler(transport.Deps{
		RequestTimeout:         time.Minute,
		ReferenceImportTimeout: time.Minute,
		Synonyms:               service,
		OperatorToken:          synonymOperator,
		ReferenceReadToken:     synonymReadToken,
		Registerer:             prometheus.NewRegistry(),
		Logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func requestSynonyms(
	t *testing.T,
	handler http.Handler,
	operation synonymRequest,
	expected int,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(operation.method, operation.path, strings.NewReader(operation.body))
	req.Header.Set("Authorization", "Bearer "+operation.token)
	req.Header.Set("If-None-Match", operation.etag)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != expected {
		t.Fatalf("%s %s: HTTP %d, хотели %d, %s", operation.method, operation.path, rec.Code, expected, rec.Body)
	}
	return rec
}

func saveSynonymTaxonomy(t *testing.T, uow ports.UnitOfWork) {
	t.Helper()
	release, _, err := taxonomydata.Load()
	if err != nil {
		t.Fatal(err)
	}
	definition := release.Definition()
	definition.Version = 17
	err = uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Taxonomy().Save(ctx, definition, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func startSynonymValidator(t *testing.T) (*searchapi.Index, *atomic.Int32) {
	t.Helper()
	want := readSynonymFixture(t, "request.json")
	reply := readSynonymFixture(t, "response.json")
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/internal/search/synonyms/validate" ||
			r.Header.Get("Authorization") != "Bearer "+synonymReadToken || r.Header.Get("X-Admin-Id") != "" {
			t.Error("неверный маршрут или служебная авторизация")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var expected, observed any
		if json.Unmarshal(want, &expected) != nil || json.NewDecoder(r.Body).Decode(&observed) != nil ||
			!reflect.DeepEqual(expected, observed) {
			t.Errorf("тело запроса: %#v", observed)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(reply)
	}))
	t.Cleanup(server.Close)
	index, err := searchapi.New(searchapi.Config{URL: server.URL, Token: synonymAdmin, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return index, calls
}

func readSynonymFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/search-synonyms/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSearchSynonymsRegistrySourcesAndBrandGuard(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(pool)
	saveSynonymTaxonomy(t, uow)
	seedSynonymAliases(t, pool)

	now := fake.NewClock(time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC))
	validator := fake.SynonymValidator{ValidateFunc: func(
		_ context.Context,
		request ports.SynonymValidationRequest,
	) (ports.SynonymValidationResponse, error) {
		if len(request.Rules) != 2 {
			t.Fatalf("ожидались имя USB и подтверждённый вариант реестра: %+v", request.Rules)
		}
		reply := ports.SynonymValidationResponse{BatchID: request.BatchID, ValidatorVersion: 1}
		for _, rule := range request.Rules {
			alias := rule.From == "разветвитель usb"
			if (rule.Source == "reference_alias") != alias {
				t.Fatalf("неверно передан источник: %+v", rule)
			}
			reply.Results = append(reply.Results, synonyms.Validation{
				ID:                  rule.ID,
				Complete:            true,
				ScopesChecked:       5,
				SourceLiteralScopes: 0,
				TargetLiteralScopes: 5,
				RescuedScopes:       3,
				Decision:            synonyms.Pass,
				Evidence: synonyms.Evidence{
					Fingerprint:      strings.Repeat("0", 64),
					CheckedAt:        now.Now(),
					TaxonomyRevision: "17",
				},
			})
		}
		return reply, nil
	}}
	service := app.New(uow, now, validator, &fake.Publisher{}, app.EventTopic)
	res, err := service.Run(ctx)
	if err != nil || res.Pass != 1 || res.Reject != 1 || res.Active != 1 {
		t.Fatalf("допуск вариантов реестра: %+v, %v", res, err)
	}
	page, err := service.List(ctx, "", 10)
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("список направлений: %+v, %v", page, err)
	}
	for _, item := range page.Entries {
		if item.Phase == synonyms.Active && (item.Source.Kind != synonyms.RegistryAlias ||
			item.From != "разветвитель usb" || item.Source.Revision != "17:2") {
			t.Fatalf("допущен другой вариант: %+v", item)
		}
	}
	_, err = pool.Exec(ctx, `UPDATE reference.entries SET status = 'deprecated' WHERE dictionary_code = 'test_names'`)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := service.Run(ctx); err != nil || res.Active != 0 || res.DailyChecked != 2 {
		t.Fatalf("снятие подтверждения: %+v, %v", res, err)
	}
}

func seedSynonymAliases(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	const seed = `
		INSERT INTO reference.dictionaries (code, kind, identity, alias_kinds, limits)
			SELECT 'test_names', kind, identity, alias_kinds, limits FROM reference.dictionaries WHERE code = 'brand';
		INSERT INTO reference.entries (id, dictionary_code, canonical_name, canonical_key, status, source,
			revision, version, created_at, updated_at) VALUES
			('00000000-0000-4000-8000-000000000001', 'test_names', 'USB-концентраторы', 'usb-концентраторы',
				'confirmed', 'seed', 2, 1, now(), now()),
			('00000000-0000-4000-8000-000000000002', 'brand', 'Роутеры', 'роутеры',
				'confirmed', 'seed', 3, 1, now(), now());
		INSERT INTO reference.search_synonym_registry_scopes (entry_id, canonical_key, category_id, category_name)
			VALUES
			('00000000-0000-4000-8000-000000000001', 'usb-концентраторы', 'computers.usb-hubs', 'USB-концентраторы');
		INSERT INTO reference.aliases (id, entry_id, dictionary_code, text, key, kind, source, status) VALUES
			('00000000-0000-4000-8000-000000000003', '00000000-0000-4000-8000-000000000001', 'test_names',
				'Разветвитель USB', 'разветвитель usb', 'spelling', 'seed', 'active'),
			('00000000-0000-4000-8000-000000000004', '00000000-0000-4000-8000-000000000001', 'test_names',
				'ЮСБ', 'юсб', 'abbreviation', 'seed', 'active'),
			('00000000-0000-4000-8000-000000000005', '00000000-0000-4000-8000-000000000001', 'test_names',
				'ЮСБ концентраторы', 'юсб концентраторы', 'spelling', 'seed', 'proposed')`
	if _, err := pool.Exec(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
}
