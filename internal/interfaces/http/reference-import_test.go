package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

const referenceImportJSONFixture = `{
    "schemaVersion":2,
    "sources":[{"id":"s1","url":"https://example.org/data","license":"CC0-1.0"}],
    "dictionaries":[{
        "code":"d1","kind":"open",
        "identity":false,"aliasKinds":["spelling"],
        "limits":{"maxEntries":1000000,"maxAliases":50,
            "maxAliasLength":120,"maxAliasWords":8,"normalizer":"v1"},
        "release":1,"entries":[{"canonical":"Значение"}]} ]
}`

func TestReferenceImportRecordsRequestPrincipal(t *testing.T) {
	store := fake.NewStore()
	clock := fake.NewClock(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	service := appreference.NewService(store, clock, &fake.IDs{}, nil, appreference.Config{})
	handler := referenceHandlers{service: service}
	for _, actor := range []string{"op1", "op2"} {
		request := httptest.NewRequest(http.MethodPost, "/import", strings.NewReader(referenceImportJSONFixture))
		request.Header.Set("Content-Type", "application/json")
		ctx := context.WithValue(request.Context(), keyOperator, operator.Principal{ID: actor})
		recorder := httptest.NewRecorder()
		handler.importPackage(recorder, request.WithContext(ctx))
		if recorder.Code != http.StatusOK {
			t.Fatal(recorder.Code, recorder.Body.String())
		}
		changes := store.ReferenceChanges()
		if len(changes) != 1 || changes[0].Meta.Actor != "op1" || changes[0].Meta.Source != reference.SourceSeed {
			t.Fatal("импорт потерял автора или повтор переписал журнал", changes)
		}
	}
}

func TestReferenceImportStrictJSONAndYAML(t *testing.T) {
	yamlBody := "schemaVersion: 2\nsources:\n  - id: s1\n    url: https://example.org/data\n" +
		"    license: CC0-1.0\ndictionaries:\n  - code: d1\n    kind: open\n    identity: false\n    release: 1\n" +
		"    aliasKinds: [spelling]\n" +
		"    limits: {maxEntries: 1000000, maxAliases: 50,\n" +
		"      maxAliasLength: 120, maxAliasWords: 8, normalizer: v1}\n" +
		"    entries:\n      - canonical: Значение\n"
	extraField := strings.Replace(
		referenceImportJSONFixture,
		`"identity":false,`,
		`"identity":false,"extra":true,`,
		1,
	)
	cases := []struct {
		name        string
		contentType string
		body        string
		valid       bool
	}{
		{name: "JSON", contentType: "application/json", body: referenceImportJSONFixture, valid: true},
		{name: "YAML", contentType: "application/yaml", body: yamlBody, valid: true},
		{name: "text YAML", contentType: "text/yaml", body: yamlBody, valid: true},
		{
			name:        "JSON charset",
			contentType: "application/json; charset=utf-8",
			body:        referenceImportJSONFixture,
			valid:       true,
		},
		{name: "неверный тип", contentType: "text/plain", body: yamlBody},
		{name: "YAML под видом JSON", contentType: "application/json", body: yamlBody},
		{
			name:        "лишнее поле словаря",
			contentType: "application/json",
			body:        extraField,
		},
		{name: "лишнее поле", contentType: "application/json", body: `{"schemaVersion":2,"extra":true}`},
		{name: "повтор JSON", contentType: "application/json", body: `{"schemaVersion":2,"schemaVersion":2}`},
		{name: "повтор YAML", contentType: "application/yaml", body: "schemaVersion: 2\nschemaVersion: 2\n"},
		{name: "несколько документов", contentType: "application/yaml", body: yamlBody + "---\nschemaVersion: 2\n"},
		{name: "якорь", contentType: "application/yaml", body: "schemaVersion: &v 1\n"},
		{name: "ссылка", contentType: "application/yaml", body: "a: &v 1\nb: *v\n"},
		{name: "специальный тег", contentType: "application/yaml", body: "schemaVersion: !custom 1\n"},
		{name: "не строковый ключ", contentType: "application/yaml", body: "1: value\n"},
		{name: "вложенный повтор", contentType: "application/json", body: `{"sources":[{"id":"s1","id":"s2"}]}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			input, err := decodeReferenceImport(httptest.NewRecorder(), request)
			if (err == nil) != test.valid {
				t.Fatal(input, err)
			}
			if test.valid && (input.SchemaVersion != 2 || input.Dictionaries[0].Entries[0].Canonical != "Значение") {
				t.Fatal("документ изменился при разборе", input)
			}
		})
	}
}

func TestReferenceImportBodyLimit(t *testing.T) {
	body := strings.NewReader(strings.Repeat(" ", maxReferenceImportBody+1))
	request := httptest.NewRequest("POST", "/", body)
	request.Header.Set("Content-Type", "application/json")
	if _, err := decodeReferenceImport(httptest.NewRecorder(), request); err == nil {
		t.Fatal("тело больше потолка принято")
	}
}

func TestReferenceImportJSONAndYAMLShareDefaultedChecksum(t *testing.T) {
	jsonRequest := httptest.NewRequest("POST", "/", strings.NewReader(referenceImportJSONFixture))
	jsonRequest.Header.Set("Content-Type", "application/json")
	jsonInput, err := decodeReferenceImport(httptest.NewRecorder(), jsonRequest)
	if err != nil {
		t.Fatal(err)
	}
	yamlBody := "schemaVersion: 2\nsources:\n  - license: CC0-1.0\n    url: https://example.org/data\n" +
		"    id: s1\ndictionaries:\n  - entries:\n      - status: candidate\n        aliases: []\n" +
		"        canonical: Значение\n    release: 1\n" +
		"    aliasKinds: [spelling]\n" +
		"    limits: {maxEntries: 1000000, maxAliases: 50,\n" +
		"      maxAliasLength: 120, maxAliasWords: 8, normalizer: v1}\n" +
		"    kind: open\n    identity: false\n    code: d1\n"
	yamlRequest := httptest.NewRequest("POST", "/", strings.NewReader(yamlBody))
	yamlRequest.Header.Set("Content-Type", "application/yaml")
	yamlInput, err := decodeReferenceImport(httptest.NewRecorder(), yamlRequest)
	if err != nil {
		t.Fatal(err)
	}
	fromJSON, err := reference.PrepareImport(jsonInput)
	if err != nil {
		t.Fatal(err)
	}
	fromYAML, err := reference.PrepareImport(yamlInput)
	if err != nil || fromJSON[0].Delivery.Checksum != fromYAML[0].Delivery.Checksum {
		t.Fatal("эквивалентный YAML/default status имеет другую сумму", fromJSON, fromYAML, err)
	}
}

func TestReferenceImportEmptyDictionaryHTTP(t *testing.T) {
	for _, kind := range []string{"open", "closed"} {
		t.Run(kind, func(t *testing.T) {
			store := fake.NewStore()
			clock := fake.NewClock(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
			service := appreference.NewService(store, clock, &fake.IDs{}, nil, appreference.Config{})
			handler := referenceHandlers{service: service}
			body := strings.ReplaceAll(
				referenceImportJSONFixture,
				`"entries":[{"canonical":"Значение"}]`,
				`"entries":[]`,
			)
			body = strings.ReplaceAll(body, `"kind":"open"`, `"kind":"`+kind+`"`)
			for range 2 {
				request := httptest.NewRequest(http.MethodPost, "/import", strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				handler.importPackage(recorder, request)
				expected := http.StatusOK
				if kind == "closed" {
					expected = http.StatusBadRequest
				}
				if recorder.Code != expected {
					t.Fatal(recorder.Code, recorder.Body.String())
				}
			}
			dictionaries, err := service.DictionarySummaries(context.Background())
			if err != nil || (kind == "open" && (len(dictionaries) != 1 || dictionaries[0].Entries != 0)) ||
				(kind == "closed" && len(dictionaries) != 0) || len(store.OutboxRows()) != 0 {
				t.Fatal(dictionaries, err)
			}
		})
	}
}

func TestReferenceImportRejectsVersionOne(t *testing.T) {
	store := fake.NewStore()
	service := appreference.NewService(store, fake.NewClock(time.Now()), &fake.IDs{}, nil, appreference.Config{})
	handler := referenceHandlers{service: service}
	body := strings.Replace(referenceImportJSONFixture, `"schemaVersion":2`, `"schemaVersion":1`, 1)
	request := httptest.NewRequest(http.MethodPost, "/import", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.importPackage(response, request)
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), "reference.invalid_delivery") {
		t.Fatal(response.Code, response.Body.String())
	}
	dictionaries, err := service.DictionarySummaries(context.Background())
	if err != nil || len(dictionaries) != 0 {
		t.Fatal("версия 1 изменила реестр", dictionaries, err)
	}
}

func TestReferenceImportRequiresIdentity(t *testing.T) {
	for _, contentType := range []string{"application/json", "application/yaml"} {
		for _, value := range []string{"", `"identity":null,`} {
			body := strings.Replace(referenceImportJSONFixture, `"identity":false,`, value, 1)
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			request.Header.Set("Content-Type", contentType)
			_, err := decodeReferenceImport(httptest.NewRecorder(), request)
			if !errors.Is(err, reference.ErrInvalidSettings) {
				t.Fatal("identity не проверен", contentType, err)
			}
		}
	}
}
