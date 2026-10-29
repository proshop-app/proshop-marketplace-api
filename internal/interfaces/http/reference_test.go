package http_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
)

// readToken — токен читателя реестра в тестах; отличается от операторского.
const readToken = "reference-reader-token-0123456789abcdef"

type referenceEnv struct {
	server   *httptest.Server
	registry *prometheus.Registry
}

type referenceCall struct {
	method, path, body, ifMatch string
	authorized                  bool
	// bearer — свой токен вместо операторского
	bearer string
}

type referenceReply struct {
	status int
	etag   string
	body   map[string]any
}

func newReferenceEnv(t *testing.T) *referenceEnv {
	t.Helper()
	store := fake.NewStore()
	store.SeedReferenceDictionary(reference.DictionaryState{
		Code:       "b1",
		Kind:       reference.KindOpen,
		Identity:   true,
		AliasKinds: reference.AliasKinds,
		Limits:     reference.DefaultLimits(),
	})
	config := appreference.Config{Topic: "catalog.reference.changed"}
	service := appreference.NewService(store, fake.NewClock(t0), &fake.IDs{}, &fake.Publisher{}, config)
	registry := prometheus.NewRegistry()
	handler := transport.NewHandler(transport.Deps{
		Reference:          service,
		OperatorToken:      token,
		ReferenceReadToken: readToken,
		Registerer:         registry,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	e := &referenceEnv{server: httptest.NewServer(handler), registry: registry}
	t.Cleanup(e.server.Close)
	return e
}

func (e *referenceEnv) do(t *testing.T, call referenceCall) referenceReply {
	t.Helper()
	request, err := http.NewRequest(call.method, e.server.URL+call.path, strings.NewReader(call.body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	switch {
	case call.bearer != "":
		request.Header.Set("Authorization", "Bearer "+call.bearer)
	case call.authorized:
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if call.ifMatch != "" {
		request.Header.Set("If-Match", call.ifMatch)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	reply := referenceReply{status: response.StatusCode, etag: response.Header.Get("ETag")}
	if err := json.NewDecoder(response.Body).Decode(&reply.body); err != nil {
		t.Fatalf("%s %s: тело не JSON: %v", call.method, call.path, err)
	}
	return reply
}

func (r referenceReply) entry(t *testing.T) map[string]any {
	t.Helper()
	entry, ok := r.body["entry"].(map[string]any)
	if !ok {
		t.Fatalf("нет записи в ответе: %v", r.body)
	}
	return entry
}

func (r referenceReply) errorCode() string {
	detail, _ := r.body["error"].(map[string]any)
	code, _ := detail["code"].(string)
	return code
}

func TestReferenceRequiresServiceToken(t *testing.T) {
	e := newReferenceEnv(t)
	reply := e.do(t, referenceCall{method: http.MethodGet, path: "/internal/reference/b1/snapshot"})
	if reply.status != http.StatusUnauthorized {
		t.Fatalf("снимок без токена: %d", reply.status)
	}
}

func TestReferenceReadTokenOpensOnlyReads(t *testing.T) {
	e := newReferenceEnv(t)
	created := e.do(t, referenceCall{
		method:     http.MethodPost,
		path:       "/internal/reference/b1/entries",
		body:       `{"canonical":"Бренд","status":"confirmed"}`,
		authorized: true,
	})
	id := created.entry(t)["id"].(string)
	reads := []referenceCall{
		{method: http.MethodGet, path: "/internal/reference/b1/snapshot"},
		{method: http.MethodPost, path: "/internal/reference/b1/resolve", body: `{"text":"бренд"}`},
		{method: http.MethodGet, path: "/internal/reference/b1/entries"},
		{method: http.MethodGet, path: "/internal/reference/b1/entries/" + id},
	}
	for _, call := range reads {
		call.bearer = readToken
		if reply := e.do(t, call); reply.status != http.StatusOK {
			t.Fatalf("чтение %s %s токеном читателя: %d %v", call.method, call.path, reply.status, reply.body)
		}
	}
	writes := []referenceCall{
		{method: http.MethodPost, path: "/internal/reference/b1/entries", body: `{"canonical":"Бренд Два"}`},
		{method: http.MethodPatch, path: "/internal/reference/b1/entries/" + id, body: `{}`, ifMatch: created.etag},
		{method: http.MethodPost, path: "/internal/reference/b1/entries/" + id + "/merge", body: `{"into":"b1"}`},
		{method: http.MethodPost, path: "/internal/reference/b1/entries/" + id + "/aliases", body: `{"text":"Brend"}`},
		{method: http.MethodDelete, path: "/internal/reference/b1/aliases/" + id},
	}
	for _, call := range writes {
		call.bearer = readToken
		if reply := e.do(t, call); reply.status != http.StatusUnauthorized {
			t.Fatalf("мутация %s %s токеном читателя: %d", call.method, call.path, reply.status)
		}
	}
	stranger := referenceCall{method: http.MethodGet, path: "/internal/reference/b1/snapshot", bearer: "x" + readToken}
	if reply := e.do(t, stranger); reply.status != http.StatusUnauthorized {
		t.Fatalf("чужой токен: %d", reply.status)
	}
}

func TestReferenceEntryLifecycle(t *testing.T) {
	e := newReferenceEnv(t)
	created := e.do(t, referenceCall{
		method:     http.MethodPost,
		path:       "/internal/reference/b1/entries",
		body:       `{"canonical":"Бренд","status":"confirmed","aliases":[{"text":"Brend","kind":"translit"}]}`,
		authorized: true,
	})
	entry := created.entry(t)
	if created.status != http.StatusCreated || created.body["schemaVersion"] != float64(1) ||
		created.body["revision"] != float64(1) || created.etag != `"1"` || len(entry["aliases"].([]any)) != 2 {
		t.Fatalf("создание: %d %v %s", created.status, created.body, created.etag)
	}
	id := entry["id"].(string)

	taken := e.do(t, referenceCall{
		method:     http.MethodPost,
		path:       "/internal/reference/b1/entries",
		body:       `{"canonical":"BREND","status":"confirmed"}`,
		authorized: true,
	})
	if taken.status != http.StatusConflict || taken.errorCode() != "REFERENCE_KEY_TAKEN" {
		t.Fatalf("занятый ключ: %d %v", taken.status, taken.body)
	}

	patch := referenceCall{
		method:     http.MethodPatch,
		path:       "/internal/reference/b1/entries/" + id,
		body:       `{"canonical":"Бренд Два"}`,
		authorized: true,
	}
	reply := e.do(t, patch)
	if reply.status != http.StatusBadRequest || reply.errorCode() != "reference.if_match_required" {
		t.Fatalf("правка без If-Match: %d %v", reply.status, reply.body)
	}
	patch.ifMatch = `"7"`
	if reply := e.do(t, patch); reply.status != http.StatusConflict || reply.errorCode() != "REFERENCE_ENTRY_CHANGED" {
		t.Fatalf("устаревший If-Match: %d %v", reply.status, reply.body)
	}
	patch.ifMatch = created.etag
	renamed := e.do(t, patch)
	if renamed.status != http.StatusOK || renamed.entry(t)["key"] != "бренд два" || renamed.body["changed"] != true {
		t.Fatalf("переименование: %d %v", renamed.status, renamed.body)
	}

	alias := e.do(t, referenceCall{
		method:     http.MethodPost,
		path:       "/internal/reference/b1/entries/" + id + "/aliases",
		body:       `{"text":"B-Rend"}`,
		authorized: true,
	})
	if alias.status != http.StatusCreated {
		t.Fatalf("вариант: %d %v", alias.status, alias.body)
	}
	aliasID := alias.body["alias"].(map[string]any)["id"].(string)
	removed := e.do(t, referenceCall{
		method:     http.MethodDelete,
		path:       "/internal/reference/b1/aliases/" + aliasID,
		authorized: true,
	})
	if removed.status != http.StatusOK || len(removed.entry(t)["aliases"].([]any)) != 3 {
		t.Fatalf("удаление варианта: %d %v", removed.status, removed.body)
	}

	survivor := e.do(t, referenceCall{
		method:     http.MethodPost,
		path:       "/internal/reference/b1/entries",
		body:       `{"canonical":"Бренд Три","status":"confirmed"}`,
		authorized: true,
	})
	merged := e.do(t, referenceCall{
		method:     http.MethodPost,
		path:       "/internal/reference/b1/entries/" + id + "/merge",
		body:       `{"into":"` + survivor.entry(t)["id"].(string) + `"}`,
		ifMatch:    removed.etag,
		authorized: true,
	})
	if merged.status != http.StatusOK || len(merged.entry(t)["aliases"].([]any)) != 4 {
		t.Fatalf("объединение: %d %v", merged.status, merged.body)
	}
	read := referenceCall{method: http.MethodGet, path: "/internal/reference/b1/entries/" + id, authorized: true}
	view := e.do(t, read)
	if view.entry(t)["status"] != "merged" || view.body["resolved"] == nil {
		t.Fatalf("объединённая запись: %v", view.body)
	}
}

func TestReferenceSnapshotAndResolve(t *testing.T) {
	e := newReferenceEnv(t)
	e.do(t, referenceCall{
		method:     http.MethodPost,
		path:       "/internal/reference/b1/entries",
		body:       `{"canonical":"Бренд","status":"confirmed","metadata":{"directions":["d1"]}}`,
		authorized: true,
	})
	e.do(t, referenceCall{
		method:     http.MethodPost,
		path:       "/internal/reference/b1/entries",
		body:       `{"canonical":"Брендовый","source":"catalog"}`,
		authorized: true,
	})
	full := referenceCall{method: http.MethodGet, path: "/internal/reference/b1/snapshot", authorized: true}
	snapshot := e.do(t, full)
	entries := snapshot.body["entries"].([]any)
	if snapshot.status != http.StatusOK || snapshot.body["full"] != true || snapshot.body["normalizer"] != "v1" ||
		len(entries) != 2 || entries[0].(map[string]any)["metadata"] != nil {
		t.Fatalf("снимок: %d %v", snapshot.status, snapshot.body)
	}
	since := referenceCall{method: http.MethodGet, path: "/internal/reference/b1/snapshot?since=1", authorized: true}
	delta := e.do(t, since)
	if delta.body["full"] != false || len(delta.body["entries"].([]any)) != 1 {
		t.Fatalf("дельта: %v", delta.body)
	}
	for _, query := range []string{"?since=-1", "?since=x", "?scope=tenant:t1"} {
		path := "/internal/reference/b1/snapshot" + query
		bad := e.do(t, referenceCall{method: http.MethodGet, path: path, authorized: true})
		if bad.status != http.StatusBadRequest {
			t.Fatalf("снимок %s: %d", query, bad.status)
		}
	}

	match := e.do(t, referenceCall{
		method:     http.MethodPost,
		path:       "/internal/reference/b1/resolve",
		body:       `{"text":" БРЕНД "}`,
		authorized: true,
	})
	found := match.body["match"].(map[string]any)
	if match.status != http.StatusOK || found["confidence"] != "exact" || found["scope"] != "global" {
		t.Fatalf("resolve: %d %v", match.status, match.body)
	}
	fuzzy := e.do(t, referenceCall{
		method:     http.MethodPost,
		path:       "/internal/reference/b1/resolve",
		body:       `{"text":"бре","tenantId":"t1"}`,
		authorized: true,
	})
	if fuzzy.body["match"] != nil || len(fuzzy.body["candidates"].([]any)) != 2 {
		t.Fatalf("кандидаты: %v", fuzzy.body)
	}
	tooLong := e.do(t, referenceCall{
		method:     http.MethodPost,
		path:       "/internal/reference/b1/resolve",
		body:       `{"text":"` + strings.Repeat("б", 201) + `"}`,
		authorized: true,
	})
	tooLarge := e.do(t, referenceCall{
		method:     http.MethodPost,
		path:       "/internal/reference/b1/resolve",
		body:       `{"text":"` + strings.Repeat("b", 5000) + `"}`,
		authorized: true,
	})
	garbage := e.do(t, referenceCall{
		method:     http.MethodPost,
		path:       "/internal/reference/garbage1/resolve",
		body:       `{"text":`,
		authorized: true,
	})
	if garbage.status != http.StatusBadRequest {
		t.Fatalf("тело не разбирается: %d", garbage.status)
	}
	unknown := e.do(t, referenceCall{
		method:     http.MethodPost,
		path:       "/internal/reference/b9/resolve",
		body:       `{"text":"бренд"}`,
		authorized: true,
	})
	if tooLong.status != http.StatusBadRequest || tooLarge.status != http.StatusBadRequest ||
		unknown.status != http.StatusNotFound {
		t.Fatalf("отказы resolve: %d %d %d", tooLong.status, tooLarge.status, unknown.status)
	}
	// match, candidates и invalid словаря b1 плюс invalid неизвестного словаря под общей меткой
	families, err := e.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]bool{}
	for _, family := range families {
		if family.GetName() != "marketplace_reference_resolve_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "dictionary" {
					labels[label.GetValue()] = true
				}
			}
		}
	}
	// адреса b9 и garbage1 не становятся метками: их словарь не подтверждён
	if len(labels) != 2 || !labels["b1"] || !labels["unknown"] {
		t.Fatalf("метки словаря в метрике resolve: %v", labels)
	}
}
