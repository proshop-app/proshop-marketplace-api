package http_test

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"testing"
)

func TestReferenceConsumerContract(t *testing.T) {
	e := newReferenceEnv(t)
	created := e.do(t, referenceCall{
		method: http.MethodPost,
		path:   "/internal/reference/b1/entries",
		body: `{"canonical":"Первое","status":"confirmed",` +
			`"metadata":{"directions":["d1"],"order":7,"label":"Название"}}`,
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
		path:   "/api/v1/internal/reference/b1/snapshot",
		bearer: readToken,
	})
	if snapshot.status != http.StatusOK {
		t.Fatalf("снимок: %+v", snapshot)
	}
	raw, err := os.ReadFile("testdata/reference-snapshot-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]any
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.body, golden) {
		actual, _ := json.Marshal(snapshot.body)
		t.Fatalf("снимок отличается от эталона: %s", actual)
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
