package http

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

const referenceImportJSONFixture = `{
    "schemaVersion":1,
    "sources":[{"id":"s1","url":"https://example.org/data","license":"CC0-1.0"}],
    "dictionaries":[{"code":"d1","kind":"open","release":1,"entries":[{"canonical":"Значение"}]}]
}`

func TestReferenceImportStrictJSONAndYAML(t *testing.T) {
	yamlBody := "schemaVersion: 1\nsources:\n  - id: s1\n    url: https://example.org/data\n" +
		"    license: CC0-1.0\ndictionaries:\n  - code: d1\n    kind: open\n    release: 1\n" +
		"    entries:\n      - canonical: Значение\n"
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
		{name: "лишнее поле", contentType: "application/json", body: `{"schemaVersion":1,"extra":true}`},
		{name: "повтор JSON", contentType: "application/json", body: `{"schemaVersion":1,"schemaVersion":2}`},
		{name: "повтор YAML", contentType: "application/yaml", body: "schemaVersion: 1\nschemaVersion: 1\n"},
		{name: "несколько документов", contentType: "application/yaml", body: yamlBody + "---\nschemaVersion: 1\n"},
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
			if test.valid && (input.SchemaVersion != 1 || input.Dictionaries[0].Entries[0].Canonical != "Значение") {
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
	yamlBody := "schemaVersion: 1\nsources:\n  - license: CC0-1.0\n    url: https://example.org/data\n" +
		"    id: s1\ndictionaries:\n  - entries:\n      - status: candidate\n        aliases: []\n" +
		"        canonical: Значение\n    release: 1\n    kind: open\n    code: d1\n"
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
