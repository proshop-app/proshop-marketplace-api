package http_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/event"
)

const (
	sessionPath      = "/api/v1/operator/session"
	participantsPath = "/api/v1/operator/participants"
	deliveryPath     = "/api/v1/operator/participants/t1/delivery"
	eventPath        = "/api/v1/operator/events/1"
)

func operatorCall(t *testing.T, e *env, method, path, body, cookie, origin string, key ...string) response {
	t.Helper()
	req, err := http.NewRequest(method, e.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if len(key) > 0 {
		req.Header.Set("Idempotency-Key", key[0])
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("X-Requested-With", "KvazarOperator")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	req.Header.Set("X-Marketplace-Money-Unit", "RUB")
	raw, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Body.Close() }()
	out := response{status: raw.StatusCode, header: raw.Header}
	if raw.StatusCode != http.StatusNoContent {
		data, err := io.ReadAll(raw.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &out.body); err != nil {
			t.Fatal(string(data), err)
		}
	}
	return out
}

func TestOperatorCookieRoutesAndCSRF(t *testing.T) {
	e := newEnv(t)
	origin := "https://admin.example"
	call := func(method, path, body, cookie string) response {
		return operatorCall(t, e, method, path, body, cookie, origin)
	}
	credentials := `{"email":"op@example.test","password":"password"}`
	crossLogin := operatorCall(t, e, "POST", sessionPath, credentials, "", "https://other.example")
	if crossLogin.status != http.StatusForbidden {
		t.Fatal(crossLogin)
	}
	login := call("POST", sessionPath, credentials, "")
	if login.status != http.StatusOK || login.body["role"] != "operator" {
		t.Fatal(login)
	}
	cookies := (&http.Response{Header: login.header}).Cookies()
	if len(cookies) != 2 {
		t.Fatal(cookies)
	}
	c := cookies[0]
	if c.Name != "__Host-kvazar_operator" || !c.Secure || !c.HttpOnly || c.Domain != "" ||
		c.Path != "/" || c.SameSite != http.SameSiteStrictMode || (c.MaxAge <= 0 || c.MaxAge > 3600) {
		t.Fatal(c)
	}
	cookie := c.Name + "=" + c.Value + "; " + cookies[1].Name + "=" + cookies[1].Value
	if strings.Contains(fmtBody(login.body), c.Value) {
		t.Fatal("токен попал в JSON")
	}
	if r := call("GET", sessionPath, "", cookie); r.status != http.StatusOK {
		t.Fatal(r)
	}
	if r := call("GET", participantsPath, "", cookie); r.status != http.StatusOK {
		t.Fatal(r)
	}
	e.seedTenant("t1", "s1")
	createBody := `{"tenantId":"t1","name":"Магазин"}`
	crossCreate := operatorCall(t, e, "POST", participantsPath, createBody, cookie, "https://other.example")
	if crossCreate.status != http.StatusForbidden {
		t.Fatal(crossCreate)
	}
	created := call("POST", participantsPath, createBody, cookie)
	if created.status != http.StatusCreated {
		t.Fatal(created)
	}
	emptyDelivery := created.body["delivery"].(map[string]any)
	for _, key := range []string{"regions", "carriers"} {
		array, ok := emptyDelivery[key].([]any)
		if !ok || len(array) != 0 {
			t.Fatal("пустое покрытие сериализуется массивами", created)
		}
	}
	delivery := `{"version":1,"delivery":{"regions":["RU-MOW"],"carriers":["cdek","yandex"]}}`
	updated := call("PUT", deliveryPath, delivery, cookie)
	if updated.status != http.StatusOK || updated.body["version"] != float64(2) {
		t.Fatal(updated)
	}
	if r := call("PUT", deliveryPath, delivery, cookie); r.status != http.StatusConflict {
		t.Fatal(r)
	}
	if err := messaging.NewParker(e.store).Park(context.Background(), ports.ParkedEvent{
		Topic:    "topic",
		TenantID: "t1",
		ParkedAt: t0,
		Value:    []byte(`{"password":"secret","phone":"personal","operation":"upsert"}`),
		Error:    "secret",
		Reason:   "invalid",
	}); err != nil {
		t.Fatal(err)
	}
	listed := call("GET", "/api/v1/operator/events?status=parked&tenantId=t1&limit=1", "", cookie)
	if listed.status != http.StatusOK || listed.body["total"] != float64(1) {
		t.Fatal(listed)
	}
	if strings.Contains(fmtBody(listed.body), "secret") || strings.Contains(fmtBody(listed.body), "personal") {
		t.Fatal("секреты в ответе", listed)
	}
	invalidNote := call("POST", eventPath+"/resolve", `{"expectedVersion":1,"note":" "}`, cookie)
	if invalidNote.status != http.StatusBadRequest {
		t.Fatal(invalidNote)
	}
	retried := call("POST", eventPath+"/retry", `{"expectedVersion":1,"note":"исправлено"}`, cookie)
	if retried.status != http.StatusOK || retried.body["status"] != "queued" {
		t.Fatal(retried)
	}
	for _, body := range []string{`{"note":"без версии"}`, `{"expectedVersion":0,"note":"нулевая версия"}`} {
		if r := call("POST", eventPath+"/retry", body, cookie); r.status != http.StatusBadRequest {
			t.Fatal(r)
		}
	}
	replay := messaging.NewEvents(e.store, e.clock, &fake.Replay{Err: event.ErrFilter})
	var task ports.Task
	tasksBefore := len(e.store.OutboxRows())
	for _, row := range e.store.OutboxRows() {
		if row.Kind == messaging.TaskReplay {
			task = ports.Task{Payload: row.Payload, Attempts: 1}
		}
	}
	if err := replay.HandleTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"retry", "resolve"} {
		r := call("POST", eventPath+"/"+action, `{"expectedVersion":1,"note":"задержавшееся решение"}`, cookie)
		if r.status != http.StatusConflict {
			t.Fatal(r)
		}
		current := r.body["event"].(map[string]any)
		if current["status"] != "parked" || current["version"] != float64(3) {
			t.Fatal("409 возвращает текущее состояние", r)
		}
	}
	if len(e.store.OutboxRows()) != tasksBefore {
		t.Fatal("устаревшие решения создали задачи", e.store.OutboxRows())
	}
	logout := call("DELETE", sessionPath, "", cookie)
	if logout.status != http.StatusNoContent || !strings.Contains(logout.header.Get("Set-Cookie"), "Max-Age=0") {
		t.Fatal(logout)
	}
	if r := call("GET", sessionPath, "", ""); r.status != http.StatusUnauthorized {
		t.Fatal(r)
	}
	if r := call("POST", sessionPath+"/refresh", "", cookie); r.status != http.StatusUnauthorized {
		t.Fatal(r)
	}
	if r := e.call(t, "GET", "/api/v1/operator/events", "", true); r.status != http.StatusOK {
		t.Fatal(r)
	}
}

