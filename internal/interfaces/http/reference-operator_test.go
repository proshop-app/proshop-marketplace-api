package http_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
)

const operatorReferenceImportFixture = `{
    "schemaVersion":1,
    "sources":[{"id":"s1","url":"https://example.org/data","license":"CC0-1.0"}],
    "dictionaries":[{"code":"d1","kind":"closed","release":1,"entries":[{"canonical":"Значение"}]}]
}`

func TestOperatorReferenceImportAndApprovalUseExistingCookieCSRF(t *testing.T) {
	store := fake.NewStore()
	service := appreference.NewService(store, fake.NewClock(t0), &fake.IDs{}, &fake.Publisher{}, appreference.Config{})
	e := newEnv(t, func(deps *transport.Deps) {
		deps.Reference = service
	})
	origin := "https://admin.example"
	login := operatorCall(t, e, "POST", sessionPath, `{"email":"op@example.test","password":"password"}`, "", origin)
	cookies := (&http.Response{Header: login.header}).Cookies()
	if login.status != http.StatusOK || len(cookies) != 2 {
		t.Fatal(login)
	}
	cookie := cookies[0].Name + "=" + cookies[0].Value
	path := "/api/v1/operator/reference/import"
	cross := operatorCall(t, e, "POST", path, operatorReferenceImportFixture, cookie, "https://other.example")
	if cross.status != http.StatusForbidden {
		t.Fatal("импорт обошёл CSRF", cross)
	}
	imported := operatorCall(t, e, "POST", path, operatorReferenceImportFixture, cookie, origin)
	if imported.status != http.StatusOK {
		t.Fatal(imported)
	}
	listed := operatorCall(t, e, "GET", "/api/v1/operator/reference", "", cookie, origin)
	if listed.status != http.StatusOK {
		t.Fatal(listed)
	}
	dictionary := listed.body["dictionaries"].([]any)[0].(map[string]any)
	if dictionary["candidates"] != float64(1) || dictionary["kind"] != "closed" || dictionary["entries"] != float64(1) {
		t.Fatal(listed)
	}
	entries := operatorCall(t, e, "GET", "/api/v1/operator/reference/d1/entries?status=candidate", "", cookie, origin)
	if entries.status != http.StatusOK || entries.body["total"] != float64(1) {
		t.Fatal(entries)
	}
	entry := entries.body["entries"].([]any)[0].(map[string]any)
	entryPath := "/api/v1/operator/reference/d1/entries/" + entry["id"].(string)
	etag := `"` + strconv.FormatInt(int64(entry["revision"].(float64)), 10) + `"`
	approved := referenceApprove(t, e, entryPath, cookie, origin, etag)
	if approved.status != http.StatusOK || approved.body["entry"].(map[string]any)["status"] != "confirmed" {
		t.Fatal(approved)
	}
	stale := referenceApprove(t, e, entryPath, cookie, origin, etag)
	if stale.status != http.StatusConflict {
		t.Fatal("устаревшая ревизия принята", stale)
	}
}

func TestReferenceReadTokenDoesNotGrantOperatorImportOrSummary(t *testing.T) {
	e := newReferenceEnv(t)
	for _, call := range []referenceCall{
		{method: "GET", path: "/api/v1/operator/reference", bearer: readToken},
		{
			method: "POST",
			path:   "/api/v1/operator/reference/import",
			bearer: readToken,
			body:   operatorReferenceImportFixture,
		},
		{method: "GET", path: "/api/v1/operator/reference/b1/entries", bearer: readToken},
	} {
		if reply := e.do(t, call); reply.status != http.StatusUnauthorized {
			t.Fatal("токен читателя получил операторский доступ", reply)
		}
	}
}

func referenceApprove(t *testing.T, e *env, path, cookie, origin, etag string) response {
	t.Helper()
	request, err := http.NewRequest("PATCH", e.server.URL+path, strings.NewReader(`{"status":"confirmed"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Cookie", cookie)
	request.Header.Set("Origin", origin)
	request.Header.Set("X-Requested-With", "KvazarOperator")
	request.Header.Set("If-Match", etag)
	raw, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Body.Close() }()
	body, err := io.ReadAll(raw.Body)
	if err != nil {
		t.Fatal(err)
	}
	result := response{status: raw.StatusCode, header: raw.Header}
	if err := json.Unmarshal(body, &result.body); err != nil {
		t.Fatal(err)
	}
	return result
}
