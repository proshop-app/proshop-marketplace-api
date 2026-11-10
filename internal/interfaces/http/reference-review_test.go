package http_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
)

const reviewPath = "/api/v1/operator/reference/b1/entries/review"

func TestReferenceBulkReviewExplicitAndAllExclusionsHTTP(t *testing.T) {
	e := newReferenceEnv(t)
	selected := []string{}
	for i := 0; i < 3; i++ {
		created := e.do(t, referenceCall{
			method:     "POST",
			path:       "/internal/reference/b1/entries",
			authorized: true,
			body:       fmt.Sprintf(`{"canonical":"Значение %d","status":"candidate"}`, i),
		})
		if created.status != 201 {
			t.Fatal(created)
		}
		selected = append(selected, created.entry(t)["id"].(string))
	}
	page := e.do(t, referenceCall{
		method:     "GET",
		path:       "/api/v1/operator/reference/b1/entries?status=candidate",
		authorized: true,
	})
	if page.etag != `"reference-3"` || page.body["revision"] != float64(3) {
		t.Fatal(page)
	}
	explicit := fmt.Sprintf(`{"status":"confirmed","selection":{"mode":"ids","ids":[%q]}}`, selected[0])
	approved := e.do(t, referenceCall{
		method:     "POST",
		path:       reviewPath,
		body:       explicit,
		ifMatch:    page.etag,
		authorized: true,
	})
	if approved.status != 200 || approved.etag != `"reference-4"` || approved.body["changed"] != float64(1) ||
		approved.body["dictionary"] != "b1" || approved.body["schemaVersion"] != float64(1) {
		t.Fatal(approved)
	}
	stale := e.do(t, referenceCall{
		method:     "POST",
		path:       reviewPath,
		body:       explicit,
		ifMatch:    page.etag,
		authorized: true,
	})
	if stale.status != 409 || stale.errorCode() != "reference.dictionary_changed" {
		t.Fatal(stale)
	}
	template := `{"status":"rejected","selection":{"mode":"allCandidates","excludedIds":[%q]},` +
		`"reason":"Проверено"}`
	all := fmt.Sprintf(template, selected[1])
	rejected := e.do(t, referenceCall{
		method:     "POST",
		path:       reviewPath,
		body:       all,
		ifMatch:    approved.etag,
		authorized: true,
	})
	if rejected.status != 200 || rejected.body["changed"] != float64(1) || rejected.etag != `"reference-5"` {
		t.Fatal(rejected)
	}
	remaining := e.do(t, referenceCall{
		method:     "GET",
		path:       "/api/v1/operator/reference/b1/entries?status=candidate",
		authorized: true,
	})
	entries := remaining.body["entries"].([]any)
	if remaining.body["total"] != float64(1) || entries[0].(map[string]any)["id"] != selected[1] {
		t.Fatal(remaining)
	}
}

