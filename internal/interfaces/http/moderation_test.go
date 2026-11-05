package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const moderationURL = "/api/v1/operator/moderation"

type moderationEnv struct {
	handler http.Handler
	store   *fake.Store
	clock   *fake.Clock
	caseID  string
}

func newModerationEnv(t *testing.T) moderationEnv {
	t.Helper()
	store := fake.NewStore()
	clock := fake.NewClock(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	store.SeedParticipant(participant.State{
		TenantID: "t1",
		Name:     "Магазин",
		Slug:     "t1",
		Trust:    participant.Probation,
		Status:   participant.StatusConnected,
		Version:  1,
	})
	store.SeedTenant(participant.TenantState{TenantID: "t1", Slug: "t1", Status: participant.TenantActive, Version: 1})
	store.SeedTaxonomy(taxonomy.Definition{
		Version:    1,
		Categories: []taxonomy.Category{{ID: "c1", Name: "Категория", Slug: "c1"}},
	})
	service := appmoderation.NewService(store, clock, &fake.IDs{}, appmoderation.Config{ProbationOffers: 50})
	projector := appcatalog.NewProjector(store, clock, service)
	err := projector.ApplyProduct(context.Background(), appcatalog.ProductEvent{
		EventID: "e1",
		Snapshot: catalog.Snapshot{
			TenantID:  "t1",
			ProductID: "p1",
			Revision:  1,
			At:        clock.Now(),
			Card: catalog.Card{
				Name:        "Товар",
				Marketplace: &taxonomy.Card{CategoryID: "c1", Published: true},
			},
			PriceProfiles: []catalog.PriceProfile{{
				Key:            catalog.DefaultPriceProfile,
				Price:          money.FromKopecks(19999),
				EffectivePrice: money.FromKopecks(19999),
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.GetOffer(context.Background(), ids.OfferID{Tenant: "t1", Product: "p1"})
	if err != nil || view.Case == nil {
		t.Fatal(view, err)
	}
	handler := NewHandler(Deps{
		Moderation:    service,
		Participants:  appparticipant.NewService(store, clock, &fake.IDs{}),
		Catalog:       appcatalog.NewQueries(store, fake.NewIndex(), fake.NewPlatform()),
		OperatorToken: "token",
		Registerer:    prometheus.NewRegistry(),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return moderationEnv{handler: handler, store: store, clock: clock, caseID: string(view.Case.ID)}
}

func (e moderationEnv) request(t *testing.T, method, path, body, key string, status int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	e.handler.ServeHTTP(response, request)
	if response.Code != status {
		t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
	}
	return response
}

func TestModerationDecisionsHTTP(t *testing.T) {
	for _, action := range []string{"allow", "reject"} {
		t.Run(action, func(t *testing.T) {
			e := newModerationEnv(t)
			path := moderationURL + "/cases/" + e.caseID + "/" + action
			e.request(t, "POST", path, `{"note":"Заметка"}`, "", 400)
			e.request(t, "POST", path, `{"expectedVersion":1,"note":" "}`, "", 400)
			longNote := `{"expectedVersion":1,"note":"` + strings.Repeat("а", 2001) + `"}`
			e.request(t, "POST", path, longNote, "", 400)
			e.request(t, "POST", path, `{"expectedVersion":1,"note":"Заметка","reason":"unknown"}`, "", 400)
			conflict := e.request(t, "POST", path, `{"expectedVersion":2,"note":"Заметка"}`, "", 409)
			if !strings.Contains(conflict.Body.String(), `"case":`) {
				t.Fatal(conflict.Body.String())
			}
			body := `{"expectedVersion":1,"note":"Заметка"}`
			first := e.request(t, "POST", path, body, "k1", 200)
			second := e.request(t, "POST", path, body, "k1", 200)
			if first.Body.String() != second.Body.String() {
				t.Fatal("повтор изменил ответ")
			}
			e.request(t, "POST", moderationURL+"/cases", `{"offerId":"t1~p1","note":"Проверка"}`, "", 201)
			replay := e.request(t, "POST", path, body, "k1", 200)
			if first.Body.String() != replay.Body.String() {
				t.Fatal("новое дело изменило сохранённый ответ")
			}

			reused := e.request(t, "POST", path, `{"expectedVersion":1,"note":"Другая"}`, "k1", 409)
			if !strings.Contains(reused.Body.String(), "IDEMPOTENCY_KEY_REUSED") {
				t.Fatal(reused.Body.String())
			}
			e.request(t, "POST", path, body, "", 409)
			e.clock.Advance(appmoderation.DecisionTTL)
			e.request(t, "POST", path, body, "k1", 409)
			offer := e.request(t, "GET", moderationURL+"/offers/t1~p1", "", "", 200)
			var view struct {
				History []moderation.Decision `json:"history"`
			}
			if err := json.Unmarshal(offer.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if len(view.History) != 1 || view.History[0].Operator.ID != "machine" {
				t.Fatal(view)
			}
		})
	}
}

func TestModerationStaleHTTP(t *testing.T) {
	e := newModerationEnv(t)
	p, ok := e.store.Product("t1", "p1")
	if !ok {
		t.Fatal("нет проекции")
	}
	p.Revision++
	e.store.SeedProduct(p)
	response := e.request(
		t,
		"POST",
		moderationURL+"/cases/"+e.caseID+"/allow",
		`{"expectedVersion":1,"note":"Заметка"}`,
		"",
		409,
	)
	if !strings.Contains(response.Body.String(), "moderation.case_stale") {
		t.Fatal(response.Body.String())
	}
}

func TestModerationQueriesHTTP(t *testing.T) {
	e := newModerationEnv(t)
	for _, path := range []string{
		"/cases",
		"/cases?status=all",
		"/cases/" + e.caseID,
		"/offers/t1~p1",
		"/verdicts?status=pending",
	} {
		e.request(t, "GET", moderationURL+path, "", "", 200)
	}
	for _, path := range []string{
		"/cases?status=bad",
		"/cases?source=bad",
		"/cases?limit=201",
		"/cases?offset=-1",
		"/verdicts",
	} {
		e.request(t, "GET", moderationURL+path, "", "", 400)
	}
	card := e.request(t, "GET", moderationURL+"/cases/"+e.caseID, "", "", 200)
	if !strings.Contains(card.Body.String(), `"price":19999`) ||
		!strings.Contains(card.Body.String(), `"categoryPath":["Категория"]`) {
		t.Fatal(card.Body.String())
	}
	e.request(t, "GET", moderationURL+"/offers/t1~p2", "", "", 404)
	e.request(t, "POST", moderationURL+"/cases", `{"offerId":"t1~p1","note":"Проверка"}`, "", 409)
	e.request(t, "POST", moderationURL+"/cases/"+e.caseID+"/allow", `{"expectedVersion":1,"note":"Заметка"}`, "", 200)
	e.request(t, "POST", moderationURL+"/cases", `{"offerId":"t1~p1","note":"Проверка"}`, "", 201)
	response := e.request(t, "GET", "/api/v1/operator/catalog/offers/t1~p1/publication", "", "", 200)
	if !strings.Contains(response.Body.String(), `"moderation":{"status":"pending"`) {
		t.Fatal(response.Body.String())
	}
}

func TestModerationDictionariesHTTP(t *testing.T) {
	e := newModerationEnv(t)
	e.request(t, "GET", moderationURL+"/stop-phrases", "", "", 200)
	e.request(t, "POST", moderationURL+"/stop-phrases", `{"text":"Товар"}`, "", 201)
	duplicate := e.request(t, "POST", moderationURL+"/stop-phrases", `{"text":" товар "}`, "", 409)
	if !strings.Contains(duplicate.Body.String(), "moderation.stop_phrase_exists") {
		t.Fatal(duplicate.Body.String())
	}
	e.request(t, "DELETE", moderationURL+"/stop-phrases/товар", "", "", 204)
	e.request(t, "DELETE", moderationURL+"/stop-phrases/товар", "", "", 404)
	e.request(t, "GET", moderationURL+"/reasons", "", "", 200)
	e.request(t, "POST", moderationURL+"/reasons", `{"code":"r1","message":"Причина"}`, "", 201)
	e.request(t, "PUT", moderationURL+"/reasons/r1", `{"message":"Текст"}`, "", 200)
	e.request(t, "POST", moderationURL+"/reasons", `{"code":"r1","message":"Текст"}`, "", 409)
	e.request(t, "PUT", moderationURL+"/reasons/r2", `{"message":"Текст"}`, "", 404)
}

func TestModerationParticipantHTTP(t *testing.T) {
	e := newModerationEnv(t)
	e.request(t, "POST", moderationURL+"/reasons", `{"code":"r1","message":"Текст"}`, "", 201)
	prefix := "/api/v1/operator/participants/t1"
	e.request(t, "POST", prefix+"/enable", `{"reason":"r1","note":"Заметка"}`, "", 200)
	e.request(t, "POST", prefix+"/disable", `{"reason":"r1","note":"Заметка"}`, "", 200)
	e.request(t, "POST", prefix+"/enable", "", "", 200)
	response := e.request(t, "PUT", prefix+"/trust", `{"trust":"trusted","reason":"r1","note":"Заметка"}`, "", 200)
	if !strings.Contains(response.Body.String(), `"trust":"trusted"`) ||
		!strings.Contains(response.Body.String(), `"approvedOffers":0`) {
		t.Fatal(response.Body.String())
	}
	e.request(t, "PUT", prefix+"/trust", `{"trust":"trusted","note":" "}`, "", 400)
	history := e.request(t, "GET", prefix+"/decisions?limit=2&offset=0", "", "", 200)
	if !strings.Contains(history.Body.String(), `"total":4`) {
		t.Fatal(history.Body.String())
	}
}

func TestModerationM4Responses(t *testing.T) {
	e := newModerationEnv(t)
	e.request(t, "POST", moderationURL+"/reasons", `{"code":"r1","message":"Текст"}`, "", 201)
	duplicate := e.request(t, "POST", moderationURL+"/reasons", `{"code":"r1","message":"Текст"}`, "", 409)
	if !strings.Contains(duplicate.Body.String(), "moderation.reason_exists") {
		t.Fatal(duplicate.Body.String())
	}
	prefix := "/api/v1/operator/participants/t1"
	for _, action := range []string{"enable", "disable"} {
		response := e.request(t, "POST", prefix+"/"+action, `{"reason":"r2"}`, "", 400)
		if !strings.Contains(response.Body.String(), "moderation.reason_unknown") {
			t.Fatal(response.Body.String())
		}
	}
	e.request(t, "PUT", prefix+"/trust", `{"trust":"trusted","reason":"r2","note":"Заметка"}`, "", 400)
	e.request(t, "PUT", prefix+"/trust", `{"trust":"trusted","reason":"r1","note":"Заметка"}`, "", 200)
	e.request(t, "PUT", moderationURL+"/reasons/r1", `{"message":"Изменено"}`, "", 200)
	history := e.request(t, "GET", prefix+"/decisions", "", "", 200)
	if !strings.Contains(history.Body.String(), `"message":"Текст"`) ||
		!strings.Contains(history.Body.String(), `"by":"operator"`) {
		t.Fatal(history.Body.String())
	}
	for _, trust := range []string{"trusted", "probation"} {
		response := e.request(t, "GET", "/api/v1/operator/participants?trust="+trust, "", "", 200)
		want := `"total":0`
		if trust == "trusted" {
			want = `"total":1`
		}
		if !strings.Contains(response.Body.String(), want) {
			t.Fatal(response.Body.String())
		}
	}
	e.request(t, "GET", "/api/v1/operator/participants?trust=unknown", "", "", 400)
}

func TestModerationM4CaseDecision(t *testing.T) {
	e := newModerationEnv(t)
	e.request(t, "POST", moderationURL+"/reasons", `{"code":"r1","message":"Текст"}`, "", 201)
	path := moderationURL + "/cases/" + e.caseID
	e.request(t, "POST", path+"/reject", `{"expectedVersion":1,"reason":"r1","note":"Заметка"}`, "", 200)
	e.request(t, "PUT", moderationURL+"/reasons/r1", `{"message":"Изменено"}`, "", 200)
	details := e.request(t, "GET", path, "", "", 200)
	var body struct {
		History []struct {
			Reason moderation.ReasonCode `json:"reason"`
		} `json:"history"`
		Decision struct {
			Reason moderation.ReasonCode `json:"reason"`
		} `json:"decision"`
	}
	if err := json.Unmarshal(details.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.History) != 1 || body.History[0].Reason.Text != "Текст" || body.Decision.Reason.Text != "Текст" {
		t.Fatal(details.Body.String())
	}
}
