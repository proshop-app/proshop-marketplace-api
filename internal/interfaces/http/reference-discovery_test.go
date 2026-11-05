package http_test

import (
	"net/http"
	"testing"
)

func TestReferenceConsumerContract(t *testing.T) {
	e := newReferenceEnv(t)
	created := e.do(t, referenceCall{
		method:     http.MethodPost,
		path:       "/internal/reference/b1/entries",
		body:       `{"canonical":"Первое","metadata":{"directions":["d1"],"order":7,"label":"Название"}}`,
		authorized: true,
	})
	id := created.entry(t)["id"].(string)
	for _, path := range []string{"/internal/reference", "/api/v1/internal/reference"} {
		reply := e.do(t, referenceCall{method: http.MethodGet, path: path, bearer: readToken})
		if reply.status != http.StatusOK || reply.body["schemaVersion"] != float64(1) {
			t.Fatalf("discovery: %+v", reply)
		}
		dictionaries := reply.body["dictionaries"].([]any)
		dict := dictionaries[0].(map[string]any)
		if dict["code"] != "b1" || dict["kind"] != "open" || dict["revision"] != float64(1) {
			t.Fatalf("словарь: %v", dict)
		}
		denied := e.do(t, referenceCall{method: http.MethodGet, path: path})
		if denied.status != http.StatusUnauthorized {
			t.Fatal("discovery без токена")
		}
	}
	snapshot := e.do(t, referenceCall{
		method: http.MethodGet,
		path:   "/api/v1/internal/reference/b1/snapshot?since=0",
		bearer: readToken,
	})
	if snapshot.status != http.StatusOK || snapshot.body["kind"] != "open" {
		t.Fatalf("снимок: %+v", snapshot)
	}
	entry := snapshot.body["entries"].([]any)[0].(map[string]any)
	if entry["order"] != float64(7) || entry["label"] != "Название" || entry["directions"].([]any)[0] != "d1" {
		t.Fatalf("поля снимка: %v", entry)
	}
	for _, test := range []struct {
		id     string
		status int
	}{
		{id: id, status: http.StatusOK},
		{id: "00000000-0000-0000-0000-000000000099", status: http.StatusNotFound},
	} {
		reply := e.do(t, referenceCall{
			method: http.MethodGet,
			path:   "/api/v1/internal/reference/b1/entries/" + test.id,
			bearer: readToken,
		})
		if reply.status != test.status {
			t.Fatalf("запись: %+v", reply)
		}
	}
}