func TestReferenceBulkReviewZeroHeadAuthAndStrictRequest(t *testing.T) {
	e := newReferenceEnv(t)
	all := `{"status":"confirmed","selection":{"mode":"allCandidates","excludedIds":[]}}`
	for _, call := range []referenceCall{
		{method: "POST", path: reviewPath, body: all, ifMatch: `"reference-0"`},
		{method: "POST", path: reviewPath, body: all, ifMatch: `"reference-0"`, bearer: readToken},
	} {
		if reply := e.do(t, call); reply.status != 401 {
			t.Fatal(reply)
		}
	}
	empty := e.do(t, referenceCall{
		method:     "POST",
		path:       reviewPath,
		body:       all,
		ifMatch:    `"reference-0"`,
		authorized: true,
	})
	if empty.status != 200 || empty.body["changed"] != float64(0) || empty.etag != `"reference-0"` {
		t.Fatal(empty)
	}
	id := "00000000-0000-0000-0000-000000000001"
	invalid := []string{
		`null`,
		`[]`,
		`{}`,
		`{"status":"confirmed","selection":null}`,
		`{"status":"confirmed","selection":{"mode":"allCandidates"}}`,
		`{"status":"confirmed","selection":{"mode":"allCandidates","excludedIds":null}}`,
		`{"status":"confirmed","selection":{"mode":"allCandidates","excludedIds":[],"ids":null}}`,
		`{"status":"confirmed","selection":{"mode":"ids","ids":[]}}`,
		`{"status":"deprecated","selection":{"mode":"allCandidates","excludedIds":[]}}`,
		`{"status":"confirmed","selection":{"mode":"allCandidates","excludedIds":[]},"unexpected":true}`,
		`{"status":"confirmed","selection":{"mode":"allCandidates","excludedIds":[],"unexpected":true}}`,
		`{"status":"confirmed","status":"rejected","selection":{"mode":"allCandidates","excludedIds":[]}}`,
		`{"status":"confirmed","selection":{"mode":"allCandidates","excludedIds":[]},"reason":null}`,
		fmt.Sprintf(`{"status":"confirmed","selection":{"mode":"ids","ids":[%q],"excludedIds":null}}`, id),
		fmt.Sprintf(`{"status":"confirmed","selection":{"mode":"ids","ids":[%q,%q]}}`, id, id),
		`{"status":"confirmed","selection":{"mode":"ids","ids":["wrong"]}}`,
		all + `{}`,
		strings.Repeat(" ", 513<<10) + all,
	}
	for _, body := range invalid {
		reply := e.do(t, referenceCall{
			method:     "POST",
			path:       reviewPath,
			body:       body,
			ifMatch:    `"reference-0"`,
			authorized: true,
		})
		if reply.status != 400 {
			t.Fatal("нестрогий запрос принят", body[:min(200, len(body))], reply)
		}
	}
	missing := fmt.Sprintf(`{"status":"confirmed","selection":{"mode":"ids","ids":[%q]}}`, id)
	reply := e.do(t, referenceCall{
		method:     "POST",
		path:       reviewPath,
		body:       missing,
		ifMatch:    `"reference-0"`,
		authorized: true,
	})
	if reply.status != 409 || reply.errorCode() != "reference.selection_changed" {
		t.Fatal(reply)
	}
}

func TestReferenceBulkReviewCookieCSRF(t *testing.T) {
	store := fake.NewStore()
	store.SeedReferenceDictionary(reference.DictionaryState{
		Code:       "b1",
		Kind:       reference.KindOpen,
		Limits:     reference.DefaultLimits(),
		AliasKinds: reference.AliasKinds,
	})
	e := newEnv(t, func(deps *transport.Deps) {
		deps.Reference = appreference.NewService(store, fake.NewClock(t0), &fake.IDs{}, nil, appreference.Config{})
	})
	origin := "https://admin.example"
	login := operatorCall(t, e, "POST", sessionPath, `{"email":"op@example.test","password":"password"}`, "", origin)
	cookies := (&http.Response{Header: login.header}).Cookies()
	if login.status != 200 || len(cookies) != 2 {
		t.Fatal(login)
	}
	cookie := cookies[0].Name + "=" + cookies[0].Value
	for _, requestOrigin := range []string{"https://other.example", origin} {
		request, err := http.NewRequest(
			"POST",
			e.server.URL+reviewPath,
			strings.NewReader(`{"status":"confirmed","selection":{"mode":"allCandidates","excludedIds":[]}}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Cookie", cookie)
		request.Header.Set("Origin", requestOrigin)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Requested-With", "KvazarOperator")
		request.Header.Set("If-Match", `"reference-0"`)
		raw, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		err = json.NewDecoder(raw.Body).Decode(&body)
		_ = raw.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if requestOrigin != origin && raw.StatusCode != 403 {
			t.Fatal("CSRF пропущен", raw.StatusCode)
		}
		if requestOrigin == origin && raw.StatusCode != 200 {
			t.Fatal("собственная cookie отклонена", body)
		}
	}
}
