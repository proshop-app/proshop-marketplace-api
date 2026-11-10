package http_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
)

func TestScopedHintRulesRequireOperatorCookieCSRFAndDoNotGrantReadTokenWrites(t *testing.T) {
	store := fake.NewStore()
	store.SeedTaxonomy(taxonomy.Definition{
		Version:    1,
		Attributes: []taxonomy.Attribute{{Code: "a1", Name: "Поле", Type: "string"}},
		Schemas:    map[string]taxonomy.Schema{"s1": {Attributes: []string{"a1"}}},
		Categories: []taxonomy.Category{{ID: "c1", Name: "Категория", Slug: "c1", Schema: "s1"}},
	})
	store.SeedReferenceDictionary(reference.DictionaryState{Code: "d1", Kind: reference.KindOpen})
	e := newEnv(t, func(deps *transport.Deps) {
		deps.Taxonomy = apptaxonomy.NewService(store, fake.NewClock(t0), nil)
	})
	origin := "https://admin.example"
	login := operatorCall(t, e, "POST", sessionPath, `{"email":"op@example.test","password":"password"}`, "", origin)
	cookies := (&http.Response{Header: login.header}).Cookies()
	if login.status != http.StatusOK || len(cookies) != 2 {
		t.Fatal(login)
	}
	cookie := cookies[0].Name + "=" + cookies[0].Value
	for _, source := range []string{"https://other.example", origin} {
		request, err := http.NewRequest(
			"PUT",
			e.server.URL+
				"/api/v1/operator/taxonomy/dictionary-bindings/a1?categoryId=c1",
			strings.NewReader(`{"mode":"extend","dictionaries":["d1"]}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Cookie", cookie)
		request.Header.Set("Origin", source)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Requested-With", "KvazarOperator")
		request.Header.Set("If-Match", `"taxonomy-1"`)
		raw, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, raw.Body)
		_ = raw.Body.Close()
		expected := http.StatusForbidden
		if source == origin {
			expected = http.StatusOK
		}
		if raw.StatusCode != expected {
			t.Fatal("неверная проверка CSRF", source, raw.StatusCode)
		}
	}
	ref := newReferenceEnv(t)
	for _, method := range []string{"PUT", "DELETE"} {
		reply := ref.do(t, referenceCall{
			method: method,
			path:   "/api/v1/operator/taxonomy/dictionary-bindings/a1",
			bearer: readToken,
			body:   `{"mode":"replace","dictionaries":[]}`,
		})
		if reply.status != http.StatusUnauthorized {
			t.Fatal("read token получил право менять rules", method, reply)
		}
	}
}