func fmtBody(body map[string]any) string { raw, _ := json.Marshal(body); return string(raw) }

func TestOperatorDecisionIdempotency(t *testing.T) {
	for _, action := range []string{"retry", "resolve"} {
		t.Run(action, func(t *testing.T) {
			e := newEnv(t)
			origin := "https://admin.example"
			login := operatorCall(
				t,
				e,
				"POST",
				sessionPath,
				`{"email":"op@example.test","password":"password"}`,
				"",
				origin,
			)
			cookie := (&http.Response{Header: login.header}).Cookies()[0]
			if err := messaging.NewParker(e.store).Park(context.Background(), ports.ParkedEvent{
				Topic:    "topic",
				TenantID: "t1",
				ParkedAt: t0,
			}); err != nil {
				t.Fatal(err)
			}
			call := func(body string) response {
				return operatorCall(
					t,
					e,
					"POST",
					eventPath+"/"+action,
					body,
					cookie.Name+"="+cookie.Value,
					origin,
					"decision-key",
				)
			}
			body := `{"expectedVersion":1,"note":"исправлено"}`
			first := call(body)
			if first.status != http.StatusOK {
				t.Fatal(first)
			}
			if action == "retry" {
				row := e.store.OutboxRows()[0]
				service := messaging.NewEvents(e.store, e.clock, &fake.Replay{})
				if err := service.HandleTask(context.Background(), ports.Task{Payload: row.Payload}); err != nil {
					t.Fatal(err)
				}
			}
			again := call(body)
			if again.status != http.StatusOK || fmtBody(again.body) != fmtBody(first.body) {
				t.Fatal("повтор должен вернуть первоначальный снимок", first, again)
			}
			for _, changed := range []string{
				`{"expectedVersion":1,"note":"другая заметка"}`,
				`{"expectedVersion":2,"note":"исправлено"}`,
			} {
				reused := call(changed)
				if reused.status != http.StatusConflict ||
					reused.body["error"].(map[string]any)["code"] != "IDEMPOTENCY_KEY_REUSED" {
					t.Fatal(reused)
				}
			}
			expectedTasks := 0
			if action == "retry" {
				expectedTasks = 1
			}
			if len(e.store.OutboxRows()) != expectedTasks {
				t.Fatal("повтор создал задачу")
			}
		})
	}
}
